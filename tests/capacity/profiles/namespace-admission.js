// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { env, integer } from '../lib/config.js';
import { graphql } from '../lib/graphql.js';

const apiA = env('CAPACITY_API_A');
const apiB = env('CAPACITY_API_B');
const token = env('CAPACITY_TOKEN');
const runID = env('CAPACITY_RUN_ID').toLowerCase().replace(/[^a-z0-9]/g, '').slice(-20);
const rate = integer('CAPACITY_RATE', 10);
const burstRounds = integer('CAPACITY_BURST_ROUNDS', 0, 0);
const burstRate = integer('CAPACITY_BURST_RATE', 100);
const burstIntervalSeconds = integer('CAPACITY_BURST_INTERVAL_SECONDS', 60);

if (apiA === apiB) {
  throw new Error('CAPACITY_API_A and CAPACITY_API_B must identify distinct replicas');
}

const mutation = `mutation($input: CreateNamespaceInput!) {
  createNamespace(input: $input) { namespace { metadata { name } } }
}`;

const admitted = new Counter('gitstore_namespace_admitted');
const graphqlFailures = new Rate('gitstore_namespace_graphql_failed');
const conflicts = new Counter('gitstore_namespace_conflicts');
const visibility = new Trend('gitstore_namespace_visibility_ms', true);
const existenceQuery = `query($name: String!) { namespace(by: {identifier: $name}) { id } }`;

const scenarios = {
  sustained: {
    executor: 'constant-arrival-rate',
    rate,
    timeUnit: '1s',
    duration: __ENV.CAPACITY_DURATION || '10m',
    preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', Math.max(20, rate * 2)),
    maxVUs: integer('CAPACITY_MAX_VUS', Math.max(100, rate * 10)),
  },
};
for (let round = 1; round <= burstRounds; round += 1) {
  scenarios[`burst${round}`] = {
    executor: 'constant-arrival-rate',
    rate: burstRate,
    timeUnit: '1s',
    duration: '1s',
    startTime: `${round * burstIntervalSeconds}s`,
    preAllocatedVUs: integer('CAPACITY_BURST_PREALLOCATED_VUS', Math.max(50, burstRate)),
    maxVUs: integer('CAPACITY_BURST_MAX_VUS', Math.max(200, burstRate * 3)),
  };
}

export const options = {
  scenarios,
  thresholds: {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    gitstore_namespace_graphql_failed: ['rate<0.001'],
    gitstore_namespace_visibility_ms: ['p(95)<=1000', 'p(99)<=3000'],
    http_req_failed: ['rate<0.001'],
    // The feature's 1s/3s objective is watch visibility after mutation
    // acknowledgement and is enforced by the domain verifier. Mutations may
    // drain behind an accepted 100/s burst, but the drain remains bounded.
    'http_req_duration{operation:createNamespace}': ['p(99)<30000'],
  },
};

export function setup() {
  for (const endpoint of [apiA, apiB]) {
    const result = graphql(endpoint, token, 'query { __typename }', {}, { operation: 'preflight' });
    if (result.response.status < 200 || result.response.status >= 300 || result.body.errors) {
      throw new Error(`GraphQL preflight failed for ${endpoint}`);
    }
  }
}

export default function () {
  const sequence = exec.scenario.iterationInTest;
  const scenario = exec.scenario.name.toLowerCase().replace(/[^a-z0-9]/g, '').slice(-12);
  const traffic = scenario.startsWith('burst') ? 'burst' : 'sustained';
  const name = `nwk6-${runID}-${scenario}-${sequence}`;
  const endpoints = sequence % 2 === 0 ? [apiA, apiB] : [apiB, apiA];
  let result;
  let acknowledged = false;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const endpoint = endpoints[attempt % endpoints.length];
    result = graphql(endpoint, token, mutation, {
      input: {
        apiVersion: 'gitstore.dev/v1beta1',
        kind: 'Namespace',
        metadata: { name },
        spec: { title: name, tier: 'USER' },
      },
    }, { operation: 'createNamespace', traffic, replica: endpoint === apiA ? 'a' : 'b' });
    if (result.response.status >= 200 && result.response.status < 300 &&
        (!result.body.errors || result.body.errors.length === 0) &&
        result.body.data && result.body.data.createNamespace &&
        result.body.data.createNamespace.namespace.metadata.name === name) {
      acknowledged = true;
      break;
    }
    const rawErrors = JSON.stringify(result.body.errors || []);
    if (!rawErrors.includes('NAMESPACE_CONFLICT') && !rawErrors.includes('RESOURCE_VERSION_CONFLICT')) {
      break;
    }
    conflicts.add(1);
    for (const confirmEndpoint of endpoints) {
      const confirmation = graphql(confirmEndpoint, token, existenceQuery, { name }, { operation: 'confirmNamespace' });
      if (confirmation.body.data && confirmation.body.data.namespace && confirmation.body.data.namespace.id) {
        acknowledged = true;
        break;
      }
    }
    if (acknowledged) {
      break;
    }
    sleep(0.025 * (attempt + 1));
  }

  check({ result, acknowledged }, {
    'createNamespace returned HTTP 2xx': ({ result: finalResult }) => finalResult.response.status >= 200 && finalResult.response.status < 300,
    'createNamespace reached an acknowledged state': ({ acknowledged: finalAcknowledged }) => finalAcknowledged,
  });
  graphqlFailures.add(!acknowledged);
  if (acknowledged) {
    admitted.add(1);
    const visibilityStarted = Date.now();
    let visibleEverywhere = false;
    while (Date.now() - visibilityStarted < 30000) {
      visibleEverywhere = true;
      for (const endpoint of [apiA, apiB]) {
        const confirmation = graphql(endpoint, token, existenceQuery, { name }, { operation: 'confirmVisibility' });
        if (!confirmation.body.data || !confirmation.body.data.namespace || !confirmation.body.data.namespace.id) {
          visibleEverywhere = false;
          break;
        }
      }
      if (visibleEverywhere) {
        break;
      }
      sleep(0.01);
    }
    const visibilityMillis = Date.now() - visibilityStarted;
    visibility.add(visibilityMillis);
    check({ visibleEverywhere }, {
      'namespace became visible through every replica': ({ visibleEverywhere: visible }) => visible,
    });
  }
}

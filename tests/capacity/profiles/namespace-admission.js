// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter, Gauge, Rate, Trend } from 'k6/metrics';
import { env, integer, durationNanoseconds, announceLoadWindow } from '../lib/config.js';
import { graphql, graphqlSucceeded, graphqlErrorCode } from '../lib/graphql.js';

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
  createNamespace(input: $input) { namespace { id metadata { name } } }
}`;

const admitted = new Counter('gitstore_namespace_admitted');
const offered = new Counter('gitstore_namespace_offered');
const scheduled = new Counter('gitstore_namespace_scheduled');
const missed = new Counter('gitstore_namespace_missed_schedules');
const endpointCallbacks = new Counter('gitstore_namespace_endpoint_callbacks');
const declaredBurstRounds = new Gauge('gitstore_namespace_burst_rounds');
const graphqlFailures = new Rate('gitstore_namespace_graphql_failed');
const conflicts = new Counter('gitstore_namespace_conflicts');
const visibility = new Trend('gitstore_namespace_visibility_ms', true);
const existenceQuery = `query($name: String!) { namespace(by: {name: $name}) { id } }`;

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

const scheduleThresholds = {};
for (const name of Object.keys(scenarios)) {
  scheduleThresholds[`gitstore_namespace_offered{schedule:${name}}`] = ['count>0'];
  scheduleThresholds[`gitstore_namespace_scheduled{schedule:${name}}`] = ['count>0'];
  scheduleThresholds[`gitstore_namespace_missed_schedules{schedule:${name}}`] = ['count==0'];
  scheduleThresholds[`gitstore_namespace_endpoint_callbacks{schedule:${name}}`] = ['count<=1'];
}

export const options = {
  scenarios,
  thresholds: {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    gitstore_namespace_missed_schedules: ['count==0'],
    ...scheduleThresholds,
    gitstore_namespace_graphql_failed: ['rate<0.001'],
    gitstore_namespace_visibility_ms: ['p(95)<=1000', 'p(99)<=3000'],
    http_req_failed: ['rate<0.001'],
    // The feature's 1s/3s objective is watch visibility after mutation
    // acknowledgement and is enforced by the domain verifier. Mutations may
    // drain behind an accepted 100/s burst, but the drain remains bounded.
    'http_req_duration{operation:createNamespace}': ['p(99)<30000'],
  },
};

function scenarioSchedule(config) {
  if (!config || config.executor !== 'constant-arrival-rate' || config.timeUnit !== '1s' ||
      !Number.isSafeInteger(config.rate) || config.rate <= 0 || typeof config.duration !== 'string') {
    throw new Error('Namespace scenario has an invalid effective native schedule');
  }
  const duration = durationNanoseconds(config.duration);
  const count = (duration * BigInt(config.rate) + BigInt('999999999')) / BigInt('1000000000');
  if (count > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new Error('Namespace schedule exceeds exact offer-counter precision');
  }
  return { rate: config.rate, offers: Number(count), durationNs: duration.toString() };
}

export function setup() {
  const effective = exec.test.options.scenarios;
  if (Object.keys(effective).sort().join(',') !== Object.keys(scenarios).sort().join(',')) {
    throw new Error('Namespace effective scenarios differ from the declared schedule');
  }
  const schedules = {};
  let total = 0;
  declaredBurstRounds.add(burstRounds);
  for (const name of Object.keys(scenarios)) {
    const schedule = scenarioSchedule(effective[name]);
    if (schedule.rate !== scenarios[name].rate ||
        (name !== 'sustained' && schedule.durationNs !== '1000000000')) {
      throw new Error('Namespace effective burst or arrival rate differs from the declared schedule');
    }
    total += schedule.offers;
    if (!Number.isSafeInteger(total)) {
      throw new Error('Namespace combined schedule exceeds exact offer-counter precision');
    }
    schedules[name] = schedule;
    const tags = { schedule: name };
    scheduled.add(schedule.offers, tags);
    offered.add(0, tags);
    missed.add(0, tags);
    endpointCallbacks.add(0, tags);
  }
  for (const endpoint of [apiA, apiB]) {
    const result = graphql(endpoint, token, 'query { __typename }', {}, { operation: 'preflight' });
    if (!graphqlSucceeded(result) || typeof result.body.data.__typename !== 'string' ||
        result.body.data.__typename.length === 0) {
      throw new Error(`GraphQL preflight failed for ${endpoint}`);
    }
  }
  return { schedules };
}

function namespaceConfirmed(result, expectedID) {
  return graphqlSucceeded(result) && result.body.data.namespace &&
    typeof result.body.data.namespace.id === 'string' && result.body.data.namespace.id.length > 0 &&
    (expectedID === undefined || result.body.data.namespace.id === expectedID);
}

export default function (data) {
  const sequence = exec.scenario.iterationInTest;
  const progress = exec.scenario.progress;
  const schedule = data && data.schedules && data.schedules[exec.scenario.name];
  const tags = { schedule: exec.scenario.name };
  // k6's stop timer can race one callback at the exclusive load endpoint.
  if (schedule && sequence === schedule.offers && progress === 1) {
    endpointCallbacks.add(1, tags);
    return;
  }
  offered.add(1, tags);
  const start = exec.scenario.startTime;
  if (!schedule || !Number.isSafeInteger(sequence) || sequence < 0 || sequence >= schedule.offers ||
      !Number.isSafeInteger(start) || start <= 0 ||
      !Number.isFinite(progress) || progress < 0 || progress > 1) {
    throw new Error('Namespace offer has an invalid declared schedule identity');
  }
  if (progress === 1) {
    missed.add(1, tags);
    throw new Error('Namespace offer arrived after its declared load window');
  }
  const intervalMs = 1000 / schedule.rate;
  const elapsedMs = Number(schedule.durationNs) / 1000000 * progress;
  if (elapsedMs - sequence * intervalMs >= intervalMs) {
    missed.add(1, tags);
  }
  if (sequence === 0 && exec.scenario.name === 'sustained') {
    announceLoadWindow(start, schedule.durationNs);
  }
  const scenario = exec.scenario.name.toLowerCase().replace(/[^a-z0-9]/g, '').slice(-12);
  const traffic = scenario.startsWith('burst') ? 'burst' : 'sustained';
  const name = `nwk6-${runID}-${scenario}-${sequence}`;
  const endpoints = sequence % 2 === 0 ? [apiA, apiB] : [apiB, apiA];
  let result;
  let acknowledged = false;
  let acknowledgedID;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const endpoint = endpoints[attempt % endpoints.length];
    result = graphql(endpoint, token, mutation, {
      input: {
        apiVersion: 'gitstore.dev/v1beta1',
        kind: 'Namespace',
        metadata: { name },
        spec: { title: name, tier: 'ORGANIZATION' },
      },
    }, { operation: 'createNamespace', traffic, replica: endpoint === apiA ? 'a' : 'b' });
    if (graphqlSucceeded(result) && result.body.data.createNamespace &&
        result.body.data.createNamespace.namespace && result.body.data.createNamespace.namespace.metadata &&
        typeof result.body.data.createNamespace.namespace.id === 'string' &&
        result.body.data.createNamespace.namespace.id.length > 0 &&
        result.body.data.createNamespace.namespace.metadata.name === name) {
      acknowledged = true;
      acknowledgedID = result.body.data.createNamespace.namespace.id;
      break;
    }
    if (graphqlErrorCode(result) !== 'CONFLICT') {
      break;
    }
    conflicts.add(1);
    for (const confirmEndpoint of endpoints) {
      const confirmation = graphql(confirmEndpoint, token, existenceQuery, { name }, { operation: 'confirmNamespace' });
      if (namespaceConfirmed(confirmation)) {
        acknowledged = true;
        acknowledgedID = confirmation.body.data.namespace.id;
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
        if (!namespaceConfirmed(confirmation, acknowledgedID)) {
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

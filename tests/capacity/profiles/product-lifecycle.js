// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Feature 055's capacity entry point. The full verifier is deliberately built
// incrementally with the Product admission, durable-watch, and controller
// slices; keeping the profile executable from the outset prevents an
// unregistered scenario from becoming production-only work.
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { env } from '../lib/config.js';
import { graphql } from '../lib/graphql.js';

// The local alpha stack is validated from the host on 127.0.0.1, whereas k6
// runs in a Docker container on Docker Desktop.  Permit that runner to use
// Docker's stable host gateway without weakening the independently validated
// replica endpoints recorded in the gate evidence.
const apiA = env('PRODUCT_CAPACITY_API_A', env('CAPACITY_API_A'));
const apiB = env('PRODUCT_CAPACITY_API_B', env('CAPACITY_API_B'));
const token = env('CAPACITY_TOKEN');
const replicaChecks = new Counter('product_lifecycle_replica_checks');

export const options = {
  scenarios: {
    preflight: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '30s',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    http_req_failed: ['rate<0.001'],
  },
};

export default function () {
  for (const endpoint of [apiA, apiB]) {
    const result = graphql(endpoint, token, 'query { __typename }', {}, {
      operation: 'productLifecyclePreflight',
    });
    check(result, {
      'Product lifecycle API replica is reachable': ({ response, body }) =>
        response.status >= 200 && response.status < 300 && !body.errors,
    });
    replicaChecks.add(1);
  }
}

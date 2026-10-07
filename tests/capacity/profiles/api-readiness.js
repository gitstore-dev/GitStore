// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import { check } from 'k6';
import { env, integer } from '../lib/config.js';

const baseURL = env('CAPACITY_BASE_URL');
const rate = integer('CAPACITY_RATE', 10);

export const options = {
  scenarios: {
    readiness: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration: __ENV.CAPACITY_DURATION || '1m',
      preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', 10),
      maxVUs: integer('CAPACITY_MAX_VUS', 100),
    },
  },
  thresholds: {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(95)<1000', 'p(99)<3000'],
  },
};

export default function () {
  const health = http.get(`${baseURL}/health`, { tags: { operation: 'health' } });
  const ready = http.get(`${baseURL}/ready`, { tags: { operation: 'ready' } });
  check(health, { 'health is 2xx': (response) => response.status >= 200 && response.status < 300 });
  check(ready, { 'ready is 2xx': (response) => response.status >= 200 && response.status < 300 });
}

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import { check } from 'k6';
import execution from 'k6/execution';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { env, integer } from '../lib/config.js';

const baseURL = env('CAPACITY_BASE_URL');
const rate = integer('CAPACITY_RATE', 10);
const identityReadiness = __ENV.CAPACITY_IDENTITY_READINESS === '1';
const endpoints = identityReadiness ? env('CAPACITY_API_ENDPOINTS').split(',') : [baseURL];
const identityCases = identityReadiness ? new SharedArray('identity cases', () => {
  const fixture = JSON.parse(open(env('CAPACITY_IDENTITY_FIXTURE_FILE')));
  if (!Array.isArray(fixture.cases) || fixture.cases.length < 3) {
    throw new Error('identity readiness requires seeded principal cases');
  }
  return fixture.cases;
}) : [];
const identityRequests = new Counter('identity_requests');
const identityDecisions = new Counter('identity_decisions');
const identityGroups = new Counter('identity_groups');
const scenarios = {};
if (identityReadiness) {
  for (let index = 0; index < endpoints.length; index++) {
    scenarios[`identity_${index}`] = {
      executor: 'constant-arrival-rate',
      rate: integer('CAPACITY_RATE', 500),
      timeUnit: '1s',
      duration: __ENV.CAPACITY_DURATION || '30m',
      preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', 50),
      maxVUs: integer('CAPACITY_MAX_VUS', 100),
    };
  }
} else {
  scenarios.readiness = {
    executor: 'constant-arrival-rate',
    rate,
    timeUnit: '1s',
    duration: __ENV.CAPACITY_DURATION || '1m',
    preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', 10),
    maxVUs: integer('CAPACITY_MAX_VUS', 100),
  };
}

export const options = {
  scenarios,
  thresholds: {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(95)<1000', 'p(99)<3000'],
    ...(identityReadiness ? {
      identity_requests: ['count>0'],
      identity_decisions: ['count>0'],
      identity_groups: ['count>0'],
    } : {}),
  },
};

if (identityReadiness) {
  for (let index = 0; index < endpoints.length; index++) {
    options.thresholds[`identity_requests{replica:${index}}`] = ['count>0'];
    options.thresholds[`identity_decisions{replica:${index}}`] = ['count>0'];
    options.thresholds[`identity_groups{replica:${index}}`] = ['count>0'];
  }
}

function actorResponse(response) {
  if (response.status !== 200) return null;
  try {
    const body = response.json();
    if (body.errors && body.errors.length) return null;
    return body.data && body.data.node;
  } catch (_) {
    return null;
  }
}

function sameGroups(actor, expected) {
  if (!actor || !Array.isArray(actor.groups)) return false;
  const actual = [...actor.groups].sort();
  const wanted = [...expected].sort();
  return JSON.stringify(actual) === JSON.stringify(wanted);
}

function identityRequest() {
  const index = Number(execution.scenario.name.split('_')[1]);
  const sample = identityCases[execution.scenario.iterationInTest % identityCases.length];
  const tags = { replica: `${index}`, principal_kind: sample.kind, operation: 'identity' };
  const query = `{node(id:${JSON.stringify(sample.actorID)}){id __typename ... on User{groups} ... on ServiceAccount{groups}}}`;
  const request = (token) => http.post(`${endpoints[index]}/admin/graphql`, JSON.stringify({ query }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    tags,
    timeout: '5s',
  });
  const response = request(sample.token);
  identityRequests.add(1, tags);
  const actor = actorResponse(response);
  let decisionMatches = false;
  if (sample.allowed) {
    decisionMatches = actor && actor.id === sample.actorID && actor.__typename === sample.kind;
  } else if (response.status === 200) {
    try {
      const errors = response.json().errors;
      decisionMatches = Array.isArray(errors) && errors.length > 0 &&
        errors.every((error) => error.extensions && error.extensions.code === 'FORBIDDEN');
    } catch (_) { /* The correctness check below rejects malformed responses. */ }
  }
  const groupActor = sample.allowed ? actor : actorResponse(request(env('CAPACITY_TOKEN')));
  const groupsMatch = groupActor && groupActor.id === sample.actorID &&
    groupActor.__typename === sample.kind && sameGroups(groupActor, sample.groups);
  check(response, { 'identity decision matches seeded grant': () => !!decisionMatches });
  check(groupActor, { 'identity groups match seeded membership': () => !!groupsMatch });
  if (decisionMatches) identityDecisions.add(1, tags);
  if (groupsMatch) identityGroups.add(1, tags);
}

export default function () {
  if (identityReadiness) {
    identityRequest();
    return;
  }
  const health = http.get(`${baseURL}/health`, { tags: { operation: 'health' } });
  const ready = http.get(`${baseURL}/ready`, { tags: { operation: 'ready' } });
  check(health, { 'health is 2xx': (response) => response.status >= 200 && response.status < 300 });
  check(ready, { 'ready is 2xx': (response) => response.status >= 200 && response.status < 300 });
}

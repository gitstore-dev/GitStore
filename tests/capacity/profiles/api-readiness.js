// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import { check } from 'k6';
import execution from 'k6/execution';
import crypto from 'k6/crypto';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { env, integer } from '../lib/config.js';

const baseURL = env('CAPACITY_BASE_URL');
const rate = integer('CAPACITY_RATE', 10);
const identityReadiness = __ENV.CAPACITY_IDENTITY_READINESS === '1';
const identityChaos = __ENV.CAPACITY_IDENTITY_CHAOS === '1';
const rbacWorkload = __ENV.CAPACITY_RBAC_WORKLOAD === '1';
if (rbacWorkload && (identityReadiness || identityChaos)) {
  throw new Error('RBAC and identity workloads must be selected independently');
}
const endpoints = identityReadiness || rbacWorkload ? env('CAPACITY_API_ENDPOINTS').split(',') : [baseURL];
if (rbacWorkload && (endpoints.length !== 2 ||
    !endpoints.every((endpoint) => /^https?:\/\/[^\s,]+$/.test(endpoint)) ||
    new Set(endpoints.map((endpoint) => endpoint.replace(/\/+$/, ''))).size !== 2)) {
  throw new Error('RBAC requires exactly two distinct directly addressed API replicas');
}
const rbacCases = rbacWorkload ? new SharedArray('RBAC cases', () => {
  const raw = open(env('CAPACITY_RBAC_FIXTURE_FILE'));
  if (raw.length > 16 * 1024 * 1024) throw new Error('RBAC fixture exceeds its bounded working set');
  const fixture = JSON.parse(raw);
  if (typeof fixture.owner !== 'string' || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(fixture.owner) ||
      !Array.isArray(fixture.cases) || fixture.cases.length < 10 ||
      fixture.cases.length > 20000 || fixture.cases.length % 10 !== 0) {
    throw new Error('RBAC requires an owned fixture with complete ten-case 60/30/10 groups');
  }
  for (let offset = 0; offset < fixture.cases.length; offset += 10) {
    const counts = { allow: 0, deny: 0, isolation: 0 };
    for (const sample of fixture.cases.slice(offset, offset + 10)) {
      if (!Object.hasOwn(counts, sample.expected) ||
          !['product.list', 'role.list'].includes(sample.action) ||
          typeof sample.namespace !== 'string' || sample.namespace.length === 0 || sample.namespace.length > 253 ||
          typeof sample.subject !== 'string' || sample.subject.length === 0 || sample.subject.length > 256 ||
          typeof sample.token !== 'string' || sample.token.length === 0 || sample.token.length > 16384) {
        throw new Error('RBAC case lacks a bounded ordinary principal, action or Namespace');
      }
      counts[sample.expected]++;
    }
    if (counts.allow !== 6 || counts.deny !== 3 || counts.isolation !== 1) {
      throw new Error('RBAC cases must preserve the independently authored 60/30/10 mix');
    }
  }
  return fixture.cases.map((sample) => ({ ...sample, owner: fixture.owner }));
}) : [];
const rbacRequests = new Counter('rbac_requests');
const rbacCompleted = new Counter('rbac_completed');
const rbacOutcomes = {
  allow: new Counter('rbac_allowed'), deny: new Counter('rbac_denied'),
  isolation: new Counter('rbac_isolation'), unavailable: new Counter('rbac_unavailable'),
  wrong: new Counter('rbac_wrong'),
};
const rbacFailureReceipts = new Counter('rbac_failure_receipts');
const rbacFailureReceiptsSuppressed = new Counter('rbac_failure_receipts_suppressed');
let rbacFailureReceiptCount = 0;
const rbacFailureReceiptLimit = 16; // k6 module state is isolated per VU.
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
const identityUnavailable = new Counter('identity_unavailable');
const faultWindows = identityChaos ? new SharedArray('identity fault windows', () =>
  JSON.parse(open(env('CAPACITY_IDENTITY_FIXTURE_FILE'))).faultWindows) : [];
const scenarios = {};
if (identityReadiness || rbacWorkload) {
  for (let index = 0; index < endpoints.length; index++) {
    scenarios[`${rbacWorkload ? 'rbac' : 'identity'}_${index}`] = {
      executor: 'constant-arrival-rate',
      rate: integer('CAPACITY_RATE', 500),
      timeUnit: '1s',
      duration: __ENV.CAPACITY_DURATION || '30m',
      preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', 100),
      maxVUs: integer('CAPACITY_MAX_VUS', 200),
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
    ...(identityChaos ? {} : { http_req_failed: ['rate<0.001'] }),
    http_req_duration: ['p(95)<1000', 'p(99)<3000'],
    ...(identityReadiness ? {
      identity_requests: ['count>0'],
      identity_decisions: ['count>0'],
      identity_groups: ['count>0'],
    } : {}),
    ...(rbacWorkload ? {
      rbac_requests: ['count>0'], rbac_completed: ['count>0'],
      rbac_allowed: ['count>0'], rbac_denied: ['count>0'], rbac_isolation: ['count>0'],
      rbac_wrong: ['count==0'], rbac_unavailable: ['count==0'],
    } : {}),
  },
};

if (identityReadiness) {
  for (let index = 0; index < endpoints.length; index++) {
    options.thresholds[`identity_requests{replica:${index}}`] = ['count>0'];
    options.thresholds[`identity_decisions{replica:${index}}`] = ['count>0'];
    options.thresholds[`identity_groups{replica:${index}}`] = ['count>0'];
    if (identityChaos) {
      options.thresholds[`identity_unavailable{replica:${index}}`] = ['count>=0'];
    }
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

function expectedUnavailability(response, replica) {
  const now = Date.now();
  if (!identityChaos || !faultWindows.some((window) =>
    window.replicas.includes(replica) && now >= window.startMillis && now <= window.endMillis)) return false;
  if ([0, 401, 502, 503, 504].includes(response.status)) return true;
  if (response.status !== 200) return false;
  try {
    const errors = response.json().errors;
    return Array.isArray(errors) && errors.length > 0 &&
      errors.every((error) => error.extensions && error.extensions.code === 'UNAUTHENTICATED');
  } catch (_) {
    return false;
  }
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
  if (identityChaos) identityUnavailable.add(0, tags);
  if (expectedUnavailability(response, index)) {
    identityUnavailable.add(1, tags);
    return;
  }
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
  const groupResponse = sample.allowed ? response : request(env('CAPACITY_TOKEN'));
  if (expectedUnavailability(groupResponse, index)) {
    identityUnavailable.add(1, tags);
    return;
  }
  const groupActor = sample.allowed ? actor : actorResponse(groupResponse);
  const groupsMatch = groupActor && groupActor.id === sample.actorID &&
    groupActor.__typename === sample.kind && sameGroups(groupActor, sample.groups);
  check(response, { 'identity decision matches seeded grant': () => !!decisionMatches });
  check(groupActor, { 'identity groups match seeded membership': () => !!groupsMatch });
  if (decisionMatches) identityDecisions.add(1, tags);
  if (groupsMatch) identityGroups.add(1, tags);
}

let rbacOffered = 0;
if (rbacWorkload) {
  const duration = /^([1-9][0-9]*)(s|m|h)$/.exec(__ENV.CAPACITY_DURATION || '30m');
  if (!duration) throw new Error('RBAC duration requires bounded seconds, minutes or hours');
  const seconds = Number(duration[1]) * { s: 1, m: 60, h: 3600 }[duration[2]];
  const offered = seconds * integer('CAPACITY_RATE', 500);
  if (!Number.isSafeInteger(offered) || offered <= 0) throw new Error('RBAC offered work exceeds its accounting bound');
  rbacOffered = offered;
  for (let index = 0; index < endpoints.length; index++) {
    options.thresholds[`rbac_requests{replica:${index}}`] = [`count==${offered}`];
    options.thresholds[`rbac_completed{replica:${index}}`] = [`count==${offered}`];
  }
}

function rbacResponse(response, field) {
  if (response.status !== 200) return 'unavailable';
  if (typeof response.body !== 'string' || response.body.length > 65536) return 'wrong';
  try {
    const body = response.json();
    if (body.errors !== undefined && body.errors !== null && !Array.isArray(body.errors)) return 'wrong';
    if (body.errors && body.errors.length) {
      const code = body.errors[0].extensions && body.errors[0].extensions.code;
      if (!body.errors.every((error) => error.extensions && error.extensions.code === code)) return 'wrong';
      if (code === 'AUTHORIZATION_UNAVAILABLE' || code === 'DIRECTORY_UNAVAILABLE' ||
          code === 'UNAUTHENTICATED') return 'unavailable';
      if (code !== 'FORBIDDEN' || (body.data && body.data[field] != null)) return 'wrong';
      return 'deny';
    }
    const data = body.data && body.data[field];
    if (!data || !Array.isArray(data.edges) || data.edges.length > 1 ||
        !data.edges.every((edge) => edge && edge.node && typeof edge.node.id === 'string' && edge.node.id.length > 0)) {
      return 'wrong';
    }
    return 'allow';
  } catch (_) {
    return 'wrong';
  }
}

function rbacFailureDetails(response) {
  const knownCodes = new Set([
    'AUTHORIZATION_UNAVAILABLE', 'DIRECTORY_UNAVAILABLE', 'UNAUTHENTICATED', 'FORBIDDEN', 'INTERNAL_SERVER_ERROR',
    'GRAPHQL_VALIDATION_FAILED', 'BAD_USER_INPUT', 'RATE_LIMITED', 'RESOURCE_EXHAUSTED',
    'QUOTA_EXCEEDED', 'TOO_MANY_REQUESTS', 'THROTTLED', 'SERVICE_UNAVAILABLE',
  ]);
  const quotaCodes = new Set(['RATE_LIMITED', 'RESOURCE_EXHAUSTED', 'QUOTA_EXCEEDED', 'TOO_MANY_REQUESTS', 'THROTTLED']);
  let body;
  let bodyState = 'bounded';
  let digest = null;
  if (typeof response.body !== 'string') {
    bodyState = 'missing';
  } else if (response.body.length > 65536) {
    bodyState = 'oversized';
  } else {
    digest = crypto.sha256(response.body, 'hex');
    try { body = response.json(); } catch (_) { bodyState = 'invalid_json'; }
  }
  const errors = body && Array.isArray(body.errors) ? body.errors : [];
  const codes = errors.slice(0, 8).map((error) => {
    const code = error && error.extensions && error.extensions.code;
    return knownCodes.has(code) ? code : code === undefined ? 'MISSING' : 'UNKNOWN';
  });
  let reason;
  if (response.status === 0) reason = 'transport';
  else if (response.status === 429) reason = 'http_throttled';
  else if (errors.some((error) => error && error.extensions && quotaCodes.has(error.extensions.code))) {
    reason = response.status === 200 ? 'graphql_quota' : 'http_quota';
  }
  else if (response.status !== 200) reason = 'http_status';
  else if (bodyState !== 'bounded') reason = bodyState;
  else if (errors.length && errors.every((error) => error && error.extensions &&
    error.extensions.code === 'AUTHORIZATION_UNAVAILABLE')) reason = 'graphql_authorization_unavailable';
  else if (errors.length && errors.every((error) => error && error.extensions &&
    error.extensions.code === 'DIRECTORY_UNAVAILABLE')) reason = 'graphql_directory_unavailable';
  else if (errors.length && errors.every((error) => error && error.extensions &&
    error.extensions.code === 'UNAUTHENTICATED')) reason = 'graphql_unauthenticated';
  else if (errors.length) reason = 'graphql_error';
  else reason = 'result_or_verdict_mismatch';
  return { reason, errorCodes: codes, errorCodesTruncated: errors.length > 8, bodyState, responseDigest: digest };
}

function recordRBACFailure(response, sample, replica, correlationID, observed, classification, tags) {
  if (rbacFailureReceiptCount >= rbacFailureReceiptLimit) {
    rbacFailureReceiptsSuppressed.add(1, tags);
    return;
  }
  rbacFailureReceiptCount++;
  const label = (value, limit) => typeof value === 'string' && value.length <= limit &&
    /^[a-zA-Z0-9_.:-]+$/.test(value) && !value.includes(sample.token) ? value : '<redacted>';
  const receipt = {
    type: 'rbac_failed_response', correlationID: label(correlationID, 160),
    replica, action: sample.action, namespace: label(sample.namespace, 253),
    observedAt: new Date().toISOString(), httpStatus: response.status,
    classification, observed, expected: sample.expected,
    transportErrorCode: Number.isInteger(response.error_code) && response.error_code >= 0 &&
      response.error_code <= 65535 ? response.error_code : null,
    ...rbacFailureDetails(response),
  };
  console.error(JSON.stringify(receipt));
  rbacFailureReceipts.add(1, tags);
}

function rbacRequest() {
  const replica = Number(execution.scenario.name.split('_')[1]);
  const iteration = execution.scenario.iterationInTest;
  // k6 can schedule an extra boundary iteration after the declared offers.
  if (iteration >= rbacOffered) return;
  const sample = rbacCases[iteration % rbacCases.length];
  const field = sample.action === 'role.list' ? 'roles' : 'products';
  const tags = { replica: `${replica}`, expected: sample.expected, operation: sample.action };
  const correlationID = `${sample.owner}-ordinary-${replica}-${iteration}`;
  const response = http.post(`${endpoints[replica].replace(/\/+$/, '')}/admin/graphql`, JSON.stringify({
    query: `query($namespace:String!){${field}(namespace:$namespace,first:1){edges{node{id}}}}`,
    variables: { namespace: sample.namespace },
  }), {
    headers: {
      'Content-Type': 'application/json', Authorization: `Bearer ${sample.token}`,
      'X-Request-ID': correlationID,
    },
    tags, timeout: '5s', redirects: 0,
  });
  rbacRequests.add(1, tags);
  rbacCompleted.add(1, tags);
  for (const metric of Object.values(rbacOutcomes)) metric.add(0, tags);
  const observed = rbacResponse(response, field);
  const matches = observed === (sample.expected === 'isolation' ? 'deny' : sample.expected);
  const outcome = matches ? sample.expected : observed === 'unavailable' ? 'unavailable' : 'wrong';
  rbacOutcomes[outcome].add(1, tags);
  if (outcome === 'unavailable' || outcome === 'wrong') {
    recordRBACFailure(response, sample, replica, correlationID, observed, outcome, tags);
  }
  check(response, { 'ordinary RBAC decision matches independently authored case': () => matches });
}

export default function () {
  if (rbacWorkload) {
    rbacRequest();
    return;
  }
  if (identityReadiness) {
    identityRequest();
    return;
  }
  const health = http.get(`${baseURL}/health`, { tags: { operation: 'health' } });
  const ready = http.get(`${baseURL}/ready`, { tags: { operation: 'ready' } });
  check(health, { 'health is 2xx': (response) => response.status >= 200 && response.status < 300 });
  check(ready, { 'ready is 2xx': (response) => response.status >= 200 && response.status < 300 });
}

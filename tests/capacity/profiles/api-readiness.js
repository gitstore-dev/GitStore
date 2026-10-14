// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { env, integer } from '../lib/config.js';
import { graphql } from '../lib/graphql.js';

const baseURL = env('CAPACITY_BASE_URL');
const endpoints = (__ENV.CAPACITY_API_ENDPOINTS || baseURL).split(',').map((url) => url.trim().replace(/\/$/, ''));
const rate = integer('CAPACITY_RATE', 10);
const mode = __ENV.CAPACITY_MODE || __ENV.MODE || 'diagnostic';
const duration = __ENV.CAPACITY_DURATION || (mode === 'diagnostic' ? '1m' : '10m');
const token = __ENV.CAPACITY_TOKEN || '';
const enabled = token !== '';
const denialCode = __ENV.CAPACITY_DENIAL_CODE || 'FORBIDDEN';
const deniedToken = __ENV.CAPACITY_DENIED_TOKEN || '';
const query = 'query { namespaces(first: 1) { edges { node { id } } } }';
const preAllocatedVUs = integer('CAPACITY_PREALLOCATED_VUS', 10);
const maxVUs = integer('CAPACITY_MAX_VUS', 100);
if (endpoints.some((url) => !/^https?:\/\/[^@\s,?#]+$/.test(url)) ||
    new Set(endpoints).size !== endpoints.length) {
  throw new Error('CAPACITY_API_ENDPOINTS must contain distinct base URLs without credentials or query strings');
}
if (!['FORBIDDEN', 'UNAUTHENTICATED'].includes(denialCode)) {
  throw new Error('CAPACITY_DENIAL_CODE must specify an exact authorization error class');
}
if (mode !== 'diagnostic' && (!enabled || endpoints.length < 2 ||
    rate !== 10 || preAllocatedVUs !== 10 || maxVUs !== 100)) {
  throw new Error('readiness gates require authenticated probes, two replicas and the declared 10/10/100 envelope');
}
const mismatches = new Counter('readiness_authz_mismatches');
const counters = endpoints.map((_, i) => ({
  attempts: new Counter(`readiness_authz_attempts_${i}`),
  allowed: new Counter(`readiness_authz_allowed_${i}`),
  denied: new Counter(`readiness_authz_denied_${i}`),
}));
const thresholds = {
  checks: ['rate==1'],
  dropped_iterations: ['count==0'],
  'http_req_failed{traffic:readiness}': ['rate<0.001'],
  'http_req_duration{traffic:readiness}': ['p(95)<1000', 'p(99)<3000'],
};
if (enabled) {
  thresholds.readiness_authz_mismatches = ['count==0'];
  counters.forEach((_, i) => {
    thresholds[`readiness_authz_allowed_${i}`] = ['count>0'];
    thresholds[`readiness_authz_denied_${i}`] = ['count>0'];
    thresholds[`http_req_failed{replica:${i},traffic:readiness}`] = ['rate<0.001'];
    thresholds[`http_req_duration{replica:${i},traffic:readiness}`] = ['p(95)<1000', 'p(99)<3000'];
  });
}

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(95)', 'p(99)'],
  scenarios: Object.fromEntries(endpoints.map((_, i) => [
    `readiness_${i}`, {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs,
      maxVUs,
    },
  ])),
  thresholds,
};

function authSnapshot(body) {
  const auth = body && body.auth;
  const policy = auth && auth.policy;
  const digest = /^[0-9a-f]{64}$/;
  if (!auth || !policy || policy.loaded !== true ||
      !digest.test(auth.catalogDigest || '') || !digest.test(policy.digest || '') ||
      !Array.isArray(auth.authn) || auth.authn.length === 0 || auth.authn.length > 5 ||
      new Set(auth.authn).size !== auth.authn.length ||
      !auth.authn.every((name) => ['static-users', 'anonymous', 'serviceaccount-assertion', 'serviceaccount-jwt', 'oidc-jwt'].includes(name)) ||
      typeof auth.authz !== 'string' || !auth.authz || auth.authz.length > 64 ||
      typeof auth.userdir !== 'string' || !auth.userdir || auth.userdir.length > 64 ||
      typeof policy.apiVersion !== 'string' || !policy.apiVersion || policy.apiVersion.length > 128 ||
      ![policy.roles, policy.bindings, policy.rules].every((value) => Number.isInteger(value) && value >= 0) ||
      policy.roles > 200 || policy.bindings > 1000 || policy.rules > 2000) {
    return null;
  }
  return {
    authn: auth.authn, authz: auth.authz, userdir: auth.userdir, catalogDigest: auth.catalogDigest,
    policy: { loaded: true, apiVersion: policy.apiVersion, digest: policy.digest,
      roles: policy.roles, bindings: policy.bindings, rules: policy.rules },
  };
}

function json(response) {
  try {
    return response.json();
  } catch (_) {
    return null;
  }
}

function snapshotIdentity(auth) {
  const policy = auth.policy;
  return JSON.stringify([auth.authn, auth.authz, auth.userdir, auth.catalogDigest,
    policy.loaded, policy.apiVersion, policy.digest, policy.roles, policy.bindings, policy.rules]);
}

export function setup() {
  if (!enabled) return { replicas: [] };
  const replicas = endpoints.map((endpoint) => {
    const response = http.get(`${endpoint}/ready`, { timeout: '5s', tags: { traffic: 'readiness', operation: 'ready_setup' } });
    const auth = authSnapshot(json(response));
    if (response.status !== 200 || !auth) {
      throw new Error('readiness authorization probes require a ready API and complete non-secret snapshot metadata');
    }
    return { endpoint, auth };
  });
  if (replicas.some((replica) => snapshotIdentity(replica.auth) !== snapshotIdentity(replicas[0].auth))) {
    throw new Error('readiness replicas must publish identical provider, policy and compiled catalog identities');
  }
  return { replicas };
}

export default function (data) {
  const index = Number(exec.scenario.name.replace('readiness_', ''));
  const endpoint = endpoints[index];
  const tags = { traffic: 'readiness', replica: `${index}` };
  const health = http.get(`${endpoint}/health`, { timeout: '5s', tags: { ...tags, operation: 'health' } });
  const ready = http.get(`${endpoint}/ready`, { timeout: '5s', tags: { ...tags, operation: 'ready' } });
  check(health, { 'health is 2xx': (response) => response.status >= 200 && response.status < 300 });
  check(ready, { 'ready is 2xx': (response) => response.status >= 200 && response.status < 300 });
  if (!enabled) return;
  check(authSnapshot(json(ready)), {
    'active auth snapshot unchanged': (auth) => auth !== null &&
      snapshotIdentity(auth) === snapshotIdentity(data.replicas[index].auth),
  });
  const probeTags = { traffic: 'authorization', replica: `${index}` };
  const allow = graphql(`${endpoint}/graphql`, token, query, {}, { ...probeTags, operation: 'authorization_allow' }, { timeout: '5s' });
  const deny = graphql(`${endpoint}/graphql`, deniedToken, query, {}, { ...probeTags, operation: 'authorization_deny' }, { timeout: '5s' });
  const edges = allow.body && allow.body.data && allow.body.data.namespaces && allow.body.data.namespaces.edges;
  const allowed = allow.response.status === 200 && allow.body &&
    (!allow.body.errors || (Array.isArray(allow.body.errors) && allow.body.errors.length === 0)) &&
    Array.isArray(edges) && edges.length <= 1 &&
    edges.every((edge) => edge && edge.node && typeof edge.node.id === 'string' && edge.node.id.length > 0);
  const denied = deny.response.status === 200 && deny.body && deny.body.data == null &&
    Array.isArray(deny.body.errors) && deny.body.errors.length === 1 &&
    deny.body.errors[0].extensions && deny.body.errors[0].extensions.code === denialCode;
  check(allowed, { 'namespace list allowed with exact response shape': (value) => value === true });
  check(denied, { 'namespace list denied with exact authorization class': (value) => value === true });
  counters[index].attempts.add(1);
  counters[index].allowed.add(allowed ? 1 : 0);
  counters[index].denied.add(denied ? 1 : 0);
  mismatches.add((allowed ? 0 : 1) + (denied ? 0 : 1));
}

export function handleSummary(data) {
  if (!enabled) return { stdout: 'Readiness-only diagnostic; no authorization gate was exercised.\n' };
  if (!data.setup_data || !Array.isArray(data.setup_data.replicas) ||
      data.setup_data.replicas.length !== endpoints.length) {
    throw new Error('Authorization readiness setup did not complete; no semantic evidence can pass');
  }
  const count = (name) => data.metrics[name] && data.metrics[name].values.count;
  const passed = Object.values(data.metrics).every((metric) =>
    Object.values(metric.thresholds || {}).every((threshold) => threshold.ok === true));
  const artifact = {
    schemaVersion: 1, component: 'api-readiness-authorization', runID: __ENV.CAPACITY_RUN_ID,
    mode, passed, rate, duration, preAllocatedVUs, maxVUs,
    measurementSeconds: data.state.testRunDurationMs / 1000,
    deniedIdentity: deniedToken ? 'credential' : 'anonymous', denialCode,
    mismatches: count('readiness_authz_mismatches'), droppedIterations: count('dropped_iterations'),
    replicas: data.setup_data.replicas.map((replica, i) => ({
      ...replica, attempts: count(`readiness_authz_attempts_${i}`),
      allowed: count(`readiness_authz_allowed_${i}`), denied: count(`readiness_authz_denied_${i}`),
      readiness: {
        p95Milliseconds: data.metrics[`http_req_duration{replica:${i},traffic:readiness}`].values['p(95)'],
        p99Milliseconds: data.metrics[`http_req_duration{replica:${i},traffic:readiness}`].values['p(99)'],
        httpFailureRate: data.metrics[`http_req_failed{replica:${i},traffic:readiness}`].values.rate,
      },
    })),
  };
  return {
    [`${__ENV.CAPACITY_SUMMARY_DIR || __ENV.CAPACITY_EVIDENCE_DIR}/authorization-probes.json`]: JSON.stringify(artifact, null, 2),
    stdout: `Authorization readiness probes: ${passed ? 'passed' : 'failed'}; ${endpoints.length} endpoints.\n`,
  };
}

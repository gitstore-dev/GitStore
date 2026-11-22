// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import http from 'k6/http';
import { check, sleep } from 'k6';
import execution from 'k6/execution';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { env, integer } from '../lib/config.js';

const baseURL = env('CAPACITY_BASE_URL');
const identityReadiness = __ENV.CAPACITY_IDENTITY_READINESS === '1';
const identityChaos = __ENV.CAPACITY_IDENTITY_CHAOS === '1';
const translationReadiness = __ENV.CAPACITY_TRANSLATION_READINESS === '1';
const rate = integer('CAPACITY_RATE', translationReadiness ? 20 : 10);
if (translationReadiness && identityReadiness) throw new Error('select one readiness domain');
const endpoints = identityReadiness || translationReadiness ? env('CAPACITY_API_ENDPOINTS').split(',') : [baseURL];
if (translationReadiness && (endpoints.length !== 2 || endpoints[0] === endpoints[1])) {
  throw new Error('Translation readiness requires two distinct API endpoints');
}
const translationCases = translationReadiness ? new SharedArray('translation cases', () => {
  const fixture = JSON.parse(open(env('CAPACITY_TRANSLATION_FIXTURE_FILE')));
  if (fixture.runId !== env('CAPACITY_RUN_ID') || !Array.isArray(fixture.cases) || fixture.cases.length !== 10) {
    throw new Error('Translation readiness requires this run\'s ten verified source cases');
  }
  return fixture.cases;
}) : [];
const translationRequests = new Counter('translation_requests');
const translationCorrect = new Counter('translation_correct');
const translationBoundary = new Counter('translation_boundary_iterations');
const translationPreparationRequests = new Counter('translation_preparation_requests');
const translationPreparationCorrect = new Counter('translation_preparation_correct');
const translationFailures = new Counter('translation_failures');
const translationFailureReasons = ['schedule', 'http', 'malformed', 'graphql', 'missing-source',
  'body', 'spec', 'metadata', 'status', 'source'];
const translationWindow = translationReadiness ?
  /^([1-9][0-9]*)(s|m|h)$/.exec(__ENV.CAPACITY_DURATION || '30m') : null;
if (translationReadiness && !translationWindow) throw new Error('Translation duration requires whole seconds, minutes or hours');
const translationOffers = translationWindow ?
  Number(translationWindow[1]) * { s: 1, m: 60, h: 3600 }[translationWindow[2]] * rate : 0;
const translationBarrier = translationReadiness && __ENV.CAPACITY_TRANSLATION_PUSHES === '1' ?
  JSON.parse(open(env('CAPACITY_TRANSLATION_START_FILE'))) : null;
const preparedTranslation = !!translationBarrier?.warmupStartAt;
const warmupOffers = preparedTranslation ? 30 * rate : 0;
const cooldownOffers = preparedTranslation ? 15 * rate : 0;
if (preparedTranslation && Date.parse(translationBarrier.startAt) -
    Date.parse(translationBarrier.warmupStartAt) !== 30000) {
  throw new Error('Translation preparation requires an exact thirty-second warmup');
}

export function setup() {
  if (!translationBarrier) return;
  if (translationBarrier.runId !== env('CAPACITY_RUN_ID')) throw new Error('stale Translation load barrier');
  const delay = Date.parse(preparedTranslation ? translationBarrier.warmupStartAt : translationBarrier.startAt) - Date.now();
  if (delay <= 0 || delay > 600000) throw new Error('missed or unbounded Translation load barrier');
  // VU initialization follows setup; release each request at its own absolute offer.
  if (delay > 2000) sleep((delay - 2000) / 1000);
}

export function teardown() {
  if (!translationBarrier) return;
  const end = Date.parse(translationBarrier.startAt) + translationOffers / rate * 1000 +
    (preparedTranslation ? 15000 : 0);
  const delay = end - Date.now();
  if (delay > 0) sleep(delay / 1000);
}
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
if (identityReadiness) {
  for (let index = 0; index < endpoints.length; index++) {
    scenarios[`identity_${index}`] = {
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
    duration: preparedTranslation ? `${translationOffers / rate + 45}s` :
      (__ENV.CAPACITY_DURATION || (translationReadiness ? '30m' : '1m')),
    preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', preparedTranslation ? 100 : 10),
    maxVUs: integer('CAPACITY_MAX_VUS', 100),
  };
}

export const options = {
  scenarios,
  summaryTrendStats: ['min', 'avg', 'max', 'p(95)', 'p(99)'],
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
    ...(translationReadiness ? {
      translation_requests: ['count>0'],
      translation_correct: ['count>0'],
      translation_boundary_iterations: ['count<=1'],
      'translation_requests{replica:0}': ['count>0'],
      'translation_requests{replica:1}': ['count>0'],
      'translation_correct{replica:0}': ['count>0'],
      'translation_correct{replica:1}': ['count>0'],
      ...Object.fromEntries(translationFailureReasons.map(reason =>
        [`translation_failures{reason:${reason}}`, ['count==0']])),
    } : {}),
    ...(preparedTranslation ? {
      'http_req_duration{phase:measured}': ['p(95)<1000', 'p(99)<3000'],
      'http_req_failed{phase:measured}': ['rate<0.001'],
      'http_reqs{phase:measured}': [`count==${translationOffers}`],
      'checks{phase:measured}': ['rate==1'],
      'iterations{phase:measured}': [`count==${translationOffers}`],
      'translation_preparation_requests{phase:warmup}': [`count==${warmupOffers}`],
      'translation_preparation_correct{phase:warmup}': [`count==${warmupOffers}`],
      'translation_preparation_requests{phase:cooldown}': [`count==${cooldownOffers}`],
      'translation_preparation_correct{phase:cooldown}': [`count==${cooldownOffers}`],
    } : {}),
  },
};

function canonical(value) {
  if (Array.isArray(value)) return value.map(canonical);
  if (value !== null && typeof value === 'object') {
    const result = {};
    for (const key of Object.keys(value).sort()) result[key] = canonical(value[key]);
    return result;
  }
  return value;
}

function translationRequest() {
  const offeredSequence = execution.scenario.iterationInTest;
  const sequence = offeredSequence - warmupOffers;
  const phase = sequence < 0 ? 'warmup' : sequence < translationOffers ? 'measured' : 'cooldown';
  if (preparedTranslation) execution.vu.metrics.tags.phase = phase;
  translationBoundary.add(0);
  if (sequence >= translationOffers + cooldownOffers) {
    if (preparedTranslation) execution.vu.metrics.tags.phase = 'boundary';
    translationBoundary.add(1);
    return;
  }
  const replica = offeredSequence % endpoints.length;
  const sample = translationCases[Math.floor(offeredSequence / endpoints.length) % translationCases.length];
  const tags = { replica: `${replica}`, operation: 'translation', ...(preparedTranslation ? { phase } : {}) };
  const measured = phase === 'measured';
  const start = preparedTranslation ? Date.parse(translationBarrier.startAt) : 0;
  const measuredStart = translationBarrier ? Date.parse(translationBarrier.startAt) : 0;
  const warmupStart = preparedTranslation ? Date.parse(translationBarrier.warmupStartAt) : measuredStart;
  if (translationBarrier) {
    const delay = warmupStart + offeredSequence / rate * 1000 - Date.now();
    if (delay > 0 && delay <= 2000) sleep(delay / 1000);
  }
  const moment = Date.now();
  const measuredEnd = measuredStart + translationOffers / rate * 1000;
  const correctPhase = !translationBarrier || (phase === 'warmup' ? moment >= warmupStart && moment < start :
    phase === 'measured' ? moment >= measuredStart && moment < measuredEnd :
      moment >= measuredEnd && moment < measuredEnd + 15000);
  const response = http.post(`${endpoints[replica]}/admin/graphql`, JSON.stringify({
    query: sample.query, variables: sample.variables,
  }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${env('CAPACITY_TOKEN')}` },
    tags, timeout: '5s',
  });
  (measured ? translationRequests : translationPreparationRequests).add(1, tags);
  let matches = false;
  let failure = correctPhase ? 'http' : 'schedule';
  if (response.status === 200) {
    try {
      const body = response.json();
      matches = correctPhase && !body.errors?.length && body.data !== undefined &&
        JSON.stringify(canonical(body.data)) === JSON.stringify(canonical(sample.expected));
      if (correctPhase) {
        if (body.errors?.length) failure = 'graphql';
        else if (!body.data?.translation) failure = 'missing-source';
        else if (matches) failure = 'none';
        else {
          failure = ['body', 'spec', 'metadata', 'status'].find(field =>
            JSON.stringify(canonical(body.data.translation[field])) !==
              JSON.stringify(canonical(sample.expected.translation[field]))) || 'source';
        }
      }
    } catch (_) { if (correctPhase) failure = 'malformed'; }
  }
  translationFailures.add(matches ? 0 : 1, { ...tags, reason: failure });
  check(response, { 'Translation source and current status match': () => matches }, tags);
  if (matches) (measured ? translationCorrect : translationPreparationCorrect).add(1, tags);
}

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

export default function () {
  if (translationReadiness) {
    translationRequest();
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

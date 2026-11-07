// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Spec 057 category hierarchy capacity profile.
//
// setup()    builds a deterministic fan-out subtree of CAPACITY_CATEGORY_SUBTREE_SIZE
//            descendants under one root and waits for its first reconciliation.
// scenarios  sustain createCategory/updateCategory (CAPACITY_RATE/s, one namespace)
//            and filtered `categories` pages; midway the subtree root is
//            re-parented under a second root and cascade convergence is measured.
// teardown() waits for the cascade, then walks every category's
//            status.resolved.path and compares the walk with filtered results
//            on both APIs (SC-009), and audits that every mutation in the pool
//            kept its own commit revision and content (SC-007).
//
// k6 cannot push to Git. The concurrent push load on the same gitstore-system
// repository comes from a companion push driver; the verifier requires its
// evidence (see tests/capacity/README.md). Chaos is injected by the integrated
// runner (CAPACITY_CHAOS_PROFILE), not from this script.
import exec from 'k6/execution';
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Gauge, Trend } from 'k6/metrics';
import { env, integer } from '../lib/config.js';
import { graphql } from '../lib/graphql.js';

const apiA = env('CAPACITY_API_A');
const apiB = env('CAPACITY_API_B');
const token = env('CAPACITY_TOKEN');
const runID = env('CAPACITY_RUN_ID').toLowerCase().replace(/[^a-z0-9]/g, '').slice(-16);
const namespace = __ENV.CAPACITY_CATEGORY_NAMESPACE || 'default';
const subtreeSize = integer('CAPACITY_CATEGORY_SUBTREE_SIZE', 10000);
const fanout = integer('CAPACITY_CATEGORY_FANOUT', 10, 2);
const mutationRate = integer('CAPACITY_RATE', 20);
const readRate = integer('CAPACITY_CATEGORY_READ_RATE', 10);
const durationSeconds = integer('CAPACITY_CATEGORY_DURATION_SECONDS', 600, 60);
const reparentAfterSeconds = integer('CAPACITY_CATEGORY_REPARENT_AFTER_SECONDS', Math.floor(durationSeconds / 2), 1);
const convergenceTimeoutMs = integer('CAPACITY_CATEGORY_CONVERGENCE_TIMEOUT_SECONDS', 900) * 1000;
const seedBatch = integer('CAPACITY_CATEGORY_SEED_BATCH', 20);

if (apiA === apiB) {
  throw new Error('CAPACITY_API_A and CAPACITY_API_B must identify distinct replicas');
}

const prefix = `chc${runID}`;
const rootName = `${prefix}-n0`;
const anchorName = `${prefix}-anchor`;
const nodeName = (i) => `${prefix}-n${i}`;
const parentIndex = (i) => Math.floor((i - 1) / fanout);
const depthOf = (i) => { let d = 0; for (let n = i; n > 0; n = parentIndex(n)) d += 1; return d; };
const leafIndex = subtreeSize;
const midIndex = Math.min(subtreeSize, 1 + fanout);
// metadata.revision is "<branch>@sha1:<commit>"; only the commit is compared.
const revisionPattern = /^.+@sha1:([0-9a-f]{40,64})$/;
const commitOf = (revision) => { const m = revisionPattern.exec(revision || ''); return m ? m[1] : ''; };

const seeded = new Counter('category_seed_created');
const mutationOK = new Counter('category_mutation_acknowledged');
const mutationFailed = new Counter('category_mutation_failed');
const conflicts = new Counter('category_mutation_conflicts');
const ownCommitChecks = new Counter('category_sc007_own_commit_checks');
const ownCommitViolations = new Counter('category_sc007_violations');
const poolRecords = new Counter('category_sc007_pool_records');
const duplicateRevisions = new Counter('category_sc007_duplicate_revisions');
const filterSetsChecked = new Counter('category_sc009_sets_checked');
const filterSetMismatches = new Counter('category_sc009_set_mismatches');
const filterMissing = new Counter('category_sc009_missing');
const filterExtra = new Counter('category_sc009_extra');
const unreconciled = new Counter('category_unreconciled_at_audit');
const cascadeConverged = new Counter('category_cascade_converged');
const cascadeConvergence = new Trend('category_cascade_convergence_ms', true);
const loadStart = new Gauge('category_load_start_ms');
const loadEnd = new Gauge('category_load_end_ms');

export const options = {
  setupTimeout: '60m',
  teardownTimeout: '60m',
  scenarios: {
    mutations: {
      executor: 'constant-arrival-rate',
      exec: 'mutate',
      rate: mutationRate,
      timeUnit: '1s',
      duration: `${durationSeconds}s`,
      preAllocatedVUs: integer('CAPACITY_PREALLOCATED_VUS', Math.max(20, mutationRate * 2)),
      maxVUs: integer('CAPACITY_MAX_VUS', Math.max(100, mutationRate * 10)),
    },
    filteredReads: {
      executor: 'constant-arrival-rate',
      exec: 'readFiltered',
      rate: readRate,
      timeUnit: '1s',
      duration: `${durationSeconds}s`,
      preAllocatedVUs: Math.max(10, readRate * 2),
      maxVUs: Math.max(50, readRate * 10),
    },
    reparent: {
      executor: 'per-vu-iterations',
      exec: 'reparent',
      vus: 1,
      iterations: 1,
      startTime: `${reparentAfterSeconds}s`,
      maxDuration: `${Math.ceil(convergenceTimeoutMs / 1000) + 120}s`,
    },
  },
  thresholds: {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    http_req_failed: ['rate<0.001'],
    // Plan.md Performance Goals, identical for alpha and production.
    'http_req_duration{operation:createCategory}': ['p(95)<=750', 'p(99)<=2000'],
    'http_req_duration{operation:updateCategory}': ['p(95)<=750', 'p(99)<=2000'],
    'http_req_duration{operation:filteredCategories}': ['p(95)<=150', 'p(99)<=500'],
    category_mutation_failed: ['count==0'],
    category_sc007_violations: ['count==0'],
    category_sc007_duplicate_revisions: ['count==0'],
    category_sc009_set_mismatches: ['count==0'],
    category_sc009_missing: ['count==0'],
    category_sc009_extra: ['count==0'],
    category_unreconciled_at_audit: ['count==0'],
  },
};

const createMutation = `mutation($input: CreateCategoryInput!) {
  createCategory(input: $input) { category { metadata { name revision } spec { title } } }
}`;
const updateMutation = `mutation($input: UpdateCategoryInput!) {
  updateCategory(input: $input) { category { metadata { name revision } spec { title } } }
}`;
const readQuery = `query($namespace: String!, $name: String!) {
  category(by: { namespacePath: { namespace: $namespace, name: $name } }) {
    metadata { name revision } spec { title parentRef { name } } status { resolved { path depth } }
  }
}`;
const pageQuery = `query($namespace: String!, $filter: CategoryFilterInput, $first: Int, $after: String) {
  categories(namespace: $namespace, filter: $filter, first: $first, after: $after) {
    edges { node { metadata { name revision } spec { title parentRef { name } } status { resolved { path depth } } } }
    pageInfo { hasNextPage endCursor }
  }
}`;
const namesOnlyPageQuery = `query($namespace: String!, $filter: CategoryFilterInput, $first: Int, $after: String) {
  categories(namespace: $namespace, filter: $filter, first: $first, after: $after) {
    edges { node { metadata { name } } }
    pageInfo { hasNextPage endCursor }
  }
}`;

function inputFor(name, title, parent) {
  const spec = { title };
  if (parent) spec.parentRef = { name: parent };
  return { metadata: { name, namespace }, spec };
}

function errorCode(body) {
  const first = body.errors && body.errors[0];
  return first && first.extensions && first.extensions.code;
}

// submit runs one create/update, retrying retryable CONFLICTs alternately
// across both replicas. It returns the admitted record or null.
function submit(kind, name, title, parent, seq, tags) {
  const mutation = kind === 'create' ? createMutation : updateMutation;
  const field = kind === 'create' ? 'createCategory' : 'updateCategory';
  const endpoints = seq % 2 === 0 ? [apiA, apiB] : [apiB, apiA];
  let sawConflict = false;
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const endpoint = endpoints[attempt % 2];
    const { response, body } = graphql(endpoint, token, mutation, { input: inputFor(name, title, parent) },
      Object.assign({ operation: `${kind}Category` }, tags));
    const payload = response.status === 200 && !body.errors && body.data && body.data[field];
    if (payload && payload.category) {
      return payload.category;
    }
    const code = errorCode(body);
    if (code === 'CONFLICT') {
      sawConflict = true;
      conflicts.add(1);
    } else if (code === 'ALREADY_EXISTS' && kind === 'create' && sawConflict) {
      // A superseded attempt may have committed: accept only our own record.
      const { body: read } = graphql(endpoint, token, readQuery, { namespace, name }, { operation: 'confirmCategory' });
      const found = read.data && read.data.category;
      if (found && found.spec.title === title) {
        return found;
      }
      return null;
    } else {
      return null;
    }
    sleep(0.025 * (attempt + 1));
  }
  return null;
}

export function setup() {
  for (const endpoint of [apiA, apiB]) {
    const result = graphql(endpoint, token, 'query { __typename }', {}, { operation: 'preflight' });
    if (result.response.status !== 200 || result.body.errors) {
      throw new Error(`GraphQL preflight failed for ${endpoint}`);
    }
  }
  for (const [name, parent] of [[anchorName, ''], [rootName, '']]) {
    if (!submit('create', name, `seed-${name}`, parent, 0, { traffic: 'seed' })) {
      throw new Error(`cannot create ${name}`);
    }
  }
  // Level by level so every parent is admitted before its children.
  let level = [];
  let currentDepth = 1;
  const flush = () => {
    for (let i = 0; i < level.length; i += seedBatch) {
      const chunk = level.slice(i, i + seedBatch);
      const responses = http.batch(chunk.map((index, n) => ({
        method: 'POST',
        url: (index + n) % 2 === 0 ? apiA : apiB,
        body: JSON.stringify({
          query: createMutation,
          variables: { input: inputFor(nodeName(index), `seed-${nodeName(index)}`, nodeName(parentIndex(index))) },
        }),
        params: { headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, tags: { operation: 'seedCategory', traffic: 'seed' } },
      })));
      responses.forEach((response, n) => {
        let ok = false;
        try { const b = response.json(); ok = !b.errors && b.data.createCategory.category; } catch (_) { ok = false; }
        if (!ok && !submit('create', nodeName(chunk[n]), `seed-${nodeName(chunk[n])}`, nodeName(parentIndex(chunk[n])), n, { traffic: 'seed' })) {
          throw new Error(`cannot seed ${nodeName(chunk[n])}`);
        }
        seeded.add(1);
      });
    }
    level = [];
  };
  for (let index = 1; index <= subtreeSize; index += 1) {
    if (depthOf(index) !== currentDepth) {
      flush();
      currentDepth = depthOf(index);
    }
    level.push(index);
  }
  flush();
  if (!waitForPath(nodeName(leafIndex), (path) => path.length === depthOf(leafIndex) + 1 && path[0] === rootName, convergenceTimeoutMs)) {
    throw new Error('seed subtree did not reconcile before the timeout');
  }
  loadStart.add(Date.now());
  return { startedAt: Date.now() };
}

function waitForPath(name, predicate, timeoutMs) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const { body } = graphql(apiB, token, readQuery, { namespace, name }, { operation: 'convergenceProbe' });
    const node = body.data && body.data.category;
    const resolved = node && node.status && node.status.resolved;
    if (resolved && predicate(resolved.path)) {
      return Date.now() - started;
    }
    sleep(0.5);
  }
  return 0;
}

export function mutate() {
  const seq = exec.scenario.iterationInTest;
  // Updates touch each seed node at most once and never the root, so every
  // mutation in the pool owns exactly one commit and the cascade is only
  // driven by the dedicated re-parent step.
  const updateIndex = 1 + (seq >> 1);
  const doUpdate = seq % 2 === 1 && updateIndex < subtreeSize;
  let name, title, parent, kind;
  if (doUpdate) {
    kind = 'update';
    name = nodeName(updateIndex);
    title = `u-${runID}-${name}`;
    parent = nodeName(parentIndex(updateIndex));
  } else {
    kind = 'create';
    name = `${prefix}-m-${seq}`;
    title = `c-${runID}-${name}`;
    parent = nodeName(1 + ((seq * 7919) % subtreeSize));
  }
  const record = submit(kind, name, title, parent, seq, {});
  const own = record && record.metadata.name === name && record.spec.title === title &&
    commitOf(record.metadata.revision) !== '';
  check({ own }, { [`${kind}Category acknowledged with its own commit`]: (v) => !!v.own });
  if (!record) {
    mutationFailed.add(1);
    return;
  }
  mutationOK.add(1);
  ownCommitChecks.add(1);
  if (!own) ownCommitViolations.add(1);
}

export function readFiltered() {
  const seq = exec.scenario.iterationInTest;
  const targets = [
    { descendantOf: rootName, includeSelf: false },
    { descendantOf: nodeName(midIndex), includeSelf: true },
    { descendantOf: anchorName, includeSelf: true },
    { descendantOf: rootName, includeSelf: false, maxDepth: 2 },
  ];
  const endpoint = seq % 2 === 0 ? apiA : apiB;
  const { response, body } = graphql(endpoint, token, namesOnlyPageQuery,
    { namespace, filter: targets[seq % targets.length], first: 100 }, { operation: 'filteredCategories' });
  check({ response, body }, {
    'filtered categories page succeeds': ({ response: r, body: b }) =>
      r.status === 200 && !b.errors && b.data && b.data.categories.edges.length <= 100,
  });
}

export function reparent() {
  const started = Date.now();
  const record = submit('update', rootName, `seed-${rootName}`, anchorName, 1, { phase: 'reparent' });
  check({ record }, { 're-parent of subtree root acknowledged': (v) => !!v.record });
  if (!record) return;
  const took = waitForPath(nodeName(leafIndex), (path) => path[0] === anchorName && path[1] === rootName, convergenceTimeoutMs);
  if (took > 0) {
    cascadeConverged.add(1);
    cascadeConvergence.add(Date.now() - started);
  }
}

function listAll(endpoint, filter, query) {
  const nodes = [];
  let after = null;
  for (let guard = 0; guard < 5000; guard += 1) {
    const { body } = graphql(endpoint, token, query, { namespace, filter, first: 100, after }, { operation: 'auditList' });
    if (!body.data) throw new Error(`audit list failed: ${JSON.stringify(body.errors)}`);
    for (const edge of body.data.categories.edges) nodes.push(edge.node);
    const info = body.data.categories.pageInfo;
    if (!info.hasNextPage) break;
    after = info.endCursor;
  }
  return nodes;
}

// walkSet computes the expected filtered result from status.resolved.path only.
function walkSet(all, root, includeSelf, maxDepth) {
  const names = [];
  for (const node of all) {
    const resolved = node.status && node.status.resolved;
    if (!resolved) continue;
    const at = resolved.path.indexOf(root);
    if (at < 0) continue;
    const relative = resolved.path.length - 1 - at;
    if (relative === 0 ? includeSelf : (!maxDepth || relative <= maxDepth)) names.push(node.metadata.name);
  }
  return names;
}

export function teardown() {
  loadEnd.add(Date.now());
  waitForPath(nodeName(leafIndex), (path) => path[0] === anchorName && path[1] === rootName, convergenceTimeoutMs);

  // Wait for every category created by the run to be reconciled, then audit.
  let all = [];
  const deadline = Date.now() + convergenceTimeoutMs;
  let pending = 0;
  do {
    all = listAll(apiA, null, pageQuery);
    pending = all.filter((n) => n.metadata.name.startsWith(prefix) && !(n.status && n.status.resolved)).length;
    if (pending > 0) sleep(5);
  } while (pending > 0 && Date.now() < deadline);
  unreconciled.add(pending);

  const cases = [
    { descendantOf: rootName, includeSelf: false },
    { descendantOf: rootName, includeSelf: true },
    { descendantOf: anchorName, includeSelf: true },
    { descendantOf: nodeName(midIndex), includeSelf: false },
    { descendantOf: nodeName(midIndex), includeSelf: true, maxDepth: 1 },
    { descendantOf: nodeName(leafIndex), includeSelf: false },
    { descendantOf: `${prefix}-does-not-exist`, includeSelf: true },
  ];
  for (const endpoint of [apiA, apiB]) {
    for (const filter of cases) {
      const expected = new Set(walkSet(all, filter.descendantOf, filter.includeSelf, filter.maxDepth));
      const actualList = listAll(endpoint, filter, namesOnlyPageQuery).map((n) => n.metadata.name);
      const actual = new Set(actualList);
      let missing = 0;
      let extra = 0;
      expected.forEach((n) => { if (!actual.has(n)) missing += 1; });
      actual.forEach((n) => { if (!expected.has(n)) extra += 1; });
      filterSetsChecked.add(1);
      filterMissing.add(missing);
      filterExtra.add(extra);
      if (missing > 0 || extra > 0 || actualList.length !== actual.size) filterSetMismatches.add(1);
    }
  }

  // SC-007: each pool record is touched by exactly one mutation, so its stored
  // revision must be a commit id, carry its own content, and be unique.
  const seen = new Set();
  for (const node of all) {
    const title = node.spec.title;
    const own = title === `c-${runID}-${node.metadata.name}` || title === `u-${runID}-${node.metadata.name}`;
    if (!own) continue;
    poolRecords.add(1);
    const commit = commitOf(node.metadata.revision);
    if (commit === '' || seen.has(commit)) {
      duplicateRevisions.add(1);
    }
    seen.add(commit);
  }
}

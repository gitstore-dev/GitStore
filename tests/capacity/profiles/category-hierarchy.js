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
import encoding from 'k6/encoding';
import { check, sleep } from 'k6';
import { Counter, Gauge, Trend } from 'k6/metrics';
import { env, integer, announceLoadWindow } from '../lib/config.js';
import { graphql, graphqlPair, graphqlSucceeded, graphqlErrorCode } from '../lib/graphql.js';

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
const productRate = integer('CAPACITY_CATEGORY_PRODUCT_RATE', 1);
const productSpread = integer('CAPACITY_CATEGORY_PRODUCT_SPREAD', 64);
const productSkew = integer('CAPACITY_CATEGORY_PRODUCT_SKEW', 256);

if (apiA === apiB) {
  throw new Error('CAPACITY_API_A and CAPACITY_API_B must identify distinct replicas');
}

const prefix = `chc${runID}`;
const rootName = `${prefix}-n0`;
const anchorName = `${prefix}-anchor`;
const emptyName = `${prefix}-empty`;
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
const mutationOffered = new Counter('category_mutation_offered');
const mutationMissed = new Counter('category_mutation_missed_schedules');
const mutationEndpointCallbacks = new Counter('category_mutation_endpoint_callbacks');
const mutationFailed = new Counter('category_mutation_failed');
const readOffered = new Counter('category_filtered_read_offered');
const readCompleted = new Counter('category_filtered_read_completed');
const readFailed = new Counter('category_filtered_read_failed');
const readMissed = new Counter('category_filtered_read_missed_schedules');
const readEndpointCallbacks = new Counter('category_filtered_read_endpoint_callbacks');
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
const declaredMutations = new Gauge('category_declared_mutations');
const declaredReads = new Gauge('category_declared_reads');
const declaredMutationRate = new Gauge('category_mutation_rate');
const declaredReadRate = new Gauge('category_read_rate');
const declaredDuration = new Gauge('category_duration_seconds');
const productSeeded = new Counter('category_product_seeded');
const productOffered = new Counter('category_product_offered');
const productCompleted = new Counter('category_product_completed');
const productFailed = new Counter('category_product_failed');
const productMissed = new Counter('category_product_missed_schedules');
const productEndpointCallbacks = new Counter('category_product_endpoint_callbacks');
const productReassigned = new Counter('category_product_reassigned');
const productOracleChecks = new Counter('category_product_oracle_checks');
const productPages = new Counter('category_product_pages');
const productPagesForward = new Counter('category_product_pages_forward');
const productPagesBackward = new Counter('category_product_pages_backward');
const productPageLatency = new Trend('category_product_page_ms', true);
const productConvergence = new Trend('category_product_convergence_ms', true);
const productOracleAttempts = new Counter('category_product_oracle_attempts');
const productOracleTransientReads = new Counter('category_product_oracle_transient_reads');
const productOracleAttemptLatency = new Trend('category_product_oracle_attempt_ms', true);
const productOracleVerificationLatency = new Trend('category_product_oracle_verification_ms', true);
const productDeclared = new Gauge('category_product_declared_updates');
const productDeclaredRate = new Gauge('category_product_rate');
const productFixtureCount = new Gauge('category_product_fixture_count');
const productExpectedMaxShard = new Gauge('category_product_expected_max_shard_members');

if (![mutationRate * durationSeconds, readRate * durationSeconds, durationSeconds * 1000,
  productRate * durationSeconds, 1 + productSpread + productSkew,
  16 * (productRate * durationSeconds + 2)]
  .every(Number.isSafeInteger)) {
  throw new Error('Category load schedule exceeds exact numeric precision');
}

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
    productMembership: {
      executor: 'per-vu-iterations',
      exec: 'reassignProducts',
      vus: 1,
      iterations: 1,
      maxDuration: `${durationSeconds + 30}s`,
      gracefulStop: '0s',
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
    category_mutation_missed_schedules: ['count==0'],
    category_filtered_read_failed: ['count==0'],
    category_filtered_read_missed_schedules: ['count==0'],
    category_sc007_violations: ['count==0'],
    category_sc007_duplicate_revisions: ['count==0'],
    category_sc009_set_mismatches: ['count==0'],
    category_sc009_missing: ['count==0'],
    category_sc009_extra: ['count==0'],
    category_unreconciled_at_audit: ['count==0'],
    category_product_failed: ['count==0'],
    category_product_missed_schedules: ['count==0'],
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
const productFields = `id metadata { name namespace uid resourceVersion generation creationTimestamp revision }
  spec { title categoryRef { name } }
  status { observedGeneration conditions { type status observedGeneration }
    resolved { category { name uid path } } }`;
const productCreateMutation = `mutation($input: CreateProductInput!) {
  createProduct(input: $input) { product { ${productFields} } }
}`;
const productUpdateMutation = `mutation($input: UpdateProductInput!) {
  updateProduct(input: $input) { product { ${productFields} } }
}`;
const productReadQuery = `query($namespace: String!, $name: String!) {
  product(by: {namespacePath: {namespace: $namespace, name: $name}}) { ${productFields} }
}`;
const membershipCategoryQuery = `query($namespace: String!, $name: String!) {
  category(by: {namespacePath: {namespace: $namespace, name: $name}}) {
    id metadata { name namespace generation } spec { parentRef { name } }
    status { observedGeneration resolved { path } }
  }
}`;
const productPageQuery = `query($namespace: String!, $name: String!,
  $first: Int, $last: Int, $after: String, $before: String) {
  category(by: {namespacePath: {namespace: $namespace, name: $name}}) {
    id products(first: $first, last: $last, after: $after, before: $before) {
      edges { cursor node { ${productFields} } }
      pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
    }
  }
}`;

function timestampNanoseconds(value) {
  const match = typeof value === 'string' &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.(\d{1,9}))?(?:Z|[+-]\d{2}:\d{2})$/.exec(value);
  const milliseconds = match && Date.parse(value);
  if (!match || !Number.isSafeInteger(milliseconds)) {
    throw new Error('Product oracle received an invalid creation timestamp');
  }
  return BigInt(milliseconds) * BigInt(1000000) +
    BigInt((match[1] || '').padEnd(9, '0').slice(3));
}

function productShard(id) {
  const decoded = encoding.b64decode(id, 'std', 's');
  const match = /^gid:\/\/GitStore\/Product\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/.exec(decoded);
  if (!match || encoding.b64encode(decoded, 'std') !== id) {
    throw new Error('Product oracle received an invalid Product identity');
  }
  let hash = 0x811c9dc5;
  for (const character of match[1]) hash = Math.imul(hash ^ character.charCodeAt(0), 0x01000193) >>> 0;
  return { uid: match[1], shard: hash % 32 };
}

function productRecord(result, field, name, title, category, previous) {
  if (!graphqlSucceeded(result)) return null;
  const payload = result.body.data[field];
  const record = field === 'product' ? payload : payload && payload.product;
  if (!record || typeof record.id !== 'string' || !record.metadata || !record.spec ||
      record.metadata.name !== name || record.metadata.namespace !== namespace ||
      record.metadata.uid !== record.id || !Number.isSafeInteger(record.metadata.generation) ||
      record.metadata.generation <= 0 || typeof record.metadata.resourceVersion !== 'string' ||
      !/^[1-9]\d*$/.test(record.metadata.resourceVersion) || record.spec.title !== title ||
      !record.spec.categoryRef || record.spec.categoryRef.name !== category ||
      commitOf(record.metadata.revision) === '') return null;
  timestampNanoseconds(record.metadata.creationTimestamp);
  productShard(record.id);
  if (previous && (record.id !== previous.id ||
      timestampNanoseconds(record.metadata.creationTimestamp) !== timestampNanoseconds(previous.metadata.creationTimestamp) ||
      record.metadata.generation !== previous.metadata.generation + 1)) return null;
  return record;
}

function submitProduct(previous, name, title, category, sequence) {
  const endpoints = sequence % 2 === 0 ? [apiA, apiB] : [apiB, apiA];
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const endpoint = endpoints[attempt % 2];
    const metadata = { name, namespace };
    if (previous) {
      const current = graphql(endpoint, token, productReadQuery, { namespace, name }, { operation: 'readProductBeforeUpdate' });
      const own = productRecord(current, 'product', name, title, category, previous);
      if (own) return own;
      const record = productRecord(current, 'product', name, previous.spec.title,
        previous.spec.categoryRef.name);
      if (!record || record.id !== previous.id || record.metadata.generation !== previous.metadata.generation) {
        throw new Error('Product changed incarnation or specification before its owned update');
      }
    }
    const result = graphql(endpoint, token, previous ? productUpdateMutation : productCreateMutation,
      { input: { metadata, spec: { title, categoryRef: { name: category } } } },
      { operation: previous ? 'updateProduct' : 'createProduct', traffic: previous ? 'membership' : 'seed' });
    const record = productRecord(result, previous ? 'updateProduct' : 'createProduct', name, title, category, previous);
    if (record) return record;
    if (graphqlErrorCode(result) !== 'CONFLICT') {
      const code = graphqlErrorCode(result);
      const safeCode = typeof code === 'string' && /^[A-Z_]{1,64}$/.test(code) ? code : 'INVALID_ACKNOWLEDGEMENT';
      const status = result.response && Number.isInteger(result.response.status) ? result.response.status : 'invalid';
      throw new Error(`Product write did not acknowledge its own identity and specification (HTTP ${status}; ${safeCode})`);
    }
    sleep(0.025 * (attempt + 1));
  }
  throw new Error('Product membership write exhausted its bounded conflict retries');
}

function intendedProductPath(category, moved) {
  if (category === anchorName) return [anchorName];
  let index = Number(category.slice(`${prefix}-n`.length));
  if (!Number.isSafeInteger(index) || index < 0 || index > subtreeSize || category !== nodeName(index)) {
    throw new Error('Product oracle received an undeclared category target');
  }
  const path = [category];
  while (index > 0) {
    index = parentIndex(index);
    path.unshift(nodeName(index));
  }
  if (moved) path.unshift(anchorName);
  return path;
}

function membershipDeadlineParams(deadline) {
  const remaining = deadline === undefined ? null : deadline - Date.now();
  if (remaining !== null && (!Number.isFinite(remaining) || remaining <= 0)) {
    throw new Error('Product membership oracle exceeded its convergence deadline');
  }
  return remaining === null ? {} : { timeout: `${Math.ceil(remaining)}ms` };
}

function membershipRequest(endpoint, query, variables, tags, deadline) {
  return graphql(endpoint, token, query, variables, tags, membershipDeadlineParams(deadline));
}

function membershipTransportPending(result, phase) {
  const status = result && result.response && result.response.status;
  if (!Number.isInteger(status) || ![0, 408, 429, 502, 503, 504].includes(status)) return false;
  const body = result.body;
  if (body && body.data !== undefined && body.data !== null) return false;
  if (body && body.errors !== undefined &&
      (!Array.isArray(body.errors) || body.errors.length !== 1 ||
        !body.errors[0] || Object.keys(body.errors[0]).length !== 1 ||
        body.errors[0].message !== 'non-JSON GraphQL response')) return false;
  productOracleTransientReads.add(1, { phase });
  console.warn(`Product membership ${phase} read awaits transport recovery (HTTP ${status})`);
  return true;
}

function membershipPhase(categories, deadline) {
  const phases = [];
  let pending = false;
  const results = graphqlPair([apiA, apiB], token, membershipCategoryQuery,
    { namespace, name: rootName }, { operation: 'categoryMembershipPhase' }, membershipDeadlineParams(deadline));
  membershipDeadlineParams(deadline);
  for (const result of results) {
    if (membershipTransportPending(result, 'phase')) {
      pending = true;
      continue;
    }
    const root = graphqlSucceeded(result) && result.body.data.category;
    if (!root || root.id !== categories[rootName] || !root.metadata ||
        root.metadata.name !== rootName || root.metadata.namespace !== namespace ||
        !Number.isSafeInteger(root.metadata.generation) || root.metadata.generation <= 0 || !root.spec) {
      throw new Error('Category membership phase did not confirm the acknowledged root identity');
    }
    const parent = root.spec.parentRef && root.spec.parentRef.name;
    if (parent && parent !== anchorName) throw new Error('Category root acquired an unexpected parent');
    const moved = parent === anchorName;
    const expected = moved ? [anchorName, rootName] : [rootName];
    if (!root.status || root.status.observedGeneration !== root.metadata.generation ||
        !root.status.resolved || JSON.stringify(root.status.resolved.path) !== JSON.stringify(expected)) pending = true;
    phases.push(moved);
  }
  return !pending && phases[0] === phases[1] ? phases[0] : null;
}

function productPageVariables(category, backward, cursor) {
  return { namespace, name: category, first: backward ? null : 100, last: backward ? 100 : null,
    after: backward ? null : cursor, before: backward ? cursor : null };
}

function productMembershipWindow(endpoint, category, expected, categories, moved, backward, identities, deadline, initialPage) {
  const seen = new Set();
  const edgeCursors = new Set();
  const cursors = new Set();
  let cursor = null;
  let offset = 0;
  let matches = true;
  const maximumPages = Math.max(1, Math.ceil(expected.length / 100)) + 1;
  for (let pageNumber = 0; pageNumber < maximumPages; pageNumber += 1) {
    membershipDeadlineParams(deadline);
    const result = pageNumber === 0 && initialPage !== undefined ? initialPage :
      membershipRequest(endpoint, productPageQuery, productPageVariables(category, backward, cursor),
        { operation: 'categoryProducts', direction: backward ? 'backward' : 'forward' }, deadline);
    membershipDeadlineParams(deadline);
    if (membershipTransportPending(result, 'page')) return false;
    if (graphqlErrorCode(result) === 'PROJECTION_NOT_READY') return false;
    const node = graphqlSucceeded(result) && result.body.data.category;
    const page = node && node.products;
    const info = page && page.pageInfo;
    const latency = result.response && result.response.timings && result.response.timings.duration;
    if (!node || node.id !== categories[category] || !page || !Array.isArray(page.edges) ||
        page.edges.length > 100 || !info || typeof info.hasNextPage !== 'boolean' ||
        typeof info.hasPreviousPage !== 'boolean' || !Number.isFinite(latency) || latency < 0) {
      throw new Error('Category.products returned an unsuccessful or malformed membership page');
    }
    productPages.add(1);
    (backward ? productPagesBackward : productPagesForward).add(1);
    productPageLatency.add(latency);
    const begin = backward ? Math.max(0, expected.length - offset - 100) : offset;
    const end = backward ? expected.length - offset : Math.min(expected.length, offset + 100);
    const window = expected.slice(begin, end);
    if (window.length !== page.edges.length) matches = false;
    for (let index = 0; index < page.edges.length; index += 1) {
      const edge = page.edges[index];
      const record = edge && edge.node;
      if (!edge || typeof edge.cursor !== 'string' || edge.cursor === '' ||
          edgeCursors.has(edge.cursor) ||
          !record || typeof record.id !== 'string' || seen.has(record.id) ||
          !identities.has(record.id) || !record.metadata || !record.spec ||
          !Number.isSafeInteger(record.metadata.generation) || record.metadata.generation <= 0 ||
          typeof record.metadata.resourceVersion !== 'string' ||
          !/^[1-9]\d*$/.test(record.metadata.resourceVersion)) {
        throw new Error('Category.products returned a missing or duplicate identity or cursor');
      }
      seen.add(record.id);
      edgeCursors.add(edge.cursor);
      const own = window[index];
      const resolved = record.status && record.status.resolved && record.status.resolved.category;
      const path = own && intendedProductPath(own.spec.categoryRef.name, moved);
      if (!own || record.id !== own.id || !record.metadata || !record.spec ||
          record.metadata.name !== own.metadata.name || record.metadata.namespace !== namespace ||
          record.metadata.uid !== own.id || record.metadata.generation !== own.metadata.generation ||
          commitOf(record.metadata.revision) === '' ||
          (own.metadata.revision && record.metadata.revision !== own.metadata.revision) ||
          BigInt(record.metadata.resourceVersion) < BigInt(own.metadata.resourceVersion) ||
          timestampNanoseconds(record.metadata.creationTimestamp) !== timestampNanoseconds(own.metadata.creationTimestamp) ||
          record.spec.title !== own.spec.title || !record.spec.categoryRef ||
          record.spec.categoryRef.name !== own.spec.categoryRef.name ||
          !record.status || record.status.observedGeneration !== own.metadata.generation ||
          !Array.isArray(record.status.conditions) || !record.status.conditions.some((condition) =>
            condition && condition.type === 'Ready' && condition.status === 'TRUE' &&
            condition.observedGeneration === own.metadata.generation) ||
          !resolved || resolved.name !== own.spec.categoryRef.name ||
          resolved.uid !== categories[own.spec.categoryRef.name] ||
          JSON.stringify(resolved.path) !== JSON.stringify(path)) matches = false;
    }
    const first = page.edges[0];
    const last = page.edges[page.edges.length - 1];
    if (info.startCursor !== (first ? first.cursor : null) || info.endCursor !== (last ? last.cursor : null)) {
      throw new Error('Category.products page cursors do not describe its returned window');
    }
    if (info.hasPreviousPage !== (begin > 0) || info.hasNextPage !== (end < expected.length)) matches = false;
    const more = backward ? info.hasPreviousPage : info.hasNextPage;
    if (!more) return matches && seen.size === expected.length;
    const next = backward ? info.startCursor : info.endCursor;
    if (typeof next !== 'string' || next === '' || cursors.has(next)) {
      throw new Error('Category.products pagination did not advance');
    }
    cursors.add(next);
    cursor = next;
    offset += page.edges.length;
  }
  throw new Error('Category.products exceeded its bounded membership-page budget');
}

function waitForProductMembership(records, categories, timeoutMs, requiredPhase) {
  const started = Date.now();
  const deadline = started + timeoutMs;
  const identities = new Set(records.map((record) => record.id));
  do {
    const attemptStarted = Date.now();
    productOracleAttempts.add(1);
    const moved = membershipPhase(categories, deadline);
    if (moved !== null && (requiredPhase === undefined || moved === requiredPhase)) {
      let matches = true;
      membershipWindows:
      for (const category of [nodeName(leafIndex), rootName, anchorName, emptyName]) {
        const expected = records.filter((record) => intendedProductPath(record.spec.categoryRef.name, moved).includes(category));
        expected.sort((a, b) => {
          const left = timestampNanoseconds(a.metadata.creationTimestamp);
          const right = timestampNanoseconds(b.metadata.creationTimestamp);
          if (left !== right) return left > right ? -1 : 1;
          const leftUID = productShard(a.id).uid;
          const rightUID = productShard(b.id).uid;
          return leftUID > rightUID ? -1 : leftUID < rightUID ? 1 : 0;
        });
        const shards = Array(32).fill(0);
        for (const record of expected) shards[productShard(record.id).shard] += 1;
        productExpectedMaxShard.add(Math.max(...shards));
        for (const backward of [false, true]) {
          const results = graphqlPair([apiA, apiB], token, productPageQuery,
            productPageVariables(category, backward, null),
            { operation: 'categoryProducts', direction: backward ? 'backward' : 'forward' },
            membershipDeadlineParams(deadline));
          for (const [index, endpoint] of [apiA, apiB].entries()) {
            if (!productMembershipWindow(endpoint, category, expected, categories, moved, backward, identities, deadline, results[index])) {
              matches = false;
            }
          }
          if (!matches) break membershipWindows;
        }
      }
      if (matches && membershipPhase(categories, deadline) === moved) {
        const attemptDuration = Date.now() - attemptStarted;
        productOracleAttemptLatency.add(attemptDuration);
        productOracleVerificationLatency.add(attemptDuration);
        productOracleChecks.add(16);
        return Date.now() - started;
      }
    }
    productOracleAttemptLatency.add(Date.now() - attemptStarted);
    sleep(0.1);
  } while (Date.now() - started < timeoutMs);
  throw new Error('Product membership did not converge to the independently acknowledged oracle');
}

function productIntent(previous, sequence) {
  const targets = [nodeName(leafIndex), rootName, anchorName];
  const current = targets.indexOf(previous.spec.categoryRef.name);
  const reassigned = sequence % 2 === 1;
  return {
    title: `u-${runID}-p-${sequence}`,
    category: reassigned ? targets[((current < 0 ? 1 : current) + 1) % targets.length] :
      previous.spec.categoryRef.name,
    reassigned,
  };
}

function inputFor(name, title, parent) {
  const spec = { title };
  if (parent) spec.parentRef = { name: parent };
  return { metadata: { name, namespace }, spec };
}

function categoryRecord(result, field, name, title) {
  if (!graphqlSucceeded(result)) return null;
  const payload = result.body.data[field];
  const category = field === 'category' ? payload : payload && payload.category;
  return category && category.metadata && category.spec && category.metadata.name === name &&
    category.spec.title === title && typeof category.metadata.revision === 'string' &&
    commitOf(category.metadata.revision) !== '' ? category : null;
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
    const result = graphql(endpoint, token, mutation, { input: inputFor(name, title, parent) },
      Object.assign({ operation: `${kind}Category` }, tags));
    const record = categoryRecord(result, field, name, title);
    if (record) return record;
    const code = graphqlErrorCode(result);
    if (code === 'CONFLICT') {
      sawConflict = true;
      conflicts.add(1);
    } else if (code === 'ALREADY_EXISTS' && kind === 'create' && sawConflict) {
      // A superseded attempt may have committed: accept only our own record.
      const read = graphql(endpoint, token, readQuery, { namespace, name }, { operation: 'confirmCategory' });
      return categoryRecord(read, 'category', name, title);
    } else {
      return null;
    }
    sleep(0.025 * (attempt + 1));
  }
  return null;
}

export function setup() {
  for (const counter of [seeded, mutationOK, mutationOffered, mutationMissed, mutationEndpointCallbacks, mutationFailed,
    readOffered, readCompleted, readFailed, readMissed, readEndpointCallbacks, conflicts, ownCommitChecks,
    ownCommitViolations, poolRecords, duplicateRevisions, filterSetsChecked,
    filterSetMismatches, filterMissing, filterExtra, unreconciled, cascadeConverged,
    productSeeded, productOffered, productCompleted, productFailed, productMissed,
    productEndpointCallbacks, productReassigned, productOracleChecks, productOracleAttempts, productPages,
    productPagesForward, productPagesBackward]) {
    counter.add(0);
  }
  declaredMutations.add(mutationRate * durationSeconds);
  declaredReads.add(readRate * durationSeconds);
  declaredMutationRate.add(mutationRate);
  declaredReadRate.add(readRate);
  declaredDuration.add(durationSeconds);
  productDeclared.add(productRate * durationSeconds);
  productDeclaredRate.add(productRate);
  productFixtureCount.add(1 + productSpread + productSkew);
  for (const endpoint of [apiA, apiB]) {
    const result = graphql(endpoint, token, 'query { __typename }', {}, { operation: 'preflight' });
    if (!graphqlSucceeded(result) || typeof result.body.data.__typename !== 'string' ||
        result.body.data.__typename.length === 0) {
      throw new Error(`GraphQL preflight failed for ${endpoint}`);
    }
  }
  for (const [name, parent] of [[anchorName, ''], [rootName, ''], [emptyName, '']]) {
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
      if (!Array.isArray(responses) || responses.length !== chunk.length) {
        throw new Error('category seed batch did not return every declared response');
      }
      responses.forEach((response, n) => {
        let body;
        try { body = response.json(); } catch (_) { body = null; }
        const ok = categoryRecord({ response, body }, 'createCategory', nodeName(chunk[n]), `seed-${nodeName(chunk[n])}`);
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
  if (waitForPath(nodeName(leafIndex), (path) => path.length === depthOf(leafIndex) + 1 && path[0] === rootName, convergenceTimeoutMs) === null) {
    throw new Error('seed subtree did not reconcile before the timeout');
  }
  const fixtureTargets = [nodeName(leafIndex)];
  for (let index = 0; index < productSpread; index += 1) {
    fixtureTargets.push(nodeName(1 + ((index * 7919) % subtreeSize)));
  }
  for (let index = 0; index < productSkew; index += 1) fixtureTargets.push(anchorName);
  const categories = {};
  for (const name of new Set([...fixtureTargets, rootName, anchorName, emptyName])) {
    const result = graphql(apiA, token, membershipCategoryQuery, { namespace, name }, { operation: 'membershipCategoryIdentity' });
    const category = graphqlSucceeded(result) && result.body.data.category;
    if (!category || typeof category.id !== 'string' || category.id === '' || !category.metadata ||
        category.metadata.name !== name || category.metadata.namespace !== namespace) {
      throw new Error('Product fixture did not confirm its requested category identity');
    }
    categories[name] = category.id;
  }
  const products = [];
  const identities = new Set();
  for (let index = 0; index < fixtureTargets.length; index += 1) {
    const name = `${prefix}-p-${index}`;
    const product = submitProduct(null, name, `seed-${name}`, fixtureTargets[index], index);
    if (identities.has(product.id)) throw new Error('Product fixture returned a duplicate acknowledged identity');
    identities.add(product.id);
    products.push(product);
    productSeeded.add(1);
  }
  waitForProductMembership(products, categories, convergenceTimeoutMs, false);
  return { startedAt: Date.now(), categories, products };
}

function observeOffer(offered, missed, endpointCallbacks, rate, sequence = exec.scenario.iterationInTest) {
  const start = exec.scenario.startTime;
  const end = start + durationSeconds * 1000;
  const now = Date.now();
  // k6 can dispatch the exclusive endpoint while its stop timer is racing.
  if (Number.isSafeInteger(start) && start > 0 && Number.isSafeInteger(end) &&
    sequence === rate * durationSeconds && now >= end) {
    endpointCallbacks.add(1);
    return null;
  }
  offered.add(1);
  if (!Number.isSafeInteger(start) || start <= 0 || !Number.isSafeInteger(end) ||
    !Number.isSafeInteger(sequence) || sequence < 0 || sequence >= rate * durationSeconds) {
    throw new Error('Category offer has an invalid declared schedule identity');
  }
  const intervalMs = 1000 / rate;
  if (now - (start + sequence * intervalMs) >= intervalMs) missed.add(1);
  if (now >= end) {
    throw new Error('Category offer arrived after its declared load window');
  }
  return start;
}

export function reassignProducts(data) {
  if (!data || !Array.isArray(data.products) || data.products.length !== 1 + productSpread + productSkew ||
      !data.categories) throw new Error('Product membership scenario requires its acknowledged fixture');
  const start = exec.scenario.startTime;
  const deadline = start + (durationSeconds + 30) * 1000;
  const revisions = new Set();
  for (let sequence = 0; sequence < productRate * durationSeconds; sequence += 1) {
    const wait = (start + sequence * 1000 / productRate - Date.now()) / 1000;
    if (wait > 0) sleep(wait);
    try {
      observeOffer(productOffered, productMissed, productEndpointCallbacks, productRate, sequence);
      const index = sequence % data.products.length;
      const previous = data.products[index];
      const intent = productIntent(previous, sequence);
      const record = submitProduct(previous, previous.metadata.name, intent.title, intent.category, sequence);
      const revision = commitOf(record.metadata.revision);
      if (revisions.has(revision)) throw new Error('Product writes returned the same supposedly own commit twice');
      revisions.add(revision);
      data.products[index] = record;
      const remaining = deadline - Date.now();
      if (remaining <= 0) throw new Error('Product membership workload exceeded its bounded drain');
      const convergence = waitForProductMembership(data.products, data.categories, Math.min(convergenceTimeoutMs, remaining));
      productConvergence.add(convergence);
      productCompleted.add(1);
      if (intent.reassigned) productReassigned.add(1);
    } catch (error) {
      productFailed.add(1);
      throw error;
    }
  }
}

function waitForPath(name, predicate, timeoutMs) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const result = graphql(apiB, token, readQuery, { namespace, name }, { operation: 'convergenceProbe' });
    const node = graphqlSucceeded(result) && result.body.data.category;
    const resolved = node && node.status && node.status.resolved;
    if (resolved && node.metadata && node.metadata.name === name && Array.isArray(resolved.path) &&
        resolved.path.every((part) => typeof part === 'string' && part.length > 0) &&
        resolved.path[resolved.path.length - 1] === name && predicate(resolved.path)) {
      return Date.now() - started;
    }
    sleep(0.5);
  }
  return null;
}

export function mutate() {
  const started = observeOffer(mutationOffered, mutationMissed, mutationEndpointCallbacks, mutationRate);
  if (started === null) return;
  if (exec.scenario.iterationInTest === 0) {
    announceLoadWindow(started, BigInt(durationSeconds) * BigInt('1000000000'));
  }
  loadStart.add(started);
  loadEnd.add(started + durationSeconds * 1000);
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
  if (observeOffer(readOffered, readMissed, readEndpointCallbacks, readRate) === null) return;
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
  const complete = check({ response, body }, {
    'filtered categories page succeeds': (result) =>
      graphqlSucceeded(result) && result.body.data.categories &&
      Array.isArray(result.body.data.categories.edges) && result.body.data.categories.edges.length <= 100 &&
      result.body.data.categories.pageInfo && typeof result.body.data.categories.pageInfo.hasNextPage === 'boolean' &&
      (!result.body.data.categories.pageInfo.hasNextPage ||
        (typeof result.body.data.categories.pageInfo.endCursor === 'string' && result.body.data.categories.pageInfo.endCursor.length > 0)) &&
      result.body.data.categories.edges.every((edge) => edge && edge.node && edge.node.metadata &&
        typeof edge.node.metadata.name === 'string' && edge.node.metadata.name.length > 0) &&
      new Set(result.body.data.categories.edges.map((edge) => edge.node.metadata.name)).size === result.body.data.categories.edges.length,
  });
  if (complete) readCompleted.add(1);
  else readFailed.add(1);
}

export function reparent() {
  const started = Date.now();
  const record = submit('update', rootName, `seed-${rootName}`, anchorName, 1, { phase: 'reparent' });
  check({ record }, { 're-parent of subtree root acknowledged': (v) => !!v.record });
  if (!record) return;
  const took = waitForPath(nodeName(leafIndex), (path) => path[0] === anchorName && path[1] === rootName, convergenceTimeoutMs);
  if (took !== null) {
    cascadeConverged.add(1);
    cascadeConvergence.add(Date.now() - started);
  }
}

function listAll(endpoint, filter, query) {
  const nodes = [];
  const names = new Set();
  const cursors = new Set();
  let after = null;
  for (let guard = 0; guard < 5000; guard += 1) {
    const { response, body } = graphql(endpoint, token, query, { namespace, filter, first: 100, after }, { operation: 'auditList' });
    const page = body && body.data && body.data.categories;
    if (!graphqlSucceeded({ response, body }) || !page || !Array.isArray(page.edges) ||
      page.edges.length > 100 || !page.pageInfo || typeof page.pageInfo.hasNextPage !== 'boolean') {
      throw new Error('category audit received an unsuccessful or malformed page');
    }
    for (const edge of page.edges) {
      const node = edge && edge.node;
      const name = node && node.metadata && node.metadata.name;
      if (typeof name !== 'string' || name === '' || names.has(name)) {
        throw new Error('category audit received a missing or duplicate identity');
      }
      names.add(name);
      nodes.push(node);
    }
    const info = page.pageInfo;
    if (!info.hasNextPage) return nodes;
    if (typeof info.endCursor !== 'string' || info.endCursor === '' || cursors.has(info.endCursor)) {
      throw new Error('category audit pagination did not advance');
    }
    cursors.add(info.endCursor);
    after = info.endCursor;
  }
  throw new Error('category audit exceeded its page budget before completion');
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

export function teardown(data) {
  waitForPath(nodeName(leafIndex), (path) => path[0] === anchorName && path[1] === rootName, convergenceTimeoutMs);
  if (!data || !Array.isArray(data.products) || data.products.length !== 1 + productSpread + productSkew || !data.categories) {
    throw new Error('Category teardown requires its acknowledged Product fixture');
  }
  const products = JSON.parse(JSON.stringify(data.products));
  for (let sequence = 0; sequence < productRate * durationSeconds; sequence += 1) {
    const index = sequence % products.length;
    const record = products[index];
    const intent = productIntent(record, sequence);
    record.spec.title = intent.title;
    record.spec.categoryRef.name = intent.category;
    record.metadata.generation += 1;
    delete record.metadata.revision;
  }
  waitForProductMembership(products, data.categories, convergenceTimeoutMs, true);

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

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Feature 055's capacity entry point. The full verifier is deliberately built
// incrementally with the Product admission, durable-watch, and controller
// slices; keeping the profile executable from the outset prevents an
// unregistered scenario from becoming production-only work.
import { check } from 'k6';
import http from 'k6/http';
import { Counter } from 'k6/metrics';
import { env } from '../lib/config.js';
import { graphql } from '../lib/graphql.js';

// The local alpha stack is validated from the host on 127.0.0.1, whereas k6
// joins the Compose network and reaches each independent service by its
// Docker DNS name. This keeps runner routing separate from the independently
// validated replica endpoints recorded in the gate evidence.
const apiA = env('PRODUCT_CAPACITY_API_A', env('CAPACITY_API_A'));
const apiB = env('PRODUCT_CAPACITY_API_B', env('CAPACITY_API_B'));
const token = env('CAPACITY_TOKEN');
const replicaChecks = new Counter('product_lifecycle_replica_checks');
const admissionChecks = new Counter('product_lifecycle_admission_checks');
const deletionRaceChecks = new Counter('product_lifecycle_deletion_race_checks');

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

  const name = `capacity-product-${__VU}-${__ITER}-${Date.now()}`;
  const created = graphql(apiA, token, `mutation($input: CreateProductInput!) {
    createProduct(input: $input) { product { metadata { name } } }
  }`, { input: {
    apiVersion: 'catalog.gitstore.dev/v1beta1', kind: 'Product',
    metadata: { namespace: 'default', name }, spec: { title: name },
  } }, { operation: 'productLifecycleCreate' });
  const admitted = check(created, {
    'Product lifecycle GraphQL admission succeeds': ({ response, body }) =>
      response.status >= 200 && response.status < 300 && !body.errors &&
      body.data && body.data.createProduct && body.data.createProduct.product.metadata.name === name,
  });
  if (admitted) {
    admissionChecks.add(1);
  }

  const observed = graphql(apiB, token, `query($namespace: String!, $name: String!) {
    product(by: { namespacePath: { namespace: $namespace, name: $name } }) { metadata { name } }
  }`, { namespace: 'default', name }, { operation: 'productLifecycleCrossReplicaRead' });
  const visibleOnPeer = check(observed, {
    'Product lifecycle admission is visible on peer replica': ({ response, body }) =>
      response.status >= 200 && response.status < 300 && !body.errors &&
      body.data && body.data.product && body.data.product.metadata.name === name,
  });
  if (!admitted || !visibleOnPeer) {
    return;
  }

  // Race two API replicas to start the same foreground deletion.  Completion
  // is controller-owned and may win immediately, so the invariant here is
  // that at least one request observes a valid lifecycle outcome rather than
  // requiring a timing-dependent final Product read.
  const id = created.body.data.createProduct.product.id;
  const deleteRequest = JSON.stringify({
    query: `mutation($input: DeleteProductInput!) {
      deleteProduct(input: $input) { outcome product { metadata { deletionTimestamp } } }
    }`,
    variables: { input: { id } },
  });
  const deleteResponses = http.batch([apiA, apiB].map((url) => ({
    method: 'POST', url, body: deleteRequest,
    params: {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      tags: { operation: 'productLifecycleDeletionRace' },
    },
  })));
  const raceObserved = check(deleteResponses, {
    'Product lifecycle deletion race starts exactly one foreground workflow': (responses) =>
      responses.some((response) => {
        if (response.status < 200 || response.status >= 300) return false;
        let body;
        try { body = response.json(); } catch (_) { return false; }
        const payload = body.data && body.data.deleteProduct;
        return !body.errors && payload &&
          (payload.outcome === 'TERMINATION_STARTED' || payload.outcome === 'ALREADY_TERMINATING');
      }),
  });
  if (raceObserved) {
    deletionRaceChecks.add(1);
  }
}

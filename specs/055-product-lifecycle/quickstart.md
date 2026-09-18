# Quickstart: Product Lifecycle Verification

Run the focused tests before and after each implementation slice, then execute
the aggregate gate before review.

```bash
make test
make build
make capacity TARGET=product PROFILE=lifecycle MODE=alpha
make pr-ready
```

## Contract checks

1. Authenticate as a Product author in a test namespace. Create and update a
   Product through Git push and GraphQL; assert equal admitted envelope, stable
   UID for update, Git provenance, and no direct datastore-only outcome.
2. Attempt GraphQL Product read/list/node/typed watch/generic watch and each
   mutation from an unauthorized subject. Assert no Product payload, existence
   detail, or cursor is disclosed.
3. Bootstrap a typed and generic Product watch, list, drain, replace the API
   replica, and resume from the opaque cursor. Assert all acknowledged
   Product create/spec/status/terminating/final-delete transitions appear and
   expiry/unavailability triggers re-bootstrap.
4. Create a resolved ProductVariant. Delete the Product through
   `DeleteProductInput { id: <product-id> }` and verify deletion is rejected.
   Remove it, request deletion, observe timestamp/finalizer/Terminating, and
   verify the Product controller completes only after a fresh blocker check.
   Attempt to create or newly resolve a variant to the terminating Product and
   verify rejection.
5. Run two controller replicas through Product updates, category reassignment,
   termination, replacement, and retry. Verify Product lifecycle state
   converges once and existing CategoryTaxonomy counts converge only for
   affected categories.
6. Retire a Product and verify its private management visibility remains;
   verify an active public snapshot is untouched. In the release-preparation
   contract test, an explicitly retired Product/child variant rejects the
   candidate.

## Capacity and recovery evidence

The `product/lifecycle` profile uses two API replicas and two controller
replicas with sustained Git-push and GraphQL mutation load, Product watch
subscribers, replay, deletion races, and replacement/materializer interruption.
Production mode enforces watch visibility p95 ≤1 second, p99 ≤3 seconds,
10,000-event replay p95 ≤5 seconds, recovery ≤30 seconds, and zero missing
acknowledged transitions or unsafe final deletion.

Document emitted metrics, traces, errors, rollback/migration order, and
operator recovery in Product lifecycle and watch runbooks before enabling the
durable Product watch fleet-wide.

## Latest local validation evidence

Recorded 2026-09-17 after the Product durable-watch recovery and Product CDC
migration changes:

- `make test-datastore-contracts` — passed.
- `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042` — passed
  against the local single-node Scylla Compose service.
- `make capacity TARGET=product PROFILE=lifecycle MODE=alpha` — passed; the
  two-replica domain verifier recorded `replicaChecks: 2` in
  `.gitstore/capacity/product/lifecycle/alpha/20260917T155447Z/`.
- `make pr-ready` — passed (and includes the aggregate build, lint, and test
  gates).

The full sustained Git-push/GraphQL/deletion-race verifier remains a separate
rollout task; do not treat this alpha evidence as authorization to enable the
durable Product-watch fleet by itself.

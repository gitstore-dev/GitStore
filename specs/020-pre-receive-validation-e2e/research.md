# Research: Pre-Receive Validation End-to-End (spec#020)

## R-001: Root Cause of CI Integration Test Failures

**Decision**: Add a CI readiness check for port 6000 (CatalogService gRPC) to the integration-test job before tests run.

**Rationale**: The `SchemaValidationHandler` and `AdmissionControlHandler` in `gitstore-git-service` both use `endpoint.connect_lazy()`, meaning the gRPC channel is dialled on first use rather than at construction time. As a result, the `async connect()` calls in `main.rs` resolve instantly and the noop fallback in the `Err` arm is only reached when the URI is malformed — not when the downstream service is unavailable.

The actual failure is an orchestration gap: the CI `integration-test` job waits for ports 4000, 5000, and 50051 but has no readiness check for port 6000 (the CatalogService gRPC endpoint). The git-service's `SchemaValidationHandler` sends its first `ValidateResources` RPC during the `git push` in the integration test. If port 6000 is not yet listening at that moment, the RPC fails with a transport error and the push is rejected with "validation service unavailable". This is a correct fail-closed behaviour for invalid-push tests but a false-positive rejection for the valid-push test. For the post-receive `AdmitResources` call, a transport error causes the fire-and-forget task to log an error and store no product, explaining "product not found" failures.

**Alternatives considered**:
- **Change `connect_lazy()` to `connect()` in the Rust service**: This would cause the service to fail to start if the API is unavailable, which is the wrong semantics for a dependency that may start later.
- **Add a startup probe in the Rust service**: The service already uses lazy connection; adding an explicit probe loop adds complexity without addressing the CI orchestration root cause.
- **Accept the flakiness**: Not acceptable — the integration test suite must be deterministic.

## R-002: ScyllaDB End-to-End Gap

**Decision**: Add a new `integration-test-scylla` CI job that uses the `compose.scylla.yml` overlay to run the full product-lifecycle test suite against a live ScyllaDB backend.

**Rationale**: The current `integration-test` job uses only `compose.yml`, which defaults to the `memdb` backend. The product lifecycle (push -> validation -> admission -> storage -> GraphQL query) has never been exercised against ScyllaDB in CI. The `compose.scylla.yml` overlay already handles all the wiring needed: it starts a ScyllaDB 5.4 node with developer-mode enabled, runs `scylla-init` to create the `gitstore` keyspace, and overrides the `api` service with `GITSTORE_DATASTORE__BACKEND=scylla`, `GITSTORE_DATASTORE__SCYLLA__HOSTS=scylla:9042`, and `GITSTORE_DATASTORE__SCYLLA__KEYSPACE=gitstore`. The datastore factory in `gitstore-api/internal/datastore/factory/factory.go` already dispatches on these env vars and runs migrations at startup. No application code changes are needed.

**Alternatives considered**:
- **Run ScyllaDB tests only in a nightly or scheduled job**: This would leave the ScyllaDB path untested on each PR, defeating the purpose of the e2e suite.
- **Use a ScyllaDB mock or test double**: ScyllaDB-specific CQL semantics (TTLs, partition layout, consistency levels) differ enough from memdb that a mock would not give confidence in the real driver.
- **Combine the memdb and ScyllaDB tests into one parameterised job**: Possible, but separating them keeps job logs and retry blast-radius independent and reflects the separate compose stacks.

## R-003: Rust Service Code Changes

**Decision**: No changes to `gitstore-git-service` Rust code are required.

**Rationale**: The `SchemaValidationHandler` and `AdmissionControlHandler` connect lazily and correctly propagate transport errors as fail-closed rejections during pre-receive and as logged errors during post-receive. The `repository_id` field passed as an empty string at startup is not used in the `ValidateResources` RPC implementation in `gitstore-api/internal/cataloggrpc/server.go` — validation is purely blob-content-based — so this is not a bug in scope for this spec. All observed CI failures are explained by the missing port-6000 readiness check, not by any defect in the Rust code.

## R-004: Go Service Code Changes

**Decision**: No changes to `gitstore-api` Go code are required.

**Rationale**: The `ValidateResources` and `AdmitResources` RPC implementations are correct. The `repository_id` field in `ValidateResources` is intentionally unused at this stage; validation is content-based. The datastore factory already handles both `memdb` and `scylla` backends via environment variable dispatch and runs ScyllaDB migrations automatically at startup. The GraphQL query path (`product(by: {namespacePath: ...})`) is exercised by the existing integration tests and requires no modifications.

## R-005: Integration Test Code Changes

**Decision**: No changes to `tests/integration/` test code are required; the existing test suite runs unchanged against both backends.

**Rationale**: The integration tests are backend-agnostic — they interact only with the HTTP git endpoint (port 5000) and the GraphQL API (port 4000), never directly with the datastore. All test helpers (`pushHelper.newPushHelper`, the GraphQL client, the namespace bootstrap) work identically regardless of whether the API is backed by memdb or ScyllaDB. The only preconditions are that the `gitci` namespace exists and the catalog repository is seeded with a README commit; these are already satisfied by the bootstrap step in the CI job.

## Summary

All fixes for this spec are in the CI workflow file (`.github/workflows/ci-integration.yml`). The service code and test code are correct; only the CI orchestration is missing the port-6000 readiness check and the ScyllaDB job.

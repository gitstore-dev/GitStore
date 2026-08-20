# Quickstart: Scylla Query and Recovery Hardening

## Test-first implementation order

1. **Envelope schema contracts**: add failing tests that compare the physical canonical superset, nullability, field names, and types across every authoritative manifest-backed table and verify full round-trip behavior.
2. **Namespace regression contracts**: verify direct UID/name lookup, complete envelope and Markdown body round-trip, and three-month forward/backward pagination remain correct.
3. **Repository read contracts**: add failing direct-UID, complete envelope and Markdown body round-trip, Namespace/month, global/month, and cross-bucket pagination tests; assert no fetch-all sorting behavior.
4. **Repository schema**: replace the sentinel Repository table and secondary indexes in the two-file alpha baseline; update row models and pagination.
5. **Uniqueness contracts**: add concurrent create tests for every unique name/UID/SKU/path reservation.
6. **Catalogue write recovery**: add failure injection after each projection step; replace logged batches with idempotent writes and compensation.
7. **Repository rename/transfer recovery**: add target-conflict, authoritative-update failure, stale-old-mapping, and retry convergence tests.
8. **Operational signals**: add log/metric assertions for projection failure, compensation failure, and dangling lookup detection.
9. **Runbook**: document large partitions, tombstones, repair cadence, compaction guardrails, projection audit, and conditional repair.
10. **Selector deferral**: update the collection materialization document with the evidence gate for issue #359; make no selector storage change.

## Focused validation

These are the reproducible validation commands for the feature.

```bash
cd gitstore-api
go test ./internal/datastore/... ./tests/contract/datastore/...
```

```bash
cd gitstore-api
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 \
  go test -tags scylla -count=1 -timeout 180s \
  ./tests/contract/datastore/... ./internal/datastore/scylla/...
```

```bash
cd tests/integration
go test -count=1 -timeout 240s \
  -run 'Test(NamespaceContract|RepositoryReadContract|RepositoryVersionContract)' ./...
```

## Issue-to-evidence traceability

| Issue | Scope | Automated test evidence | Documentation evidence | Reproducible command |
|---|---|---|---|---|
| [#353](https://github.com/gitstore-dev/GitStore/issues/353) | Time-bucket Namespace listing storage | Existing shared pagination suite in `gitstore-api/tests/contract/datastore/pagination_test.go`; Scylla shape checks in `gitstore-api/internal/datastore/scylla/namespace_model_test.go`; GraphQL coverage in `tests/integration/namespace_contract_test.go` | `specs/048-scylla-query-design/contracts/scylla-access-patterns.md`, `docs/resource-storage/README.md` | `make test-scylla-hardening`; tagged proof: `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042` |
| [#354](https://github.com/gitstore-dev/GitStore/issues/354) | Direct partition-key Namespace lookup | Existing backend-neutral envelope suite in `gitstore-api/tests/contract/datastore/contract_test.go`; memdb contracts in `gitstore-api/internal/datastore/memdb/namespace_contract_test.go`; Scylla schema/backend coverage in `namespace_model_test.go`, `migration_test.go`, and `backend_test.go`; GraphQL coverage in `tests/integration/namespace_contract_test.go` | `specs/048-scylla-query-design/contracts/scylla-access-patterns.md`, `specs/048-scylla-query-design/contracts/resource-storage-envelope.md` | `make test-scylla-hardening`; integration command shown above |
| [#355](https://github.com/gitstore-dev/GitStore/issues/355) | Repository lookup and bounded Namespace/global listing | Existing pagination/envelope suites in `gitstore-api/tests/contract/datastore/{pagination,contract}_test.go`; memdb and Scylla backend tests; `tests/integration/repository_read_contract_test.go` | `specs/048-scylla-query-design/contracts/scylla-access-patterns.md`, `docs/resource-storage/README.md`, `docs/architecture.md` | `make test-scylla-hardening`; `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042`; integration command shown above |
| [#356](https://github.com/gitstore-dev/GitStore/issues/356) | Remove cross-partition logged batches and recover partial writes | Executor failure tests in `gitstore-api/internal/datastore/scylla/failure_injection_test.go` and mutation-step coverage in `backend_recovery_test.go` | `specs/048-scylla-query-design/contracts/datastore-recovery.md` | `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042` |
| [#357](https://github.com/gitstore-dev/GitStore/issues/357) | LWT-backed create uniqueness | 100-trial backend-neutral coverage in `gitstore-api/tests/contract/datastore/hardening_test.go` and Scylla mutation coverage in `backend_recovery_test.go` | `specs/048-scylla-query-design/contracts/datastore-recovery.md` | `make test-scylla-hardening`; tagged proof: `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042` |
| [#358](https://github.com/gitstore-dev/GitStore/issues/358) | Recoverable Repository rename and transfer | Memdb mapping tests in `gitstore-api/internal/datastore/memdb/backend_test.go`, Scylla saga coverage in `repository_recovery_test.go`, and real Git/GraphQL coverage in `tests/integration/repository_version_contract_test.go` | `specs/048-scylla-query-design/contracts/datastore-recovery.md` | `make test-scylla-hardening`; tagged proof: `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042`; integration command shown above |
| [#360](https://github.com/gitstore-dev/GitStore/issues/360) | Tombstone, projection repair, and denormalized-index safeguards | Metric tests in `gitstore-api/internal/datastore/instrumented_test.go`, repair-service tests in `gitstore-api/internal/datastore/scylla/repair_test.go`, repair CLI tests in `gitstore-api/cmd/gitctl/main_test.go`, and env-gated capacity tests in `capacity_test.go` | `specs/048-scylla-query-design/contracts/operational-signals.md`; `docs/runbooks/scylla-projection-repair.md`; `docs/configuration.md`; `docs/developer-guide.md` | `cd gitstore-api && go test -count=1 -v -timeout 90s ./cmd/gitctl`; `make test-scylla-capacity SCYLLA_TEST_ADDR=127.0.0.1:9042` only with an intentionally configured capacity environment |

## Issue #359 deferral evidence

[#359](https://github.com/gitstore-dev/GitStore/issues/359) remains explicitly deferred:

- Feature 048 introduces no Product selector projection, materialized view, secondary/inverted index, external search integration, or new selector owner.
- `gitstore-api/internal/datastore/scylla/migration_test.go` contains `TestRunMigrations_DoesNotMaterializeProductLabelSelectors`, the migration-level guard for this scope boundary.
- `docs/implementation/035-collection-membership-materialization.md` records the future evidence gates: controller ownership; selector/cardinality distribution; Product label-update frequency; target latency/freshness SLO; write-amplification, storage, tombstone, compaction, and repair cost.
- Reconsideration must compare query-time CEL/selector scanning, a secondary or inverted label index, a materialized membership projection, and an external search/index service on the same representative dataset.
- The issue may proceed only after Product and Collection controllers exist, comparable benchmark evidence is attached to #359, and one owner demonstrates that the chosen alternative meets the declared SLO and operational budget.

## Repository readiness

```bash
make pr-ready
```

## Required evidence

- Concurrent uniqueness tests show exactly one winner.
- Failure-injection tests cover every mutation step.
- Cross-bucket pagination shows no missing or duplicate records.
- Namespace and Repository bodies round-trip unchanged through direct and paginated reads.
- Every authoritative resource table passes the shared envelope field/type contract.
- Query-shape tests confirm direct/bounded reads.
- Operational docs include thresholds and conditional repair validation.

## Operational and repair evidence

Unit-level audit planning, dry-run, apply, concurrent-writer protection, plan
validation, CLI confirmation, and stable JSON output:

```bash
cd gitstore-api
go test -count=1 \
  ./internal/datastore/scylla ./cmd/gitctl \
  -run 'Test(BuildRepairPlan|ProjectionRepairService|ValidateRepairPlan|ProjectionAudit|ProjectionRepair)'
```

Live Scylla audit and dry-run:

```bash
cd gitstore-api
GITSTORE_DATASTORE__SCYLLA__HOSTS=127.0.0.1:9042 \
  go run ./cmd/gitctl scylla-projection-audit
GITSTORE_DATASTORE__SCYLLA__HOSTS=127.0.0.1:9042 \
  go run ./cmd/gitctl scylla-projection-repair --dry-run
GITSTORE_DATASTORE__SCYLLA__HOSTS=127.0.0.1:9042 \
  go run ./cmd/gitctl scylla-projection-repair --confirm
```

Capacity, concurrency, partition, bounded-page, sustained-mutation, and
optional soak validation:

```bash
cd gitstore-api
SCYLLA_TEST_ADDR=127.0.0.1:9042 \
GITSTORE_SCYLLA_CAPACITY_RUN=1 \
GITSTORE_SCYLLA_CAPACITY_PRODUCTS=5000000 \
GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION=2h \
go test -tags scylla -run 'TestScyllaCapacity' -count=1 -timeout 3h \
  ./internal/datastore/scylla
```

Threshold evidence:

- hard partition ceiling: 100 MiB;
- hot-partition target: 10 MiB;
- default `gc_grace_seconds`: 10 days;
- required anti-entropy repair completion: within 7 days;
- alert on every compensation failure and sustained projection-finding growth;
- TWCS prohibited for update/delete-heavy projection tables.

**Evidence status (2026-08-19):**

- `make test-scylla-hardening`: passed.
- `make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042`: passed against the local ScyllaDB service.
- Repair plan/service and `gitctl` audit/repair tests: passed.
- Namespace and Repository GraphQL integration contracts: passed.
- `make pr-ready`: passed.
- Live CLI audit/apply against an intentionally inconsistent dataset was not run; operators must use the documented audit and dry-run procedure before a production repair.
- Five-million-resource capacity and soak evidence: pending a dedicated,
  preloaded capacity environment.

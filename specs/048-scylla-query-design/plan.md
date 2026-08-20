# Implementation Plan: Scylla Query and Recovery Hardening

**Branch**: `048-scylla-query-design` | **Date**: 2026-08-19 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/048-scylla-query-design/spec.md`

## Summary

Complete the ScyllaDB hardening initiative tracked by #352. Normalize every authoritative manifest-backed row to one canonical resource envelope, preserve the query-first Namespace projections already delivered for #353 and #354, replace Repository global/secondary-index access with direct and bounded query projections, persist the Markdown body for Namespace and Repository resources, replace cross-partition logged batches and read-before-write uniqueness checks with reservation-first LWTs plus deterministic idempotent projection writes, and make Repository rename/transfer recoverable through ordered conditional steps and compensation. Add structured consistency signals and an operator runbook for tombstones, repair cadence, large partitions, and dangling projections. Product label-selector redesign (#359) remains explicitly deferred.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); no Rust or frontend changes  
**Primary Dependencies**: Existing `gocqlx/v3 v3.0.4` + `gocql`, `go-memdb v1.3.5`, `go.uber.org/zap`, `prometheus/client_golang`; no new dependency  
**Storage**: ScyllaDB 5.x+ production datastore with query-specific denormalized tables; `go-memdb` development/test backend  
**Testing**: Backend-agnostic datastore contract tests, memdb unit tests, tagged Scylla integration tests, resolver/integration regression tests, Docker-based Scylla CI  
**Target Platform**: Linux production services and Darwin/Linux development environments  
**Project Type**: Multi-service repository; implementation is confined to the Go API datastore and operational documentation  
**Performance Goals**: Direct identity reads use one partition; page reads perform work proportional to requested page size plus fixed bucket overhead; no full Repository result sorting; keep Scylla partitions below 100 MB with a 10 MB soft target for hot partitions  
**Constraints**: No `ALLOW FILTERING`; no secondary index as a primary Namespace/Repository read path; no cross-partition logged batches; uniqueness must be deterministic under concurrency; no new service or message broker; preserve public GraphQL Relay `id` behavior while using `uid` internally and in storage; maintain the two-file alpha Scylla baseline; issue #359 remains deferred  
**Scale/Scope**: Up to 5,000,000 catalogue resources; Namespace and Repository control-plane data must remain bounded as tenants and creation history grow

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Pre-design evaluation |
|-----------|-----------------------|
| I. Test-First Development | PASS — contract, concurrency, failure-injection, and query-shape tests are defined before implementation tasks. |
| II. API-First Design | PASS — datastore recovery and operational-signal contracts are documented before backend changes; no public GraphQL change is required. |
| III. Clear Contracts & Versioning | PASS — public API behavior remains compatible; alpha-only storage layout changes are explicitly scoped. |
| IV. Production Observability & Debuggability | PASS — projection failures, compensation outcomes, dangling rows, large partitions, repair health, and datastore saturation receive structured signals. |
| V. User Story Driven Development | PASS — work maps directly to independently testable P1/P2/P3 scenarios in the specification. |
| VI. Independently Deployable Delivery | PASS — Namespace regression protection, Repository query design, write recovery, and operations can land as compatible ordered slices without coordinated service rollout. |
| VII. Simplicity with Proven Scale | PASS — reuses the existing datastore, logger, metrics, contract harness, and CLI/runbook surfaces while meeting bounded 5,000,000-resource access requirements. |
| VIII. Horizontally Replicable Core Services | PASS — correctness is stored in Scylla projections and conditional writes rather than process-local API state; retries are idempotent across replicas. |
| IX. Multi-User Authentication, Authorization & Isolation | PASS — datastore changes preserve existing caller and service boundaries and introduce no authorization bypass or shared-tenant scan. |
| X. Production Capacity, Backpressure & Load Validation | PASS — the design targets 5,000,000 resources, bounded partitions/pages, explicit concurrency behavior, and repeatable Scylla capacity validation. |

**Gate result**: PASS. No constitution violation requires exception tracking.

## Phase 0 Research Decisions

1. **One table per access pattern**: Repository UID lookup, Namespace-scoped listing, global listing, path lookup, and reverse mapping use explicit projections.
2. **Canonical resource vocabulary**: Persisted resource identity is `uid`, Repository references are `repository_id`, and `namespace` always contains the immutable Namespace name. A Namespace UUID is named `namespace_uid` only where one is explicitly required.
3. **Manifest body parity**: Namespace and Repository authoritative rows persist raw Markdown `body`; body changes advance generation and listing projections hydrate the authoritative row instead of duplicating large body content.
4. **Uniform authoritative envelope**: Product, ProductVariant, Collection, CategoryTaxonomy, Namespace, and Repository authoritative rows physically share one canonical API, metadata, lifecycle, audit, provenance, spec, body, and status superset with identical names and compatible types; inapplicable values remain null or empty.
5. **Bounded listing partitions**: Global Repository listing uses monthly buckets. Namespace-scoped Repository listing uses `(namespace, YYYY-MM)` buckets to avoid unbounded tenant partitions.
6. **Reservation-first uniqueness**: Name, UID, SKU, and path uniqueness use `INSERT ... IF NOT EXISTS`. A retry with the same identity is idempotent; a different identity receives `ErrAlreadyExists`.
7. **No cross-partition batch semantics**: Catalogue fan-out uses deterministic individual idempotent writes. Logged or unlogged batches are not used to imply atomicity across partitions.
8. **Compensation over a new repair service**: Mutations capture prior state, apply ordered steps, and compensate completed steps when a later step fails. Failed compensation is logged and metered for operator repair.
9. **Authoritative row last for deletes**: Query projections are removed first and the authoritative record last; failed intermediate deletes can be safely retried or reconstructed from the authoritative row.
10. **Repository rename/transfer as sagas**: Reserve target mapping, conditionally update the authoritative Repository, then conditionally remove the old mapping. Retry and compensation are keyed by stable Repository UID and expected resource version.
11. **Operational safeguards, not speculative automation**: Keep default general-purpose compaction unless measured evidence supports a table-specific change; document repair cadence, `gc_grace_seconds`, tombstone, large-partition, and dangling-row controls. No new controller or durable outbox is introduced.
12. **Product selectors remain deferred**: #359 produces only decision criteria and evidence requirements until Product and Collection controllers ship.

Detailed rationale and alternatives are recorded in [research.md](research.md).

## Project Structure

### Documentation (this feature)

```text
specs/048-scylla-query-design/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── datastore-recovery.md
│   ├── operational-signals.md
│   ├── resource-storage-envelope.md
│   └── scylla-access-patterns.md
├── checklists/
│   └── requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/datastore/
│   ├── datastore.go                    # backend-neutral behavior contracts
│   ├── instrumented.go                 # mutation/repair observations
│   ├── memdb/
│   │   ├── backend.go                  # atomic reference implementation
│   │   └── backend_test.go
│   └── scylla/
│       ├── backend.go                  # catalogue projection fan-out
│       ├── repository.go               # Repository projections and recovery
│       ├── models.go                   # table metadata
│       ├── pagination.go               # bounded cross-bucket pagination
│       ├── migrations/
│       │   ├── 001_initial_schema.cql  # final alpha table definitions
│       │   └── 002_secondary_indexes.cql
│       ├── backend_test.go
│       ├── migration_test.go
│       └── namespace_model_test.go
├── tests/contract/datastore/
│   ├── contract_test.go                # uniqueness and recovery behavior
│   └── pagination_test.go              # cross-bucket pagination

tests/integration/
├── namespace_contract_test.go
├── repository_read_contract_test.go
└── repository_version_contract_test.go

docs/runbooks/
└── scylla-projection-repair.md

docs/implementation/
└── 035-collection-membership-materialization.md
```

**Structure Decision**: Keep all mutation consistency logic behind the existing datastore abstraction. Memdb remains the atomic behavioral reference; Scylla implements the same contract through query projections, conditional reservations, ordered idempotent writes, and compensation. Operational guidance is documented in the existing runbook tree. No Git Service or controller-manager change is planned.

## Phase 1 Design

### Repository access patterns

- `repositories_by_uid`: authoritative full Repository row, partitioned by stable Repository UUID.
- `repositories_by_namespace`: Namespace/month partition, clustered by creation timestamp and Repository UUID for stable bidirectional pagination.
- `repositories_by_bucket`: global month partition, clustered by creation timestamp and Repository UUID.
- `namespace_mappings`: path reservation and lookup keyed by immutable Namespace name plus Repository name.
- `namespace_mappings_by_repository`: direct reverse lookup keyed by Repository UUID.
- Existing Namespace projections are normalized to `namespaces_by_uid` with `uid` columns.
- `namespaces_by_uid` and `repositories_by_uid` persist raw Markdown `body`; ordered listing projections remain narrow and hydrate each page entry by UID.
- Repository pages return `PageResult.TotalCount = -1` when an exact count would require scanning historical buckets.
- Remove Repository secondary indexes and the sentinel `bucket = "all"` storage path.

### Canonical authoritative envelope

Every full manifest-backed resource row uses:

```text
api_version        text
kind               text
namespace          text        # omitted only for cluster-scoped Namespace
uid                uuid
name               text
generation         bigint
resource_version   text
revision           text
creation_timestamp timestamp
creation_actor     text
update_timestamp   timestamp
update_actor       text
labels             map<text,text>
annotations        map<text,text>
owner_references   text        # canonical JSON
finalizers         list<text>
deletion_timestamp timestamp
repository_id      uuid
source_path        text
git_commit_sha     text
git_ref            text
spec               text        # canonical JSON
body               text
status             text        # canonical JSON
```

- Namespace and Repository add the missing envelope fields; Namespace omits only parent `namespace` and authoring `repository_id`.
- Collection and CategoryTaxonomy add `owner_references`.
- Existing `owner_refs` columns are renamed to `owner_references`.
- Fields without semantics for a resource kind are persisted as null or empty rather than renamed or silently dropped.
- Resource-specific query columns remain additive: for example `tier`, `title`, `default_branch`, `sku`, `parent_name`, and `ancestor_path`.
- Narrow `_by_name`, `_by_uid` pointer, mapping, and bucket projections intentionally store only access-pattern keys; complete API reads hydrate an authoritative row.

### Naming boundary

- GraphQL keeps Relay `id` fields and `...ID` inputs as encoded API identifiers.
- Datastore entities and Scylla columns use `UID`/`uid` for the resource-owned UUID.
- Datastore entities use `OwnerReferences`; Scylla uses `owner_references`.
- Repository foreign references use `RepositoryID`/`repository_id`.
- Repository ownership and path projections use `Namespace`/`namespace`, containing the immutable Namespace name.
- A Namespace UUID is exposed internally only as `NamespaceUID`/`namespace_uid` when a UUID relationship is unavoidable.

### Manifest body ownership

- The validator returns the raw Markdown body independently from parsed frontmatter.
- Namespace and Repository datastore entities persist `Body string`; Scylla stores it as `body text` on the authoritative `*_by_uid` row.
- Body content is author-owned and returned through the existing GraphQL `body` field.
- A body change advances generation; status-only changes do not alter body or generation.
- Listing projections store only bucket/order/UID data and hydrate authoritative rows, preventing Markdown duplication across time buckets.

### Catalogue mutation ordering

**Create**:
1. Reserve unique lookup keys with conditional inserts.
2. Write the authoritative row.
3. Write remaining query projections as idempotent individual statements.
4. On failure, compensate new projections and conditionally release reservations owned by the same resource identity.

**Update**:
1. Read and retain the prior authoritative state.
2. Apply the authoritative resource-version conditional update.
3. Apply projection changes idempotently.
4. On projection failure, retry and roll every projection forward from the committed authoritative state. If convergence cannot be completed, return `RepairRequired` and expose the committed authoritative version; do not roll projections back behind it.

**Delete**:
1. Read and retain the authoritative state.
2. Remove query projections and reservations conditionally.
3. Delete the authoritative row with the expected resource version.
4. On failure before the authoritative delete, rebuild removed projections from retained state.

### Repository rename/transfer ordering

1. Read the authoritative Repository and current mapping.
2. Conditionally reserve the target path for the stable Repository UID.
3. Conditionally update the authoritative Repository using the expected resource version.
4. Conditionally delete the old mapping only when it still points to the same Repository.
5. If step 3 fails, remove the target reservation. If step 4 fails, retain the correct target mapping, emit a stale-mapping signal, and allow idempotent retry to remove the old mapping.

### Operational safeguards

- Structured logs identify resource kind, identity, projection, operation, and compensation outcome.
- Metrics count projection-write failures, compensation attempts/failures, dangling lookup observations, and mutation latency.
- Runbook defines partition-size monitoring, tombstone thresholds, compaction review, repair cadence, `gc_grace_seconds` invariant, dangling-row audit, and manual reconciliation.
- Default `gc_grace_seconds` remains 10 days; production repair cadence must complete within 7 days.
- Default compaction remains unchanged until table-specific measurements justify LCS or another strategy. TWCS is prohibited for delete-heavy tables.

### Issue #359 decision gate

No Product selector table or search integration is introduced. The documentation records the measurements required after Product and Collection controllers ship: selector cardinality, update frequency, query latency, controller ownership, and comparison of inverted tables, controller-maintained projections, and external search.

## Phase 1 Contracts

- [scylla-access-patterns.md](contracts/scylla-access-patterns.md): required partition keys, clustering order, hydration, and pagination semantics.
- [resource-storage-envelope.md](contracts/resource-storage-envelope.md): canonical authoritative fields, types, nullability, and structural exceptions.
- [datastore-recovery.md](contracts/datastore-recovery.md): uniqueness, idempotency, compensation, error, and retry behavior.
- [operational-signals.md](contracts/operational-signals.md): structured log fields, metrics, thresholds, and runbook expectations.

## Post-Design Constitution Check

| Principle | Post-design evaluation |
|-----------|------------------------|
| Test-First | PASS — every state transition and failure point has a required failing contract test before implementation. |
| API-First | PASS — datastore and operational contracts precede code; GraphQL schema remains unchanged. |
| Clear Contracts | PASS — authoritative versus projection ownership and retry/error semantics are explicit. |
| Production Observability & Debuggability | PASS — no partial failure, dangling lookup, repair backlog, or saturation condition may be silently ignored. |
| User Story Driven | PASS — design slices map to US1 through US5. |
| Independently Deployable Delivery | PASS — #353/#354 regression tests, #355 read model, #356/#357 write model, #358 sagas, and #360 operations can be delivered as compatible slices. |
| Simplicity with Proven Scale | PASS — no new runtime service, broker, controller, or external datastore is introduced, while bounded query and repair behavior meets the declared scale. |
| Horizontal Replication | PASS — conditional reservations, optimistic concurrency, and idempotent projection convergence remain correct across API replicas. |
| Multi-User Security | PASS — Namespace-scoped access patterns preserve the existing pluggable AuthN/AuthZ enforcement boundary. |
| Production Capacity | PASS — bounded query shapes, partition targets, failure injection, and Scylla load evidence cover the declared production envelope. |

**Post-design gate result**: PASS.

## Complexity Tracking

No constitution violations or exceptional complexity are introduced.

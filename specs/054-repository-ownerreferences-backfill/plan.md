# Implementation Plan: Resource OwnerReferences and Deletion Semantics

**Branch**: `054-repository-ownerreferences-backfill` | **Date**: 2026-11-06 | **Spec**: [spec.md](./spec.md)
**Input**: [Resource OwnerReferences and Deletion Semantics](./spec.md)

## Summary

Complete and repair system-owned resource relationships for Namespace,
Repository, Product, ProductVariant, CategoryTaxonomy, Collection, File, and
ServiceAccount. Reuse the existing owner-reference model and reverse
projection, retain resource-specific deletion entry points, and add an
operator-invoked bounded `gitctl` backfill with durable progress and a
completion gate. A `true` reference blocks owner deletion; a `false` reference
allows owner deletion and leaves the dependent as an orphan. Neither value
cascades deletion. ServiceAccount remains a cluster-scoped root with empty
owner references.

## Technical Context

- **Language/Version**: Go 1.27.2 (`gitstore-api`, controller manager, and `gitctl`); no Rust change planned.
- **Primary Dependencies**: Existing gqlgen, gocqlx/Scylla, go-memdb, catalog admission, `OwnerReferenceStore`, and controller list/watch/cache/finalizer interfaces; no new dependencies.
- **Storage**: Existing authoritative resource tables and `owner_reference_dependents` projection. Add an incremental Scylla migration for durable backfill progress/completion; implement matching memdb contracts. Do not edit checksummed migration `001_infra.cql`.
- **Testing**: Go unit, API admission/resolver, backend-neutral datastore, Scylla datastore, controller integration, and race tests; repository lifecycle capacity and controller replacement evidence.
- **Target Platform**: Linux production services; macOS/Linux development.
- **Project Type**: Go API/datastore, operator CLI (`gitctl`), and existing controller-manager lifecycle paths.
- **Performance Goals**: Preserve indexed owner-dependent checks and existing capacity gates: for `repository/lifecycle` production, 60-minute sustained run, 1,000 subscribers, 10,000-event replay, visibility p95 ≤1 s and p99 ≤3 s, normalized API CPU <80%, API RSS growth <10%, and zero event loss. Backfill processes bounded pages and does not add a full-catalog scan to request paths.
- **Constraints**: System-managed references only; keep Git as canonical authored state; no cascades; preserve Namespace/Repository safety checks; metadata changes use UID/resource-version preconditions; backfill must be resumable and fail closed; no GraphQL schema shape change is expected. Clarify the existing `OwnerReference` schema documentation that incorrectly implies cascade deletion.
- **Scale/Scope**: Eight resource kinds, existing five-million-Product capacity dataset, all ownership edges in the spec matrix, repeated admission and deletion under sustained Git/GraphQL load.
- **Replica/Scaling Model**: API replicas share Scylla authoritative rows and reverse projections; writes use existing optimistic concurrency and idempotent projection synchronization. Backfill progress uses shared durable state with lease/fencing. Existing controller workflows remain duplicate-safe and restartable. Git remains exactly one active process, with retained storage and non-overlapping replacement.
- **Authentication/Authorization**: Preserve existing per-kind AuthN/AuthZ and namespace/repository isolation for all mutations and deletion. The operator backfill is an explicit `gitctl` administrative operation and must not expose credentials or authored content in output.
- **Load/Backpressure Model**: Use bounded per-kind pages and existing maximum owner-dependent page sizes; persist cursor only after safe page application. Bound retries, deadlines, and concurrency. Projection, lease, or completion-marker errors are explicit and fail closed.
- **Capacity Profile**: Extend `make capacity TARGET=repository PROFILE=lifecycle MODE=production` with an owner-reference/deletion correctness oracle. Reuse the supported two-API/two-controller, 60-minute, 1,000-subscriber/10,000-replay, retained five-million-Product scenario; no new public target/profile.
**Fault Profile**: `tests/chaos/profiles/controller-restart.json`, explicitly test-owned controller target, expected recovery ≤60 seconds. Add focused process-interruption/resume tests for the backfill command; the chaos profile does not by itself prove Scylla backfill checkpoint durability.

## Constitution Check

*GATE: Evaluated before Phase 0 research and re-checked after Phase 1 design.*

| Principle | Pre-design gate | Post-design gate |
|---|---|---|
| I. Test-First Development | **PASS** — contract, backend, admission, and race tests are planned before implementation. | **PASS** — datastore and API contracts define failure cases; integration tests cover backfill interruption, concurrent dependent creation, and deletion races. |
| II. API-First Design | **PASS** — ownership/deletion semantics are documented before handlers change. | **PASS** — the contract records the existing GraphQL metadata shape, internal reverse projection, errors, and rollout gate. |
| III. Clear Contracts & Versioning | **PASS** — no field shape changes expected; metadata semantics are clarified. | **PASS** — GraphQL type remains additive-compatible; Scylla progress state uses an incremental migration and supports staged rollout. |
| IV. Observability & Debuggability | **PASS** — backfill state and deletion errors must be observable. | **PASS** — expose per-kind progress, completion, unresolved relationships, and projection failures with bounded labels and no secret/resource-content logging. |
| V. User-Story Driven Development | **PASS** — metadata, deletion, and safe legacy upgrade are independently testable. | **PASS** — artifacts trace each story through explicit scenarios and success criteria. |
| VI. Independently Deployable Delivery | **PASS** — API/controller compatibility and the singleton Git boundary are explicit. | **PASS** — dual-write API rollout, old-writer drain, backfill, verification, then enforcement; no Git service change or overlapping Git process. |
| VII. Simplicity with Proven Scale | **PASS** — reuse the projection, lifecycle workflows, and `gitctl`. | **JUSTIFIED** — one durable progress/lease record is necessary for resumable bounded backfill across operator process/host replacement; a local-only cursor cannot safely establish fleet-wide completion. |
| VIII. Service-Specific Replica Safety | **PASS** — two API/controller replicas and rolling upgrades are required where applicable. | **PASS** — shared projection, version preconditions, idempotent workers, and durable cursor prevent process-local correctness assumptions. |
| IX. Multi-User Security & Isolation | **PASS** — per-kind authorization remains in force. | **PASS** — refs are server-managed; backfill is operator-only; namespace/repository scope is validated; reads and failures do not disclose secrets. |
| X. Capacity, Backpressure & Load Validation | **PASS** — five-million-Product scale and bounded access are in scope. | **PASS** — existing repository lifecycle production gates and a correctness oracle validate full-scale behavior; backfill and normal queries are bounded. |

**Gate result**: PASS. No constitution violation requires a waiver. The only
new durable state is the minimal progress/lease state required to meet the
resumable-backfill requirement at catalog scale.

## Rollout and Implementation Slices

1. **Contracts and write paths**: Add/repair all system-managed owner-reference
   producers and projection synchronization, including Repository ownership
   for File and ServiceAccount's root metadata projection. Keep current
   deletion guards active. Add contract tests first.
2. **Durable backfill**: Add progress persistence through an incremental
   migration, a bounded `gitctl` command, per-kind verification, and
   interruption/resume tests. Do not enable new indexed deletion decisions
   while old API writers remain.
3. **Deletion consistency**: Align blocking and orphan outcomes across Git and
   GraphQL entry points, retain per-kind finalizer/CAS work, and ensure the
   non-blocking Product/CategoryTaxonomy cleanup remains bounded and preserves
   authored references.
4. **Enforcement and evidence**: After old writers drain and backfill completes,
   enable verified index-backed deletion semantics. Extend repository lifecycle
   capacity correctness checks and run the existing controller-restart fault
   profile. Update the shared schema documentation without changing the
   `OwnerReference` field shape, then regenerate embedded API schema sources
   with `go generate ./...` in `gitstore-api`.

## Project Structure

### Documentation

```text
specs/054-repository-ownerreferences-backfill/
    plan.md
    research.md
    data-model.md
    quickstart.md
    contracts/owner-reference-lifecycle.md
    tasks.md
```

### Source Code

```text
shared/schemas/admin/
    schema.graphqls                         # clarify ObjectMeta/owner terminology; retain field shape
gitstore-api/
    internal/catalog/status.go              # common owner-reference rules
    internal/cataloggrpc/server.go          # admission ownership and deletion parity
    internal/datastore/
        datastore.go                        # bounded backfill/progress contracts
        entities.go                         # resource envelope coverage
        memdb/                               # transactional projection and test progress state
        scylla/
            migrations/                      # incremental durable backfill state only
            owner_references.go              # projection and completion-safe checks
    cmd/gitctl/                              # operator backfill command
    internal/admin/graph/resolver/           # GraphQL lifecycle entry-point parity
gitstore-controller-manager/
    internal/categorytaxonomy/               # bounded non-blocking Product orphan handling
tests/
    contract/                                # backend-neutral ownership contract
    integration/                             # backfill, replica, deletion and recovery tests
    integration/repository_lifecycle_capacity_test.go
```

**Structure Decision**: Keep implementation within existing API, datastore,
`gitctl`, controller, and integration-test components. The Git service remains
unchanged. No new service, standalone command, broker, generic lifecycle
framework, public Make target, or second owner-reference projection is added.

## Phase 0: Research

Completed in [research.md](./research.md). Existing GraphQL owner-reference
shape, datastore projection, memdb/Scylla behaviors, ServiceAccount projection,
finalizer patterns, migration constraints, and supported evidence paths were
verified. No unresolved technical clarifications remain.

## Phase 1: Design

Completed:

- [data-model.md](./data-model.md) defines resource identities, all ownership
  edges, deletion transitions, and resumable backfill state.
- [contracts/owner-reference-lifecycle.md](./contracts/owner-reference-lifecycle.md)
  defines external metadata semantics, deletion outcomes, concurrency, and
  rollout.
- [quickstart.md](./quickstart.md) defines the datastore, API/controller,
  backfill, capacity, and recovery acceptance path.

### Post-Design Constitution Recheck

All gates remain **PASS**. Design retains only the existing reverse projection
and resource-specific deletion patterns. The durable completion gate prevents
an incomplete migration from being treated as an empty dependency set. Backfill
pages and cleanup work are bounded; API/controller behavior relies on shared
state and compare-and-set, while Git remains singleton-only.

## Phase 2: Implementation Planning

Task decomposition is deferred to `/speckit-tasks`. Implementation must begin
with failing contract tests, then proceed through the rollout slices above.
**ADR status**: ADRs 0003–0006 are already Accepted. This feature implements
only the resource-owner metadata slice of Collection and File lifecycle, not
the core decisions of Proposed ADRs 0007 and 0008 (Collection reconciliation
and File deletion safety remain incomplete). Do not flip those ADRs or their
index in this feature; if implementation scope expands to ship an ADR's core
decision, add its dated status flip and index update to the implementing task
and PR.

## Complexity Tracking

| Added complexity | Why needed | Simpler alternative rejected because |
|---|---|---|
| Durable per-kind backfill cursor, lease/fencing, and completion state | Required to resume safely across process/host interruption and to gate indexed deletion until coverage is verified. | A local checkpoint or uncheckpointed scan cannot safely prove complete coverage at five-million-Product scale or coordinate concurrent operators. |

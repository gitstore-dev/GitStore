# Implementation Plan: Uniform Query Surface

**Branch**: `codex/073-uniform-query-surface` | **Date**: 2026-11-21 | **Spec**: [spec.md](spec.md)

**Planning status**: Provisional across API surfaces; recommended to finalize after the first substantive Storefront contract.

**Input**: `specs/073-uniform-query-surface/spec.md`; source baseline `57459dc5`.

## Summary

Replace connection selection arguments with `query`, retaining first/after, last/before and existing namespace/parent boundaries. One bounded parser, typed registry and whole-plan compiler supply execution and spec 072 discovery. Advanced typed queries and generic SearchItemConnection with SearchItem resource-union nodes use the same search/recommendation provider capabilities. Watches compile only predicates supported by durable journal facts and never invoke those providers.

Deliver one breaking schema cutover without deprecated aliases. No new resource metadata/status contract. These are implementation decisions, not claims of deployed providers or successful qualification.

## Technical Context

**Language/Version**: Go 1.27.2 from `go.work` and API/controller manifests. Rust Git service unchanged.

**Primary Dependencies**: Existing gqlgen v0.17.90, gqlparser v2.5.58 (API), gocql v1.17.0, gocqlx/v3 v3.0.4, memdb v1.3.5, Viper and shared observability. Small recursive-descent parser and standard Go HTTP provider client; no new parser framework, public module or query service.

**Storage**: Existing authoritative datastore, category projections and CDC journal. New forward migration for shared ranked-window TTL records/quota slots and implemented label postings. Never rewrite applied migrations. Provider indexes are derived state; results never go to Git.

**Testing**: Grammar/normalization and semantic fixtures, gqlparser schema/operation validation, backend datastore contracts, controller list/watch tests, bounded provider HTTP fixtures and real first-adapter integration, existing capacity/chaos families. Tests fail before runtime implementation.

**Target Platform**: Existing Linux API/controller deployments and development memdb.

**Project Type**: Existing GraphQL API, datastore adapters and controller clients.

**Performance Goals**: Spec PR-003–007: 30 minutes, 100 structured reads/s, 25 fresh ranked-window reads/s across 16 authorized test principals, 2,500 watches, 20 admitted changes/s, >=5M products/10K categories. Continuation/backward reads are separately attributed additional probes. At 600-second retention the steady-state fresh-window population is 15,000, below the 20,000 global bound and about 938/principal, below 2,048. Structured p95/p99 <=250 ms/1 s; ranked <=750 ms/2 s; change visibility <=1 s/3 s. Unexpected healthy errors <1%. Recommendation serving retains #29's stricter p95 <200 ms when that owning feature is claimed. No dropped work counts as success.

**Constraints**: [Grammar](contracts/query-language.md), [execution](contracts/execution.md), no scans, no live aggregates, existing diagnostics/envelopes. Scylla FTS is not advertised merely because installed.

**Scale/Scope**: Shared query/planner/provider infrastructure with separate Admin and Storefront exposure. Inventory existing Admin connections/nested relationships/cluster RBAC and management watches; align Storefront search/recommendation serving with its first substantive published-read contract. Generic search uses API-local resource union members. No automatic publication, importing Admin resource types into Storefront, public management watches, CRD platform or four-provider implementation. See [API surfaces](contracts/api-surfaces.md).

**Replica/Scaling Model**: >=2 APIs/>=2 controllers. Shared versioned cursor keys; shared Scylla windows/quota slots; index/provider revision binding. Retain per-replica controller disk checkpoints/pending work. Git remains one active process with retained storage and non-overlapping replacement.

**Authentication/Authorization**: Existing pluggable AuthN/AuthZ/UserDir enforces namespace, term and context permissions before execution. Reauthorize authoritative objects every page. No raw query/context telemetry or public provider credentials.

**Load/Backpressure Model**: Query <=8 KiB, 256 tokens, 128 nodes, depth 16, 64 predicates; <=4 sort keys; page 1–100/default 50. Window <=1,000 IDs/10-minute absolute TTL. Per API: 32 provider calls in flight/64 queued, <=16 hydration workers/request, <=64 index subqueries, <=1,000 authoritative point hydrations and <=4,096 examined candidates/request. Provider deadline 1 s within 2 s request deadline, at most one idempotent transient retry within it. Shared CAS quota slots bound windows to 2,048/principal/namespace and 20,000/deployment; expiry releases slots. Preserve stricter journal limits. Exhaustion fails explicitly; no silent truncation.

**Capacity Profile**: Extend `api/endpoint-split`, `namespace/watch`, `category/hierarchy` under existing Make selectors; new query probes are proposed. Qualification allocations: each API/controller 2 vCPU/2 GiB; one Git 4 vCPU/8 GiB; Scylla >=8 vCPU/32 GiB; first rich provider >=16 vCPU/64 GiB plus durable storage. Record actual disk/IOPS/network, index size and embedding dimensions; these are test allocations, not measured sizing advice. Query fixture <=1 KiB expression/<=32 KiB projected node. Retain separately attributed authoring/native-push traffic and stricter domain gates. No OOM/restart, RSS beyond allocation or steadily growing window retention. Change allocations only through reviewed evidence before production claims.

**Fault Profile**: Owned `api-restart`, `controller-restart`, `journal-interruption`; add bounded provider-outage support to existing harness. Recovery <=60 s after dependency readiness and existing stricter deadlines; overload <=30 s. Verify fixture ownership/receipts; preserve retained data. No live faults during planning.

## Constitution Check

Pre-design and post-design checks have the same disposition:

| Gate | Design and required evidence |
| --- | --- |
| I Test-first | Failing grammar, auth/cursor, provider-plan, watch/bootstrap and replica tests before implementation. |
| II Contract-first | Grammar, SDL overlay and execution/discovery/errors are defined first. |
| III Versioning | User-authorized exception to preferred deprecation; record a major interface change under release policy, no aliases. |
| IV Observability | Bounded parse/plan/provider/window/watch signals; private values excluded. |
| V Stories | Implementation slices map to the four spec stories. |
| VI Independent delivery | Drain incompatible API/client recipients at cutover; compatible new replicas may overlap. No claim of transparent old/new schema overlap. |
| VII Simplicity | One registry/parser; reuse datastore/journal; TTL windows only where needed for stable ranking. |
| VIII Replicas | Shared keys/windows/quotas and two-API continuation/replacement; two-controller recovery; singleton Git. |
| IX Isolation | Server-owned scope, term/context checks, current authorization on hydration. |
| X Capacity | Full retained 5M workload, bounded execution and sustained/fault evidence required. |

No non-negotiable principle is waived. ADR 0017 is already Accepted. This work does not implement the core App-installation decision in Proposed ADR 0019, CRD platform in 0016 or Collection controller status in 0007; keep their status. Implementation close-out MUST explicitly audit ADR applicability, flip any newly implemented core decision to `Accepted (YYYY-MM-DD)` and update the index in that implementing PR, and close spec 073 only after required work completes. Planning changes no ADR status.

## Project Structure

### Documentation (this feature)

```text
specs/073-uniform-query-surface/
  spec.md / plan.md / research.md / data-model.md / quickstart.md
  contracts/
    admin.graphqls / api-surfaces.md / query-language.md / execution.md / discovery.md / acceptance.md
  checklists/requirements.md
```

### Source Code (repository root)

```text
shared/schemas/admin/                    # management resource/connection contracts
shared/schemas/storefront/               # published representations; baseline contract dependency
gitstore-api/internal/query/             # proposed parser/registry/planner/providers
gitstore-api/internal/admin/graph/       # management schema/models/resolvers
gitstore-api/internal/storefront/        # independently owned serving boundary and read model
gitstore-api/internal/datastore/         # existing contracts + postings/windows
gitstore-api/internal/datastore/scylla/  # adapters and new migrations
gitstore-api/internal/watchjournal/      # preserve source/materializer/replay authority
gitstore-controller-manager/internal/listwatch/
gitstore-controller-manager/internal/checkpoint/
tests/integration/ / tests/capacity/ / tests/chaos/
docs/developer-guide.md / docs/implementation/
```

**Structure Decision**: API owns meaning. Controller sends expressions/stores fingerprints, without a second parser. Registry is authored once; generated discovery/schema checks plus semantic fixtures catch drift. Proposed package names above are destinations, not claims of existing packages.

## Phase 0 — Research

Completed against current sources; cross-API review pending the Storefront baseline: [research.md](research.md) resolves language ownership, indexed execution gaps, journal facts/bootstrap, provider pagination, heterogeneous search ordering, discovery integration and release compatibility.

## Phase 1 — Design

Completed against current sources; cross-API review pending the Storefront baseline: [data-model.md](data-model.md), [contracts](contracts/) and [quickstart.md](quickstart.md). Canonical AGENTS/CLAUDE context points to this plan. Admin overlay validation does not finalize Storefront behavior. Revisit endpoint-specific representations, trusted context, visibility, recommendation directive syntax and load fixtures after the first Storefront contract before implementation task generation.

## Phase 2 — Implementation sequence

0. **Storefront baseline / all stories**: Recommended sequencing gate: first authoritative Storefront contract, then endpoint-local SDL/resource members, publication-aware hydration and recommendation context/visibility tests. Do not copy the Admin overlay to Storefront.
1. **Foundations / all stories**: Inventory schema/consumers; failing schema/grammar/diagnostic/security fixtures; single registry/parser, authenticated cursors and whole-plan validation. Internal plumbing may land without publishing competing argument surfaces.
2. **US1**: Reuse indexed lists/category projections; implement selected bounded label postings, missing RBAC backward paging and backend parity. Repair or capability-gate scan/slice-backed nested paths.
3. **US3**: Compile durable identity/label predicates into existing transition projector; update controller operations/checkpoint query identity; retain bookmark-before-list. Test concurrent writes, predicate changes, expiry and replacement.
4. **US2**: Shared provider interface and first Qdrant adapter for actually configured indexes/modes; versioned ID-only index documents, scoped filtering, bounded projector/rebuild audit, revision checks, TTL windows/shared quotas. Recommendation serving is Storefront-only and gated on owning published-read/surface/context implementation; Admin exposes only its separately authorized management search subset. Meilisearch/Elasticsearch/Typesense are adapter targets, not mandatory integrations. Generic search returns one Relay SearchItemConnection with SearchItem resource-union nodes through the same provider path; multi-kind plans require a common stable order/window.
5. **US4**: Extend spec 072's owning search projection; generate schema/owned clients/docs together. One breaking cutover; drain old recipients before activating replacement clients. Rollback requires compatible client rollback and new-window invalidation, not input translation.
6. **Qualification / all stories**: Contributor checks, index build/audit, sustained/overload/fault evidence, release review and explicit ADR/spec lifecycle audit. Generate `tasks.md` separately.

## Complexity Tracking

| Exception or complexity | Why needed | Alternative rejected |
| --- | --- | --- |
| III preferred deprecation / VI old-new overlap | Explicit user breaking-cutover instruction; guarded major boundary and drain preserve safety. | Deprecated selectors/dual inputs violate scope. |
| Authenticated cursor | Current base64 position lacks query/scope binding. | Opaque-looking bytes are not integrity protection. |
| Shared windows/quota slots | Stable offset-provider ranking and cross-replica bounds. | Process-local caches/live offset replay break continuation. |
| Indexed label postings | Current namespace scan is ineligible. | Wrapping old helper violates scale; unsupported combinations fail. |

# Implementation Plan: CategoryTaxonomy Path Freshness, Git-Backed Mutations, and Descendant Filtering

**Branch**: `057-categorytaxonomy-path-freshness` | **Date**: 2026-10-07 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/057-categorytaxonomy-path-freshness/spec.md`

## Summary

This feature makes Git the only authoring path for categories, through the API as well as through pushes. It also makes the controller-maintained `status.resolved` hierarchy the only hierarchy clients see.

- **Mutations**: `createCategory`, `updateCategory` and a Git-backed `deleteCategory` render the manifest. Each mutation then runs these steps:
  1. Run the pre-receive checks in-process (a new `admission.ManifestValidator` wrapping `ValidateResources`).
  2. Commit through the existing Git writer.
  3. Admit the commit synchronously.
  4. Verify that the returned record came from its own commit.
- **Admission decisions**: category admission now returns its decision instead of logging it. Denials surface as `ADMISSION_REJECTED` errors with `phase: PRE_RECEIVE|POST_RECEIVE` and structured diagnostics. Warnings go in the top-level `extensions.admission`.
- **Error envelope**: every Git-backed mutation error uses one kind-neutral envelope (`code`, `diagnostics`, `phase`, `commit`). Namespace's `NAMESPACE_*` codes and its `phase`/`reason`/`reasons` keys migrate onto it.
- **Removed fields**: `Category.path`/`depth` are removed, with no fallback.
- **Ancestor index**: subtree queries (`categories(filter:)`, `children`) are served from a new closure index. `UpdateCategoryTaxonomyStatus` and final removal maintain it in the datastore layer. `gitctl scylla-projection-audit|repair` backfills and repairs it.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`); Rust 1.x (`gitstore-git-service`, golden-fixture parity test only)
**Primary Dependencies**: Existing only: gqlgen v0.17.90, gocqlx/gocql, go-memdb, `internal/gitclient` Git writer gRPC, `internal/admission`, `cataloggrpc`, zap, Prometheus. No new third-party dependency.
**Storage**: New Scylla projection table `category_ancestor_index` (migration `010`) plus a memdb table. Existing `category_taxonomies_by_namespace` is unchanged. Bare Git repositories hold the manifests.
**Testing**:
- `make test`: resolver, security, cataloggrpc and Rust unit tests.
- `make test TARGET=datastore [DATASTORE=scylla]`: backend-neutral contracts.
- Replica and rolling-upgrade tests in `cataloggrpc` and resolver packages, modelled on `namespace_commit_order_test.go`, `file_replica_test.go` and `file_rolling_upgrade_test.go`.
- `make capacity TARGET=category PROFILE=hierarchy`.

**Target Platform**: Linux containers (Compose/Kubernetes)
**Project Type**: Multi-service web backend (GraphQL API, controller, Git service)
**Performance Goals**:
- Category mutation latency is p95 ≤ 750 ms and p99 ≤ 2 s at the sustained rate below. It is dominated by one commit and one admission.
- A filtered `categories` page (≤ 100) is p95 ≤ 150 ms and p99 ≤ 500 ms, including during a 10,000-descendant cascade.
- Cascade convergence for 10,000 descendants is within the existing controller rate limits. It is measured and reported, not gated beyond the existing controller objectives.

**Constraints**:
- No namespace scans or live aggregates on request paths (ADR 0017).
- Every index write is O(depth ≤ 128).
- A mutation never batches manifests.
- Mutations add no queue.

**Scale/Scope**:
- Up to 100,000 categories per namespace, at depth ≤ 128 (typically ≤ 6).
- Product scale (5,000,000) is unaffected, because nothing on Product read paths changes.
- Sustained 20 category mutations/s per namespace, concurrent with Git pushes to the same `gitstore-system` repository. Those writes are serialized by the Git service per repository.

**Replica/Scaling Model**:
- API replicas are stateless for this feature. Mutation correctness relies on the Git service's per-repository serialized ref update, the ref-head/superseded check and the own-commit SHA check.
- Index writes are idempotent and versioned by `resource_version`.
- Controllers are unchanged: they keep the existing duplicate-safe cascade.
- Rolling upgrade: old API replicas don't serve the new fields or maintain the index. After the rollout, the operator runs `gitctl scylla-projection-audit` and then `scylla-projection-repair --confirm`, and filtered results are complete. memdb (development) needs no repair.

**Topology constraint**: Git service is stateful and singleton-only (one active
process per deployment). Sharding and placement-aware routing are not
implemented; separate volumes do not establish support. Do not introduce
multi-Git requirements unless implementing those capabilities is explicitly
approved feature scope. API/controller replica requirements are obligations to
verify, not blanket claims about current implementations.

**Authentication/Authorization**:
- The pluggable AuthN chain supplies the principal.
- The ADR-0010 actions `categoryTaxonomy.create|update|delete` are enforced in `middleware/security/graphql.go` before side effects.
- Update and delete scope comes from the stored record.
- The commit author is the principal subject.
- Diagnostics never reveal resources in other namespaces.
- `categoryTaxonomy.list` (unchanged) governs filtered lists.

**Load/Backpressure Model**:
- Mutations are bounded by the request deadline and the Git service's per-repository write lock.
- Fetching a page of index rows by UID uses bounded concurrency (≤ 16) and is capped by the page size (≤ 100).
- Index maintenance rides existing status writes, and so the controller's existing queue and rate limits.
- Repair is paged and rate-limited through the existing `gitctl` flags.

**Capacity Profile**: `tests/capacity/profiles/category-hierarchy.js`, with `preflight/category-hierarchy.sh` and `verifiers/category-hierarchy.sh`. It runs with two API and two controller replicas:
- build a 10,000-descendant subtree;
- sustain mutations concurrently with pushes;
- re-parent the root of the subtree.

Thresholds are the Performance Goals above. The verifier checks two things:
- every filtered result set equals a walk over `status.resolved.path` (SC-009);
- every successful mutation's record has its own commit SHA (SC-007).

Evidence goes in the standard capacity evidence bundle.

**Fault Profile**: reuses `tests/chaos/profiles/controller-restart.json` and `api-restart.json`, declared by the capacity profile. A controller restart mid-cascade must resume to full convergence. An API restart during index writes must be healed by the next status write, or appear in `scylla-projection-audit` and be fixed by repair. Steady state means zero audit findings after recovery.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle             | Status | Evidence                                                                                                                                                                                                              |
|-----------------------|--------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Test-First            | PASS   | Failing tests come before implementation for the contracts in `contracts/`, the datastore contract suite, the authorization matrix, the Go/Rust rejection-text golden fixture, and replica and rolling-upgrade tests. |
| API/Contract-First    | PASS   | `contracts/graphql.md`, `contracts/admission-diagnostics.md` and `contracts/datastore-ancestor-index.md` define the schema, errors, datastore and CQL.                                                                |
| Core-Service Boundary | PASS   | API: mutations, admission decision, index and diagnostics. Controller: the CategoryTaxonomy deletion client switches to `completeCategoryDeletion`; it still selects only `status.resolved {path depth}`. Git service: no behaviour change, only a golden-fixture parity test.     |
| Replica Safety        | PASS   | Own-commit SHA check and superseded handling. The index is idempotent and versioned. Rolling-upgrade gap is closed by repair. Unverified paths are named in the Verification list below. Git stays singleton.         |
| Multi-User Security   | PASS   | ADR-0010 actions, stored-resource scope, authenticated principal required, no cross-namespace disclosure in diagnostics.                                                                                              |
| Production Capacity   | PASS   | Category envelope declared. Product 5M paths untouched. Sustained mutation-plus-push load is in the capacity profile.                                                                                                 |
| Repeatable Evidence   | PASS   | `make capacity TARGET=category PROFILE=hierarchy` with a domain verifier, and `make chaos` profiles declared.                                                                                                         |
| Bounded Work          | PASS   | Depth ≤ 128, page ≤ 100, fetch concurrency ≤ 16, index writes O(depth), repair paged. No new queue.                                                                                                                   |
| Observability         | PASS   | Metrics and logs are listed below.                                                                                                                                                                                    |
| Incremental Delivery  | PASS   | Slices below deploy independently. API replicas may overlap. Git is never replaced.                                                                                                                                   |
| Simplicity            | PASS   | Reuses the Product, Namespace and repair patterns. One new table and two internal interfaces. The closure index is justified against the alternatives in ADR-0006 and R8.                                             |

**Observability additions**:
- `gitstore_category_mutation_total{operation,outcome}` and `gitstore_category_mutation_duration_seconds{operation}`.
- `gitstore_admission_rejections_total{kind,phase}`.
- `gitstore_category_ancestor_index_writes_total{result}` and `gitstore_category_ancestor_index_repair_required_total`.
- Filtered-list latency through the existing instrumented datastore op `ListCategoryDescendants`.
- Structured log fields: `commit`, `phase`, `diagnostic_count`.
- Push-path denials are logged at Warn and also recorded as a condition.

**Verification obligations (replica and rolling upgrade)**:
1. Two API replicas mutate the same category concurrently: at most one success per committed content, and the other gets `CONFLICT`.
2. A mutation and a push race on the same file.
3. Two replicas write status for the same category concurrently: the index converges to the higher resource version.
4. Old-replica status writes during a rollout: after repair, audit reports zero findings.
5. API restart between commit and admission: a retry is a no-op and returns the record.

**Post-design re-check**: PASS. There are no Complexity Tracking entries.

## Project Structure

### Documentation (this feature)

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    specs/
        057-categorytaxonomy-path-freshness/
            spec.md
            plan.md ## this file
            research.md ## Phase 0
            data-model.md ## Phase 1
            quickstart.md ## Phase 1
            contracts/ ## Phase 1
                graphql.md
                admission-diagnostics.md
                datastore-ancestor-index.md
            checklists/
                requirements.md
            tasks.md ## speckit-tasks (not yet created)
```

### Source Code (repository root)

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    shared/
        schemas/
            category.graphqls ## mutations, filter, payloads; path/depth removed
    gitstore-api/
        gqlgen.yml ## Category.parent/children resolver: true
        internal/
            admission/ ## Diagnostic, Error (shared envelope), FormatRejection, ManifestValidator, result NoOp/Warnings
            admissionreport/ ## per-request warning collector + AroundResponses extension (new package)
            cataloggrpc/
                server.go ## admitCategoryTaxonomyWithContext returns EntryDecision; ValidateManifest
                committed_manifest.go ## admission.Error, NoOp, CategoryTaxonomy delete
            namespace/
                decision.go ## Phase/Code constants removed
            graph/resolver/
                service.go ## CommitCategoryManifest, DeleteCategoryManifest, shared render/body/converge helpers
                category.resolvers.go ## create/update/delete, filtered list, parent, children
                converters.go ## drop Path/Depth
                namespace_error.go ## rebuilt on admission.Error (shared envelope)
            middleware/security/
                graphql.go ## categoryTaxonomy.create|update|delete
            datastore/
                datastore.go ## CategoryAncestorIndex interface, cursor
                instrumented.go ## forward new interface
                memdb/ ## schema + same-txn maintenance
                scylla/
                    migrations/010_category_ancestor_index.cql
                    backend.go ## status/delete maintenance via mutationExecutor
                    repair.go ## ancestor_index projection audit/repair
            app/
                server.go ## wire ManifestValidator + admission extension
        cmd/gitctl/ ## audit/repair include ancestor index
        tests/contract/datastore/ ## CategoryAncestorIndex suites
    gitstore-git-service/
        src/git/hooks/validation_handler.rs ## golden-fixture parity test only
    gitstore-admin/ ## grep and update path/depth and namespace error phase consumers
    tests/
        capacity/profiles/category-hierarchy.js
        capacity/preflight/category-hierarchy.sh
        capacity/verifiers/category-hierarchy.sh
        fixtures/admission-rejection-golden.json ## shared Go/Rust fixture
    scripts/run-capacity-target.sh ## category/hierarchy case
    config/policy.yaml ## grant categoryTaxonomy.create|update|delete
    docs/
        api-reference.md ## mutations, filter, error codes, extensions.admission
        categories/category-taxonomy-spec.md ## hierarchy via status.resolved; subtree filter
        ADRs/0006-category-taxonomy-lifecycle.md ## amended
```

**Structure Decision**:
- The existing three-service layout is kept.
- Runtime changes are in `gitstore-api`, plus the controller's deletion client switching to `completeCategoryDeletion`. The Git service only gains a parity test.
- One new internal package, `admissionreport`. The response extension is kind-neutral and will host the cost reporting planned in design 038, so it shouldn't live in `resolver`.

## Delivery Slices (independently deployable)

1. **Admission decision and diagnostics (foundation)**:
   - `admission` types, `EntryDecision`, `admission.Error`, NoOp;
   - push-path denial recorded as a condition;
   - the Go/Rust golden fixture;
   - the shared `admission.Error` envelope and the Namespace code migration.

   No schema additions except the Namespace error extensions.
2. **Remove `Category.path`/`depth`**: converters, regenerate, consumers and docs.
3. **Git-backed mutations**: `ManifestValidator`, `CommitCategoryManifest`/`DeleteCategoryManifest`, authorization, payload change, `extensions.admission` collector, policy grants. Adds the controller-only `completeCategoryDeletion` (deprecating the `updateCategoryStatus.completeDeletion` flag for one release) and switches the CategoryTaxonomy controller's deletion client to it.
4. **Ancestor index**: datastore interface, memdb, Scylla migration 010, maintenance in status and delete, contract suites, `gitctl` audit/repair.
5. **Filtered list, `parent`, `children`**: resolvers, `gqlgen.yml`, cursors, docs.
6. **Capacity and chaos evidence**: profile, preflight, verifier, run-script case, CLAUDE.md/AGENTS.md command list.

Slice 5 depends on 4, and slice 3 depends on 1; the others are independent. **Prerequisites (merged)**: #456 (controllers accept `CONFLICT` and `RESOURCE_VERSION_CONFLICT`; schema-validation guard for controller queries), #457 (fence always on, `NAMESPACE_REPOSITORY_FENCE_DISABLED` and `TransferRepository` removed) and #458 (configuration grouped by service; watch settings under `api.watch.journal`; `[push_limits]` ceiling). Slice 1 then switches the API to `CONFLICT`, and the `RESOURCE_VERSION_CONFLICT` mentions in the `update*Status` docstrings in `shared/schemas` are updated to match. After slice 4 ships to Scylla deployments, the release notes require `scylla-projection-audit` followed by `scylla-projection-repair --confirm`.

**ADR status**: the PR that completes slices 3–5 flips ADR-0006 from `Proposed` to `Accepted (<merge date>)`, following the ADR status convention. Media `fileRef` resolution (GH#244) stays listed as an open item.

## Follow-ups (outside this feature)

- File two GitHub issues when this ships:
  - Product admission denial reported as success; Product should adopt `EntryDecision` and the converge helper.
  - `Category.products` returns every product.
- `gitstore-dev/quickstart`: `config/policy.yaml` content changed (new actions), so flag the quickstart update in the PR.
- The typed `filter` is interim. A future `query` search syntax supersedes it, once the Qdrant search extension exists.

## Complexity Tracking

No violations.

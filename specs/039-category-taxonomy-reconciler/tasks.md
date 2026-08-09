# Tasks: CategoryTaxonomy Controller Reconciliation

**Input**: Design documents from `/specs/039-category-taxonomy-reconciler/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/reconciler-contract.md, quickstart.md

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and MUST fail before the corresponding implementation task begins.

**Organization**: Tasks are grouped by user story (US1 = hierarchy accuracy, US2 = condition trustworthiness, US3 = file-ref visibility) to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- File paths are exact, per plan.md's Project Structure section

## Path Conventions

Single existing Go module: `gitstore-controller-manager/`, with its established `internal/<package>` + `tests/{contract,integration}` convention (specs 025/026/036/040).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the GraphQL client dependency and minimal client package every subsequent phase needs. Nothing here is independently useful, but every other phase's tests depend on it existing.

- [x] T001 Added `github.com/gorilla/websocket v1.5.3` to `gitstore-controller-manager/go.mod` (research.md R1) via `go get` + `go mod tidy`
- [x] T002 [P] Unit tests for `graphqlclient.Client.Query`/`Mutate` in `gitstore-controller-manager/internal/graphqlclient/client_test.go` — written first, confirmed failing (package didn't exist), then passing. Uses plain `testing` (not testify), matching this module's existing convention — no testify usage found elsewhere in `gitstore-controller-manager`.
- [x] T003 [P] Unit tests for `graphqlclient.Client.Subscribe` (handshake, next-message streaming, `Stop()`, server-error-via-`Err()`) in the same file, against a `graphql-transport-ws` stub `httptest.Server` — written first, confirmed failing, then passing
- [x] T004 Implemented `graphqlclient.Client` (`Query`, `Mutate`, `Subscribe`, `Subscription` interface, `Error` type) in `gitstore-controller-manager/internal/graphqlclient/client.go`. All 8 tests pass, `-race` clean.

**Checkpoint**: A minimal GraphQL client exists and is tested in isolation. No reconciler-specific code yet.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The two concrete adapters (`CategoryTaxonomyListWatcher`, `graphqlStatusClient`) spec 040 deferred, satisfying the existing `listwatch.ListWatcher[T]`/`Watcher[T]` and `status.StatusClient` interfaces unchanged. Every user story's reconciler needs both to run against a real `gitstore-api`.

**⚠️ CRITICAL**: No user story implementation can begin until this phase is complete.

- [x] T005 [P] Unit tests for `CategoryTaxonomyListWatcher.List` (paginates the `categories` query to completion, returns the highest observed `resourceVersion`) against a stub `graphqlclient`, in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher_test.go` — written first, confirmed failing (`undefined: listwatch.NewCategoryTaxonomyListWatcher`), then passing
- [x] T006 [P] Unit tests for `CategoryTaxonomyListWatcher.Watch` (opens `watchCategories`, maps `ADDED`/`MODIFIED`/`DELETED` events, maps a `WATCH_EXPIRED`-extension error to `errors.Is(err, listwatch.ErrWatchExpired)`) against a stub `graphqlclient`, in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher_test.go`
- [x] T007 Implemented `CategoryTaxonomyListWatcher` in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go` per contracts/reconciler-contract.md. All 4 tests pass, `-race` clean.
- [x] T008 [P] Unit tests for `graphqlStatusClient.Apply` (issues `updateCategoryStatus`, maps a non-null `conflict` to `types.ErrConflict`, maps `NOT_FOUND` to `types.ErrNotFound`, maps `FORBIDDEN` to a plain wrapped error) against a stub `graphqlclient`, in `gitstore-controller-manager/internal/status/graphql_status_client_test.go` — written first, confirmed failing, then passing
- [x] T009 Implemented `graphqlStatusClient` in `gitstore-controller-manager/internal/status/graphql_status_client.go` per contracts/reconciler-contract.md. All 4 tests pass, `-race` clean.
- [x] T010 Added `ResolvedCategoryTaxonomy` (the JSON payload struct — `Depth`, `Path`, `ChildCount`, `ProductCount`, mirroring `catalog.ResolvedCategoryTaxonomy` field-for-field per data-model.md) to `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go` (new package)
- [x] T011 Added a `CategoryTaxonomy` cache-entity type (`UID`, `Namespace`, `Name`, `Generation`, `ResourceVersion`, `ParentRefName`, `Status status.ResourceStatus`, per data-model.md) to `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go` (depends on T010)

**Checkpoint**: Both client adapters exist, tested, and satisfy their existing interfaces unchanged. The reconciler's own types exist. User story implementation can now begin.

---

## Phase 3: User Story 1 — Category hierarchy status stays accurate after any change (Priority: P1) 🎯 MVP

**Goal**: The reconciler computes and writes correct `depth`/`path`/`childCount`/`productCount` for every `CategoryTaxonomy`, and propagates a path/depth change to every transitive descendant even when they weren't part of the triggering push.

**Independent Test**: Push a 3-level hierarchy (`electronics` → `computers` → `laptops`); verify each node's status shows correct `depth`/`path`. Reparent `computers` under a different root; verify `laptops`'s status (untouched by the reparenting push) updates to reflect the new ancestry without a follow-up push touching it directly.

### Tests for User Story 1 ⚠️

> Write these first; confirm they fail (no reconciler exists yet) before starting implementation below.

- [ ] T012 [P] [US1] Unit test: `computeHierarchy` returns `depth=0`/`path=[name]` for a root category, and `depth=2`/`path=[root,mid,leaf]` (root-to-self order) for a 3-level chain, walking a populated `cache.Cache[CategoryTaxonomy]`, in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy_test.go`
- [ ] T013 [P] [US1] Unit test: `computeHierarchy` for a category with 2 children and 3 matching products returns `childCount=2`/`productCount=3`; for a childless, productless category returns `0`/`0` (not omitted), in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy_test.go`
- [ ] T014 [P] [US1] Unit test: reconciling a category whose `parentRef` changed re-enqueues every direct child found in the cache (research.md R2's descendant-propagation mechanism), asserted via a fake `Enqueue` func capturing calls, in `gitstore-controller-manager/internal/categorytaxonomy/reconciler_test.go`
- [ ] T015 [P] [US1] Unit test: reconciling a deleted category's former child recomputes `path`/`depth` relative to its next-available ancestor (or promotes to root if the deleted category had no parent), in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy_test.go`
- [ ] T016 [P] [US1] Unit test: `Reconcile` is a no-op (returns `types.ResultOK()` without calling `client.Apply`) when the computed patch matches the currently observed status, including `path` array-equality (FR-013), in `gitstore-controller-manager/internal/categorytaxonomy/reconciler_test.go`
- [ ] T017 [P] [US1] Integration test (FR-015): push a depth-3 hierarchy through the existing git-admission pipeline against a running `gitstore-api`; assert `depth`/`path`/`childCount`/`productCount` correct at every level; reparent a middle node and assert its untouched descendant's status updates within the next reconciliation cycle, in `gitstore-controller-manager/tests/integration/categorytaxonomy_integration_test.go`

### Implementation for User Story 1

- [ ] T018 [US1] Implement `computeHierarchy` (depth/path walk to root via the cache, per research.md R2) in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy.go` (depends on T011-T015)
- [ ] T019 [US1] Implement `childCount`/`productCount` computation — linear cache scan for children; `productCount` via the `graphqlclient`'s `products` query filtered client-side by `categoryRef.name` (research.md R4) — in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy.go` (depends on T004, T018)
- [ ] T020 [US1] Implement `CategoryTaxonomyReconciler.Reconcile`'s hierarchy path: read from cache, compute via T018/T019, build `StatusPatch{Resolved: ...}`, `IsNoOp` short-circuit, `client.Apply`, descendant re-enqueue on a path/depth change, `ErrConflict` → `ResultTransient` — in `gitstore-controller-manager/internal/categorytaxonomy/reconciler.go` (depends on T007, T009, T016, T018, T019)
- [ ] T021 [US1] Wire `CategoryTaxonomyReconciler`, its `Cache[CategoryTaxonomy]`, and a `listwatch.Runner[CategoryTaxonomy]` (using T007's `ListWatcher` and the existing `checkpoint.FilesystemStore`) into `gitstore-controller-manager/cmd/controller/main.go`, per quickstart.md steps 1-3 (depends on T007, T009, T020)

**Checkpoint**: User Story 1 is independently functional — a controller reconciles `CategoryTaxonomy` hierarchy fields correctly, including cross-push descendant propagation, end-to-end against a real `gitstore-api`.

---

## Phase 4: User Story 2 — Parent-resolution and cycle conditions are trustworthy (Priority: P2)

**Goal**: `ParentResolved`, `Acyclic`, and `Ready` conditions accurately reflect parent-reference validity and cycle participation, and hierarchy fields are frozen (not corrupted) for cycle participants.

**Independent Test**: Push a category with an unresolvable `parentRef`; verify `ParentResolved=False`/`Ready=False`. Construct a two-node cycle across two pushes; verify both participants report `Acyclic=False` and neither's `path`/`depth` reflects a walk through the cycle.

### Tests for User Story 2 ⚠️

- [ ] T022 [P] [US2] Unit test: `ParentResolved=True` when `parentRef` is absent or resolves; `False` with a reason/message when it names a nonexistent category, in `gitstore-controller-manager/internal/categorytaxonomy/conditions_test.go` — write first, confirm failing
- [ ] T023 [P] [US2] Unit test: cycle detection (research.md R3's reimplemented DFS) correctly identifies a self-referencing category and a two-node A→B→A cycle as cycle participants, and correctly identifies an acyclic chain as not, in `gitstore-controller-manager/internal/categorytaxonomy/hierarchy_test.go`
- [ ] T024 [P] [US2] Unit test: `Acyclic=False` for every cycle participant; `path`/`depth` are left at their prior values (not recomputed, not reset) for cycle participants (FR-008); breaking the cycle on a later reconcile transitions `Acyclic` back to `True` and resumes normal recomputation, in `gitstore-controller-manager/internal/categorytaxonomy/conditions_test.go`
- [ ] T025 [P] [US2] Unit test: `Ready=True` only when `ParentResolved`, `Acyclic`, and the required-file-reference condition (US3, defaults to satisfied when no `optional: false` media exists) are all `True`; `Ready=False` if any is not, in `gitstore-controller-manager/internal/categorytaxonomy/conditions_test.go`
- [ ] T026 [P] [US2] Integration test (FR-015): construct a two-node cycle across two pushes against a running `gitstore-api`; assert `Acyclic=False` observable in status for both participants; break the cycle with a follow-up push and assert `Acyclic` transitions to `True`, in `gitstore-controller-manager/tests/integration/categorytaxonomy_integration_test.go`

### Implementation for User Story 2

- [ ] T027 [US2] Implement `computeParentResolved`/`computeAcyclic`/`computeReady` condition builders in `gitstore-controller-manager/internal/categorytaxonomy/conditions.go` (new file) (depends on T022-T025)
- [ ] T028 [US2] Wire the cycle-detection result from T023 into `computeHierarchy` (hierarchy.go, from Phase 3) so cycle participants skip path/depth recomputation per FR-008 (depends on T018, T023, T027)
- [ ] T029 [US2] Wire `computeParentResolved`/`computeAcyclic`/`computeReady` into `CategoryTaxonomyReconciler.Reconcile`'s condition-building step, merging with the file-ref condition placeholder from US3 (depends on T020, T027)

**Checkpoint**: User Stories 1 AND 2 both work independently and together — hierarchy fields are correct and frozen-on-cycle, conditions are trustworthy.

---

## Phase 5: User Story 3 — Missing required media is visible without blocking publication (Priority: P3)

**Goal**: A required (`optional: false`) media reference that cannot be confirmed to exist surfaces as an `Unknown`-status condition, never blocks the push, and an `optional: true` reference never raises a failing condition.

**Independent Test**: Push a category with an `optional: false` media entry naming a nonexistent file; verify the push succeeds and the status carries a condition indicating the reference could not be confirmed. Repeat with `optional: true`; verify no failing condition is raised.

### Tests for User Story 3 ⚠️

- [ ] T030 [P] [US3] Unit test: an `optional: false` media entry produces a required-file-reference condition with status `Unknown` (research.md R5 — `File`/#79 is not yet queryable, so the check cannot assert `True` or `False`); an `optional: true` entry produces no condition at all, in `gitstore-controller-manager/internal/categorytaxonomy/fileref_test.go` — write first, confirm failing
- [ ] T031 [P] [US3] Unit test: a category with zero media entries produces no required-file-reference condition, and `Ready` is unaffected by its absence, in `gitstore-controller-manager/internal/categorytaxonomy/fileref_test.go`
- [ ] T032 [P] [US3] Integration test (FR-015): push a category with `optional: false` media naming a missing file against a running `gitstore-api`; assert the push is accepted and the resulting status carries the `Unknown` condition; repeat with `optional: true` and assert no failing condition, in `gitstore-controller-manager/tests/integration/categorytaxonomy_integration_test.go`

### Implementation for User Story 3

- [ ] T033 [US3] Implement `computeFileRefCondition` (iterates `spec.media`, emits an `Unknown`-status condition per `optional: false` entry, no condition for `optional: true`) in `gitstore-controller-manager/internal/categorytaxonomy/fileref.go` (depends on T030, T031)
- [ ] T034 [US3] Wire `computeFileRefCondition` into `CategoryTaxonomyReconciler.Reconcile`'s condition-building step (replacing the US2 placeholder) and into `computeReady`'s inputs (depends on T029, T033)

**Checkpoint**: All three user stories are independently functional and integrated. The reconciler is feature-complete per the spec.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Observability (FR-016/SC-005) and validation spanning all three stories.

- [ ] T035 [P] Extend `docs/runbooks/controller-lag.md` with a "CategoryTaxonomy-specific notes" section distinguishing a cycle-blocked node (expected `Acyclic=False`, hierarchy fields intentionally frozen) from a genuinely stalled reconciler, per research.md R6
- [ ] T036 [P] Run `quickstart.md` end-to-end manually against a locally running `gitstore-api` + `gitstore-controller-manager`, confirming the wiring snippet in `cmd/controller/main.go` and the example GraphQL query both work as documented
- [ ] T037 Run `make pr-ready` (lint/build/test/license-check aggregate) and fix any findings
- [ ] T038 Run `graphify update .` to refresh the knowledge graph with the new `graphqlclient`/`categorytaxonomy` packages

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup (Phase 1) completion — BLOCKS all user stories. T007 depends on T004-T006; T009 depends on T004, T008; T010/T011 have no external dependency beyond the package existing.
- **User Story 1 (Phase 3)**: Depends on Foundational (Phase 2) completion.
- **User Story 2 (Phase 4)**: Depends on Foundational (Phase 2) AND on User Story 1's `computeHierarchy`/`Reconcile` skeleton existing (T018, T020) — condition logic and cycle-detection wiring extend the same functions US1 builds, so in practice US2 follows US1 in this single-reconciler package, though US2's *tests* (T022-T026) can be written in parallel with US1's from the start.
- **User Story 3 (Phase 5)**: Depends on User Story 2's condition-building wiring (T029) existing, since the file-ref condition is one more input to the same `Ready` computation. Its own tests (T030-T032) can be written independently at any time.
- **Polish (Phase 6)**: Depends on all three user stories being complete.

### User Story Dependencies

- **User Story 1 (P1)**: No dependency on US2/US3 for its own independent test — hierarchy computation and descendant propagation are meaningful and testable with no conditions logic at all.
- **User Story 2 (P2)**: Its condition-building code shares the same `Reconcile` method US1 establishes (a single reconciler, not separate ones per story) — this is a shared-implementation dependency, not a hidden coupling in the *behavior* being tested: US2's independent test (parent-resolution/cycle conditions) does not require US1's hierarchy fields to be correct, only present.
- **User Story 3 (P3)**: Builds on US2's condition aggregation (`Ready`) but is otherwise fully independent — the file-ref condition never depends on hierarchy or parent-resolution state.

### Within Each User Story

- Tests MUST be written and FAIL before implementation (constitution Principle I, non-negotiable)
- Hierarchy/condition computation functions before wiring them into `Reconcile`
- `Reconcile` wiring before `cmd/controller/main.go` registration
- Story complete (unit + integration tests passing) before moving to the next priority

### Parallel Opportunities

- T002/T003 (client unit tests) can be written in parallel — different test functions, same file, no shared state
- T005/T006 (ListWatcher tests) and T008 (StatusClient test) can be written in parallel — different files
- All US1 test tasks (T012-T017) marked [P] can be written in parallel
- All US2 test tasks (T022-T026) marked [P] can be written in parallel with each other, and in parallel with US1's tests (different files, no shared state) — though US2's *implementation* tasks (T027-T029) still wait on US1's implementation per the dependency above
- All US3 test tasks (T030-T032) marked [P] can be written in parallel with US1/US2's tests for the same reason

---

## Parallel Example: Phase 2 (Foundational)

```bash
# Launch the two adapters' test-writing in parallel (different files):
Task: "Unit tests for CategoryTaxonomyListWatcher.List in gitstore-controller-manager/internal/listwatch/graphql_listwatcher_test.go"
Task: "Unit tests for CategoryTaxonomyListWatcher.Watch in gitstore-controller-manager/internal/listwatch/graphql_listwatcher_test.go"
Task: "Unit tests for graphqlStatusClient.Apply in gitstore-controller-manager/internal/status/graphql_status_client_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (GraphQL client)
2. Complete Phase 2: Foundational (adapters + reconciler types) — CRITICAL, blocks all stories
3. Complete Phase 3: User Story 1 (hierarchy)
4. **STOP and VALIDATE**: Push a real 3-level hierarchy through git; confirm `depth`/`path`/`childCount`/`productCount` are correct and that a reparenting push updates untouched descendants. This alone is useful (spec's own "Why this priority" for US1: "the core value of the feature").
5. Add Phase 4 (US2 — conditions) once the MVP is validated
6. Add Phase 5 (US3 — file-ref) once US2 is validated
7. Complete Phase 6: Polish

### Incremental Delivery

1. Setup + Foundational → client and adapters ready, nothing reconciler-visible yet
2. User Story 1 → MVP: correct, self-propagating hierarchy fields → deployable and useful on its own
3. User Story 2 → trustworthy conditions layered on top → deployable
4. User Story 3 → file-ref visibility layered on top → deployable, feature-complete
5. Polish → runbook, validation, docs

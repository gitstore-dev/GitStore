# Tasks: Product Category and Readiness Reconciliation

**Input**: Design documents from `/specs/062-product-category-readiness/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/product-status-category-ref.graphqls, quickstart.md

**Tests**: Test-First Development (Constitution Principle I). Tests are written before implementation in each phase below.

**Organization**: Tasks are grouped by user story. Per research.md R8, the reconciler's resolution algorithm is **uniform** — the same code path serves US1/US2/US3, so most net-new production code lands in US1 (the MVP); US2 adds only the watch-driven re-enqueue; US3 adds no new controller code at all (R7), only regression coverage that the existing owner-reference sync + spec-055 decoupling flow now compose correctly.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: US1, US2, or US3

---

## Phase 1: Setup

- [X] T001 Confirm a clean starting point: run `make build` and `make test` from the repo root and record they pass before touching the schema or reconciler (this is a brownfield, additive-only feature — no new project/module init is needed).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Contract + shared data-model changes every user story's tests and implementation depend on.

- [X] T002 [P] Apply the schema additions in `specs/062-product-category-readiness/contracts/product-status-category-ref.graphqls` to `shared/schemas/product.graphqls`: add `observedGeneration: Int`, `lastAppliedRevision: String`, and `resolved: ResolvedProductStatusInput` to `input UpdateProductStatusInput` (currently only `name`/`namespace`/`resourceVersion`/`conditions`); add new `input ResolvedProductStatusInput { category: ResolvedCategoryRefInput }` and `input ResolvedCategoryRefInput { name: String!, uid: ID! }`; add `uid: ID` to `type ResolvedCategoryDefinition` (currently only `name`/`path`). All new fields nullable (rolling-upgrade safety per plan.md).
- [X] T003 [P] Add `UID string` (JSON tag `uid`) to `catalog.ResolvedCategoryDefinition` in `gitstore-api/internal/catalog/status.go` (currently `Name string; Path []string` only, ~line 87-90), matching T002's schema addition. No change needed to `catalog.ConditionType`'s `oneof` validator — `CategoryResolved` and `Ready` are already listed (status.go line 68).
- [X] T004 Regenerate gqlgen code: run `go generate ./...` from `gitstore-api/` (per `gitstore-api/generate.go`'s `//go:generate go tool gqlgen generate` directive against `gitstore-api/gqlgen.yml`). Fix any resulting compile errors in `gitstore-api/internal/graph/model/models_gen.go` and `gitstore-api/internal/graph/generated/`. Depends on T002.
- [X] T005 [P] Create `gitstore-controller-manager/internal/categorytaxonomy/product_index.go`: a `productCategoryIndex` type keyed by `(namespace, categoryName string)` mapping to a set of `types.WorkItemKey` (data-model.md's "CategoryTaxonomy reference match / Product category index"), with methods to add/remove a `(namespace, categoryName, key)` membership and to look up all keys for a `(namespace, categoryName)` pair. Guard internal state with a mutex — it is mutated from cache event-handler callbacks and read from a different cache's event handler.
- [X] T006 [P] Unit tests in `gitstore-controller-manager/internal/categorytaxonomy/product_index_test.go`: add/remove/lookup-miss, and a Product changing its `categoryRef` (remove old key, add new key) leaves no stale membership under the old name.

**Checkpoint**: Schema, catalog model, and the reverse index exist — user story work can begin.

---

## Phase 3: User Story 1 - Product becomes Ready once its category resolves (Priority: P1) 🎯 MVP

**Goal**: The uniform resolve algorithm (R8) runs on every Product reconcile, writes `CategoryResolved`/`Ready` conditions plus `resolved.category` via `updateProductStatus`, and synchronizes the Product's `CategoryTaxonomy` owner reference — end to end, so a Product referencing an already-existing category converges to `Ready=True` without further action.

**Independent Test**: Push a Product referencing an existing CategoryTaxonomy; poll until `Ready=True`; verify no other resource's status changed.

### Tests for User Story 1

- [X] T007 [P] [US1] Contract test in new `gitstore-api/internal/graph/resolver/product_status_contract_test.go`: `updateProductStatus` with `resolved.category = {name, uid}` set (a) merges `CategoryResolved=True`/`Ready=True` without disturbing `AdmissionAccepted` (`mergeProductConditions`, `product_status.go`), (b) applies `observedGeneration`/`lastAppliedRevision` when non-nil, (c) stores `resolved.category.uid` verbatim (it already arrives Relay-encoded — do not attempt to decode/re-encode it), (d) rejects a stale `resourceVersion` via the existing `statusConflictError` path (mirror `UpdateProductStatus`'s existing conflict handling in `product.resolvers.go`).
- [X] T008 [P] [US1] Contract test, same file: `resolved.category = nil` on an already-resolved Product clears `status.resolved.category` (FR-015) and clears the Product's `CategoryTaxonomy` OwnerReference.
- [X] T009 [P] [US1] Contract test, same file: `resolved.category = {name, uid}` on a Product with no prior owner reference adds a `CategoryTaxonomy`-kind OwnerReference matching the shape `resolvedCategoryOwnerReferences` produces for a fresh admission (`gitstore-api/internal/cataloggrpc/server.go:1877`) — same `blockOwnerDeletion: false`, same `RepositoryID` propagation.
- [X] T010 [P] [US1] Unit test in `gitstore-controller-manager/internal/product/reconciler_test.go`: a Product whose `CategoryRefName` matches an entry in the reconciler's CategoryTaxonomy cache (scoped to the Product's namespace) produces `CategoryResolved=True/CategoryFound`, `Ready=True/ProductReady`, and a `resolved.category` patch built from that cache entry's `Name`/`UID` verbatim (the cache's `UID` is already the Relay-encoded id — populated straight from the `metadata.uid` GraphQL field, see `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go:20` — no local encoding step exists or is needed in this process).
- [X] T011 [P] [US1] Unit test, same file: no matching CategoryTaxonomy cache entry (including an empty `CategoryRefName`) produces `CategoryResolved=False/CategoryNotFound`, `Ready=False/CategoryUnresolved`, `resolved.category=nil`.
- [X] T012 [P] [US1] Unit test, same file: a matching CategoryTaxonomy cache entry that has a non-nil `DeletionTimestamp` (or carries the foreground-deletion finalizer) is treated as not-found — same `CategoryResolved=False/CategoryNotFound` outcome as T011, not `CategoryFound` (research.md R8's `Terminating`-as-not-found rule; this is what makes US3's `CategoryDeleted`→`CategoryNotFound` convergence in T032 not flap back through `True`).
- [X] T013 [P] [US1] Unit test, same file: re-reconciling an already-`CategoryResolved=True` Product whose spec changed but `categoryRef` did not re-confirms the same condition status without advancing `lastTransitionTime` (FR-007) — mirror the `preserveTransitionTime`/`preserveLastTransitionTimes` pattern already tested in `repository/reconciler_test.go`/`categorytaxonomy/reconciler_test.go`.
- [X] T014 [P] [US1] Unit test, same file: `statusClient.Apply` returning a `types.ErrConflict`-wrapped error causes the reconciler to return `types.ResultAfter(...)`, not a terminal failure (PR-001; mirror `repository/reconciler.go`'s `conflictRequeueDelay` case).
- [X] T015 [P] [US1] Unit test, same file: a Product with a foreground-deletion finalizer set (or non-nil `DeletionTimestamp`) never has `CategoryResolved`/`Ready` computed or written — the existing deletion-completion path (current `product/reconciler.go`) still owns that branch unchanged (FR-008).
- [X] T016 [P] [US1] Unit test in new `gitstore-controller-manager/internal/status/graphql_product_status_client_test.go`, mirroring `graphql_repository_status_client_test.go`'s coverage: successful `updateProductStatus` apply, `NOT_FOUND` → `types.ErrNotFound`, `RESOURCE_VERSION_CONFLICT` → `types.ErrConflict`, and that a non-nil `patch.Resolved` is marshaled into the mutation's `resolved.category` input shape.

### Implementation for User Story 1

- [X] T017 [US1] Extend `gitstore-controller-manager/internal/product/reconciler.go`: change `Reconciler`/`NewReconciler` to also take a `cache.CacheAccessor[categorytaxonomy.CategoryTaxonomy]` and a `status.StatusClient`. In `Reconcile`, for a Product that is not entering deletion (existing branch on `hasFinalizer`/`DeletionTimestamp` stays first), look up `CategoryRefName` against the CategoryTaxonomy cache scoped to the Product's namespace (data-model.md's uniform algorithm steps 1-3) — **a matching entry counts as found only when its `DeletionTimestamp` is nil and it has no foreground-deletion finalizer; a `Terminating` match resolves as not-found** (T012, research.md R8) — build the `CategoryResolved`/`Ready` conditions and `resolved.category` JSON payload, preserve `lastTransitionTime` on no-change (mirror `preserveTransitionTime` from `repository/reconciler.go`), and call `statusClient.Apply` via a `status.StatusPatch` when `!patch.IsNoOp(current.Status)`. Map `types.ErrConflict` to `types.ResultAfter` per T014.
- [X] T018 [US1] Create `gitstore-controller-manager/internal/status/graphql_product_status_client.go`: `NewGraphQLProductStatusClient(client *graphqlclient.Client) StatusClient` issuing the `updateProductStatus` mutation, mirroring `graphql_repository_status_client.go` exactly (same `NOT_FOUND`/`RESOURCE_VERSION_CONFLICT` extension-code mapping), with its own `toUpdateProductStatusInput` that also serializes `patch.Resolved` (a marshaled `{category: {name, uid}}` payload) into the mutation's `resolved` input field.
- [X] T019 [US1] In `gitstore-api/internal/graph/resolver/product.resolvers.go`'s `UpdateProductStatus`: apply `input.ObservedGeneration`/`input.LastAppliedRevision` to `status` when non-nil (mirror the equivalent handling already present in `updateRepositoryStatus`'s resolver); when `input.Resolved != nil`, set `status.Resolved.Category` from `input.Resolved.Category` — nil clears it (FR-015), non-nil sets `catalog.ResolvedCategoryDefinition{Name: ..., UID: ...}` storing the incoming `uid` string as-is (per T007/T010, it already arrives Relay-encoded — do not decode it).
- [X] T020 [US1] Same resolver, after the status merge: synchronize `product.OwnerReferences` to match `input.Resolved.Category` (R7) — export `resolvedCategoryOwnerReferences`'s lookup-by-name logic from `gitstore-api/internal/cataloggrpc/server.go:1877` (or move it to a package both `cataloggrpc` and `graph/resolver` can import, e.g. `internal/catalog`) so the resolver can call it with `(ctx, product.Namespace, &catalog.ObjectReference{Name: input.Resolved.Category.Name}, false)` when `input.Resolved.Category != nil`, or the empty-owner-references result when nil — this reproduces admission's exact synthesis for a freshly-pushed Product, closing the gap for a Product admitted before its category existed.
- [X] T021 [US1] Rewire `gitstore-controller-manager/cmd/controller/main.go`: restructure `registerCategoryTaxonomy`/`registerProductWatch` so the `CategoryTaxonomy` cache (`catCache`) is visible to `productcontroller.NewReconciler`'s new cache-accessor parameter, and pass `status.NewGraphQLProductStatusClient(client)` as its new status-client parameter. Resolve this new Product→CategoryTaxonomy-cache dependency the same way the existing `productRunnerMu`/`productRunner` forward-reference already resolves the opposite direction (CategoryTaxonomy registration needing to reach the not-yet-created Product runner).
- [X] T022 [US1] Update `gitstore-controller-manager/internal/product/reconciler_test.go`'s existing deletion-completion test(s) for the new `NewReconciler` signature (pass a fake/empty CategoryTaxonomy cache and status client) and confirm they still pass unchanged.
- [X] T023 [US1] Integration test in new `tests/integration/product_category_readiness_test.go`: push a CategoryTaxonomy, create a Product referencing it, poll `status.conditions` until `CategoryResolved=True/CategoryFound` and `Ready=True`, and assert `status.resolved.category.uid` is non-empty and equal to that category's own `id` (quickstart.md §1); assert no other resource's status changed (spec.md US1 Independent Test).
- [X] T024 [US1] Integration test, same file: re-push the Product's spec without changing `categoryRef` and assert `CategoryResolved`'s `lastTransitionTime` is unchanged across the second reconcile, and the resolved category reference is unchanged (Acceptance Scenario US1-2, FR-007).

**Checkpoint**: User Story 1 fully functional — MVP deliverable.

---

## Phase 4: User Story 2 - Product waits for a category that doesn't exist yet, then converges (Priority: P2)

**Goal**: A Product pushed before its category exists is non-blocking (`CategoryResolved=False/CategoryNotFound`, retried at a bounded interval), and converges as soon as a matching CategoryTaxonomy is created or renamed in, driven by the reverse index (T005/T006) rather than a full Product-cache scan.

**Independent Test**: Push a Product referencing a not-yet-created category; verify `CategoryResolved=False/CategoryNotFound`; create the category; verify convergence to `Ready=True` without a new Product push.

### Tests for User Story 2

- [X] T025 [P] [US2] Unit test in `gitstore-controller-manager/internal/categorytaxonomy/products_test.go` (new or extended): the Product-cache event handler wired in `registerProductWatch` updates the `productCategoryIndex` (T005) on `OnAdd`/`OnUpdate` (categoryRef change: remove old key, add new key)/`OnDelete`, alongside the existing spec-042 count-fan-out enqueue — without replacing that existing behavior.
- [X] T026 [P] [US2] Unit test in new `gitstore-controller-manager/internal/categorytaxonomy/product_reenqueue_test.go`: a CategoryTaxonomy-cache `OnAdd` (or a rename `OnUpdate`) event handler looks up the `productCategoryIndex` by `(namespace, name)` — and, for a rename, also `(namespace, oldName)` — and calls an injected enqueue function for every matching `Product`-kind `types.WorkItemKey`, using a cache stub whose `List()` panics/fails to prove the handler never falls back to a full scan (PR-003/PR-004).
- [X] T027 [US2] Integration test in `tests/integration/product_category_readiness_test.go`: create a Product referencing a not-yet-existing category; assert `CategoryResolved=False/CategoryNotFound`, `Ready=False`; push the matching CategoryTaxonomy; assert the Product converges to `Ready=True` within the reconciliation window without a new Product push (quickstart.md §2, SC-002).

### Implementation for User Story 2

- [X] T028 [US2] Extend `gitstore-controller-manager/internal/categorytaxonomy/products.go`'s Product-cache event wiring (`NewProductCategoryEnqueueHandler` or a sibling handler registered alongside it in `registerProductWatch`) to also update the `productCategoryIndex` (T005) keyed by `(p.Namespace, p.CategoryRefName)` on add/update/delete.
- [X] T029 [US2] In `cmd/controller/main.go`'s `registerCategoryTaxonomy`, add a CategoryTaxonomy-cache event handler (alongside the existing `enqueueParent` handler already registered via `catCache.AddEventHandler`) that on `OnAdd` and on an `OnUpdate` where `Name` changed, looks up the `productCategoryIndex` for `(namespace, name)` — and for a rename also `(namespace, oldName)` — and calls `mgr.Enqueue` for each matching `Product`-kind `types.WorkItemKey` (FR-009).
- [X] T030 [US2] In `gitstore-controller-manager/internal/product/reconciler.go` (from T017), add a bounded requeue interval constant (e.g. `categoryUnresolvedRequeueDelay`, matching the `conflictRequeueDelay`/`categoryDeletionRetryInterval` convention already used in `repository/reconciler.go`/`categorytaxonomy/reconciler.go`) and return `types.ResultAfter(categoryUnresolvedRequeueDelay)` whenever the just-written condition is `CategoryResolved=False`, so resolution keeps retrying indefinitely as a fallback to T029's watch-driven path (FR-011, R6).

**Checkpoint**: User Stories 1 and 2 both work independently.

---

## Phase 5: User Story 3 - A previously resolved category disappears (Priority: P3)

**Goal**: Deleting a category that an already-`Ready` Product references flips that Product back to `CategoryResolved=False/Ready=False`, using the already-shipped spec-055 `DecoupleCategoryProducts` mechanism — which only now works correctly because T020 established the owner reference it depends on. **No new controller code** (R7).

**Independent Test**: Resolve a Product against a category, delete that category, verify the Product's status flips to `CategoryResolved=False/CategoryNotFound` while the category's own deletion completes per its own lifecycle rules.

- [X] T031 [P] [US3] Regression test proving R7's "no new controller code" claim: `DecoupleCategoryProducts` (`gitstore-api/internal/graph/resolver/service.go:540`) locates the Product via the owner reference T020 establishes and correctly sets `CategoryResolved=False/CategoryDeleted` on it — extend the existing category-deletion decoupling test coverage rather than adding new production code. This is the transient write; T032 verifies where it converges.
- [X] T032 [P] [US3] Integration test in `tests/integration/product_category_readiness_test.go`: resolve a Product against a category (as in T023), delete that category through its existing foreground-deletion flow, and assert the Product's status **converges to and stays at** `CategoryResolved=False/CategoryNotFound` (not the transient `CategoryDeleted` reason T031 confirms `DecoupleCategoryProducts` writes first — spec.md FR-010 and Acceptance Scenario US3-1 both require `CategoryNotFound` as the converged reason; T012/T017's `Terminating`-as-not-found rule is what prevents this from flapping back through `CategoryResolved=True` before the category is fully removed), `Ready=False`, while the category's own deletion completes via spec 055's `DecoupleCategoryProducts`/`updateCategoryStatus(decoupleProducts: true)` flow.

**Checkpoint**: All three user stories independently functional.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T033 [P] Update `docs/api-reference.md` for the `UpdateProductStatusInput`/`ResolvedProductStatusInput`/`ResolvedCategoryRefInput`/`ResolvedCategoryDefinition.uid` schema additions (per CLAUDE.md: update `docs/` after implementing a feature).
- [X] T034 Run `graphify update .` to refresh the knowledge graph after all code changes land, and commit `graphify-out/` in the same PR as the code (per project convention — regeneration must not be left dirty).
- [X] T035 Multi-replica validation (PR-001/PR-005): with two `gitstore-controller-manager` replicas reconciling the same Product concurrently (or a targeted test using two `Reconciler` instances against a shared fake API state), confirm exactly one `updateProductStatus` write wins per attempt and the losing replica retries against fresh state; confirm a replica restart mid-reconciliation still converges the Product on the next reconcile.
- [X] T036 Backpressure check (PR-004): verify (via existing `internal/health` queue/worker metrics, not a new capacity profile — per plan.md's Capacity Profile: N/A justification) that a burst of CategoryTaxonomy creates re-enqueuing many previously-unresolved Products does not starve unrelated Namespace/Repository/CategoryTaxonomy reconciliation.
- [X] T037 Run `specs/062-product-category-readiness/quickstart.md` end-to-end against `make compose`, confirming all three scenarios and the unrelated-state-untouched check (SC-003, CategoryTaxonomy product counts from spec 042 unaffected).
- [X] T038 Run `make pr-ready` before opening the PR.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies.
- **Foundational (Phase 2)**: Depends on Setup. Blocks all user stories — T017/T019 need T002-T004's schema; T028-T029 need T005-T006's index.
- **User Story 1 (Phase 3)**: Depends on Foundational. No dependency on US2/US3.
- **User Story 2 (Phase 4)**: Depends on Foundational. T028-T030 build on the reconciler T017 introduces in US1, but US2 is independently testable once US1's resolve path exists (a Product cannot "converge later" without first being able to resolve at all).
- **User Story 3 (Phase 5)**: Depends on Foundational and on T020 (owner-reference sync) from US1. Adds no controller code — pure verification.
- **Polish (Phase 6)**: Depends on all desired user stories being complete.

### Within Each User Story

- Tests (T007-T016, T025-T027, T031-T032) are written and confirmed failing before their corresponding implementation tasks.
- `product/reconciler.go` (T017) before its status client (T018) can be exercised end-to-end; both before `main.go` wiring (T021).
- Resolver status-merge (T019) before resolver owner-ref sync (T020) — same file, sequential.

### Parallel Opportunities

- T002, T003, T005 (different files) in parallel; T006 after T005.
- All US1 test tasks T007-T016 in parallel (different files/independent cases) before any US1 implementation task.
- T017 and T018 touch different files but T017's `Reconcile` calls into T018's client via the `status.StatusClient` interface it already depends on — implement T018 first or stub it; either order is safe since they're separate files.
- All US2 test tasks T025-T027 in parallel before US2 implementation.
- T031 and T032 (US3) in parallel — both test-only.

---

## Parallel Example: User Story 1 tests

```bash
Task: "Contract test for updateProductStatus resolved.category set in gitstore-api/internal/graph/resolver/product_status_contract_test.go"
Task: "Contract test for updateProductStatus resolved.category=nil clearing in gitstore-api/internal/graph/resolver/product_status_contract_test.go"
Task: "Unit test for reconciler resolve-found path in gitstore-controller-manager/internal/product/reconciler_test.go"
Task: "Unit test for reconciler resolve-not-found path in gitstore-controller-manager/internal/product/reconciler_test.go"
Task: "Unit test for graphqlProductStatusClient in gitstore-controller-manager/internal/status/graphql_product_status_client_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Phase 1 (Setup) → Phase 2 (Foundational: schema, catalog model, reverse index).
2. Phase 3 (User Story 1): resolver + reconciler + status client + owner-ref sync + main.go wiring.
3. **STOP and VALIDATE**: run T023-T024 integration tests independently; confirm quickstart.md §1 works against `make compose`.
4. This is deployable — an already-existing-category Product now reaches `Ready=True`.

### Incremental Delivery

1. Foundational → US1 (MVP: immediate-resolution convergence) → US2 (watch-driven convergence for out-of-order pushes) → US3 (verification that category deletion still degrades the Product correctly).
2. Each story adds value without breaking the previous one — US2 and US3 add no new schema and reuse US1's reconciler/resolver code paths per the uniform-algorithm decision (R8).

---

## Notes

- The reconciler's resolution algorithm is deliberately identical across all three stories (R8) — resist the temptation to special-case "why was this Product enqueued" inside `Reconcile`.
- A CategoryTaxonomy match only counts as "found" when it is not `Terminating` (T012/T017) — this is what makes the transient `CategoryDeleted` reason (T031) converge to `CategoryNotFound` (T032) instead of flapping back through `CategoryResolved=True`.
- `MediaResolved` and `CrossNamespaceRef` are explicitly out of scope (FR-013/FR-014) — do not add conditions or checks for either.
- The controller cannot itself Relay-encode a raw UUID (no access to `gitstore-api`'s internal node-ID scheme); every `uid` value the reconciler emits must already have arrived pre-encoded from a GraphQL response field.
- Commit after each task or logical group; stop at each checkpoint to validate a story independently before moving to the next.

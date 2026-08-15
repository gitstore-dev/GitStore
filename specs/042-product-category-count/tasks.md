# Tasks: Product Watch Transport for CategoryTaxonomy Count Reconciliation

**Input**: Design documents from `/specs/042-product-category-count/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/product-watch-contract.md, quickstart.md

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and confirmed failing first.

**Organization**: Tasks are grouped by user story per spec.md (US1: add, US2: delete, US3: reassignment, US4: restart-survival).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Maps to spec.md's US1/US2/US3/US4
- Paths are absolute-relative from repo root

## Path Conventions

Two existing Go modules, each with `internal/` + `tests/{contract,integration}`:
- `gitstore-api/` (schema in `shared/schemas/`, resolvers in `gitstore-api/internal/graph/resolver/`, admission in `gitstore-api/internal/cataloggrpc/`)
- `gitstore-controller-manager/` (`internal/listwatch/`, `internal/categorytaxonomy/`, `cmd/controller/main.go`)

---

## Phase 1: Setup

**Purpose**: Confirm a clean baseline before any change (mirrors spec 041's T001 pattern).

- [X] T001 Confirm `make build` and `make test` both pass on a clean checkout of `042-product-category-count` before any change, establishing the pre-change baseline.

---

## Phase 2: Foundational — `watchProducts` schema and event-publishing plumbing (BLOCKS all user stories)

**Purpose**: The dedicated `watchProducts`/`ProductWatchEvent` GraphQL contract (research.md R1/R4, contracts/product-watch-contract.md) and the `gitstore-api`-side event-publishing helper are load-bearing for every user story — no user story can be tested end-to-end without both existing first.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T002 Add `watchProducts(namespace: String, selector: LabelSelectorInput, resourceVersion: String): ProductWatchEvent!` to `extend type Subscription` and a new `ProductWatchEvent` type (fields: `type: WatchEventType!`, `namespace: String`, `name: String!`, `resourceVersion: String!`, `product: Product`) in `shared/schemas/product.graphqls`, mirroring `shared/schemas/category.graphqls`'s existing `watchCategories`/`CategoryWatchEvent` doc comments and field order exactly (contracts/product-watch-contract.md).
- [X] T003 Run `go generate ./...` in `gitstore-api` (per `gitstore-api/generate.go`'s `//go:generate go tool gqlgen generate` directive) to regenerate `internal/graph/generated/` and add the `WatchProducts` method stub to `generated.SubscriptionResolver`; commit the generated diff.
- [X] T004 [P] Add `publishProductEvent(evType eventbus.EventType, p *datastore.Product)` private helper to `gitstore-api/internal/cataloggrpc/server.go`, mirroring the existing `publishCategoryTaxonomyEvent` (same nil-`eventBus` guard, same `eventbus.Event{Kind: "Product", ...}` construction) per contracts/product-watch-contract.md.
- [X] T005 [P] Add `WatchProducts` resolver to `gitstore-api/internal/graph/resolver/product.resolvers.go`, mirroring `WatchCategories` in `category.resolvers.go` exactly: subscribe to `r.eventBus.Subscribe("Product", rv)`, map `WATCH_EXPIRED` the same way, and map each `eventbus.Event` to a `*model.ProductWatchEvent` using the existing `DatastoreProductToGraphQL` converter (`gitstore-api/internal/graph/resolver/converters.go:195`) for the `product` field (non-nil only for `Added`/`Modified`).

**Checkpoint**: `watchProducts` subscription is live end-to-end (schema → resolver → eventbus), but nothing publishes to it yet and nothing consumes it yet. Foundation ready for user story work.

---

## Phase 3: User Story 1 - Product count updates when a product is added to a category (Priority: P1) 🎯 MVP

**Goal**: A product created with a `categoryRef` causes that category's `productCount` to become correct without any push to the category itself.

**Independent Test**: Create a category with zero products, push one new product referencing it, confirm `productCount` becomes 1 without touching the category (spec.md US1).

### Tests for User Story 1 ⚠️ Write first, confirm failing

- [X] T006 [P] [US1] Add a test to `gitstore-api/internal/cataloggrpc/server_test.go` (alongside `TestAdmitResources_NewProduct_Created`) asserting `publishProductEvent`/`eventBus.Publish` is called with `Type: eventbus.Added`, `Kind: "Product"`, and the correct `Namespace`/`Name` when a new product with a `categoryRef` is admitted.
- [X] T007 [P] [US1] Add `gitstore-api/tests/contract/product_watch_test.go`: admit a product with a `categoryRef` via the gRPC admission path, subscribe to `watchProducts`, assert a `ProductWatchEvent` with `type: ADDED` and the expected `product.spec.categoryRef.name` arrives — following `gitstore-api/tests/contract/watch_status_test.go`'s existing pattern.
- [X] T008 [P] [US1] Add `gitstore-controller-manager/tests/contract/product_listwatcher_test.go`: `ProductListWatcher.List` against a stub GraphQL server returns the expected `Product` cache entities (`UID`, `Namespace`, `Name`, `ResourceVersion`, `CategoryRefName`) — following `listwatch_bootstrap_test.go`'s stub-server pattern.
- [X] T009 [US1] Add a test to `gitstore-controller-manager/internal/categorytaxonomy/products_test.go` (new or extended file) asserting the Product cache's `OnAdd` handler enqueues `WorkItemKey{Kind: "CategoryTaxonomy", Namespace: ..., Name: <categoryRef>}` when `CategoryRefName` is non-empty, and enqueues nothing when it is empty.
- [X] T010 [US1] Run T006-T009 and confirm all FAIL (no implementation exists yet).

### Implementation for User Story 1

- [X] T011 [US1] In `gitstore-api/internal/cataloggrpc/server.go`'s `admitProduct` create branch, call `s.publishProductEvent(eventbus.Added, p)` immediately after a successful `s.store.CreateProduct` (contracts/product-watch-contract.md call site 1).
- [X] T012 [P] [US1] Add `Product` cache entity type (`UID`, `Namespace`, `Name`, `ResourceVersion`, `CategoryRefName string`) to `gitstore-controller-manager/internal/categorytaxonomy/products.go` (data-model.md), alongside the existing `productsListQuery`/`NewProductCounter`.
- [X] T013 [P] [US1] Add `ProductListWatcher` to `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go`, satisfying `ListWatcher[Product]`/`Watcher[Product]`: `List` paginates the existing `products` query into `Product` cache entities (mirroring `CategoryTaxonomyListWatcher.List`'s pagination loop); `Watch` opens the new `watchProducts` subscription and maps each `ProductWatchEvent` to a `WatchEvent[Product]`, reading `product.spec.categoryRef.name` directly (no JSON unmarshal — research.md R4), and maps `WATCH_EXPIRED` to `listwatch.ErrWatchExpired`.
- [X] T014 [US1] In `gitstore-controller-manager/internal/categorytaxonomy/products.go`, add an `OnAdd` cache event handler (or a small exported constructor returning `cache.EventHandler[Product]`) that calls an injected `enqueueCategory(namespace, categoryName string)` func when `CategoryRefName != ""` (research.md R2, contracts/product-watch-contract.md).
- [X] T015 [US1] In `gitstore-controller-manager/cmd/controller/main.go`, add a `registerProductWatch` function (alongside `registerCategoryTaxonomy`): construct `productCache := cache.New[categorytaxonomy.Product]()`, a `ProductListWatcher`, a `Runner[categorytaxonomy.Product]` (own checkpoint key `"Product"`, no `mgr.Register` call — Product is watched, never reconciled, per research.md R1), register the `OnAdd` handler from T014 with `enqueueCategory` bound to `mgr.Enqueue` against `Kind: "CategoryTaxonomy"`, and start `productRunner.Run(ctx)` in a goroutine; call `registerProductWatch` from `main()` alongside the existing `registerCategoryTaxonomy` call.
- [X] T016 [US1] Run T006-T009 and confirm all now PASS. Run the full existing `cataloggrpc`, `graph/resolver`, `listwatch`, and `categorytaxonomy` test suites and confirm no regressions.

**Checkpoint**: Product-add → CategoryTaxonomy productCount convergence works end-to-end. This alone is a demonstrable, independently valuable slice (spec.md's stated MVP).

---

## Phase 4: User Story 2 - Product count updates when a product is removed (Priority: P1)

**Goal**: Deleting a product causes its former category's `productCount` to decrease, without touching the category.

**Independent Test**: Create a category with one product, delete the product, confirm `productCount` drops to 0 (spec.md US2).

### Tests for User Story 2 ⚠️ Write first, confirm failing

- [X] T017 [P] [US2] Add a test to `gitstore-api/internal/cataloggrpc/server_test.go` asserting `publishProductEvent` is called with `Type: eventbus.Deleted` and the deleted product's last-known `categoryRef`-derived `Namespace`/`Name` when a product is deleted via `deleteResource`.
- [X] T018 [P] [US2] Extend `gitstore-controller-manager/internal/categorytaxonomy/products_test.go` asserting the Product cache's `OnDelete` handler enqueues the deleted product's last-known category, and enqueues nothing when `CategoryRefName` was empty.
- [X] T019 [US2] Add an integration test to `gitstore-api/tests/contract/product_watch_test.go` (or a sibling): delete a product with a `categoryRef` via the admission path, subscribe to `watchProducts`, assert a `DELETED` event arrives with `product: null` and the correct `name`/`namespace`.
- [X] T020 [US2] Run T017-T019 and confirm all FAIL.

### Implementation for User Story 2

- [X] T021 [US2] In `gitstore-api/internal/cataloggrpc/server.go`'s `deleteResource`, alongside the existing `*datastore.CategoryTaxonomy` `Publish` call, add a `*datastore.Product` case that calls `s.publishProductEvent(eventbus.Deleted, r)` using the `*datastore.Product` returned by `lookupResourceByIdentity` before deletion (contracts/product-watch-contract.md call site 3).
- [X] T022 [US2] Add an `OnDelete` cache event handler in `gitstore-controller-manager/internal/categorytaxonomy/products.go` (alongside T014's `OnAdd`) that calls `enqueueCategory(p.Namespace, p.CategoryRefName)` when non-empty.
- [X] T023 [US2] Wire the `OnDelete` handler from T022 into the `productCache.AddEventHandler` call in `registerProductWatch` (`cmd/controller/main.go`, extending T015's registration).
- [X] T024 [US2] Run T017-T019 and confirm all now PASS. Confirm no regressions in the existing `deleteResource`/`TestAdmitResources_OperationAwareDeleteRemovesProductVariant`-family tests.

**Checkpoint**: Add and delete both converge `productCount` correctly. US1+US2 together are a complete lifecycle slice.

---

## Phase 5: User Story 3 - Product count updates on category reassignment (Priority: P1)

**Goal**: Changing a product's `categoryRef` decrements the old category's `productCount` and increments the new category's, and touches no third category.

**Independent Test**: Create two categories and one product under the first, move the product to the second, confirm both counts update and no other category is touched (spec.md US3).

### Tests for User Story 3 ⚠️ Write first, confirm failing

- [X] T025 [P] [US3] Add tests to `gitstore-api/internal/cataloggrpc/server_test.go`: (a) an update that changes `categoryRef` calls `publishProductEvent(eventbus.Modified, ...)`; (b) an update that changes only price/description (same `categoryRef`) does NOT call `publishProductEvent` (contracts/product-watch-contract.md call site 2).
- [X] T026 [P] [US3] Extend `gitstore-controller-manager/internal/categorytaxonomy/products_test.go`: `OnUpdate` enqueues both `old.CategoryRefName` and `current.CategoryRefName` when they differ (both non-empty); enqueues nothing when they are equal; enqueues only the non-empty side when one of them is empty (research.md R2).
- [X] T027 [US3] Add `gitstore-controller-manager/tests/integration/product_category_count_test.go`: end-to-end against a real `gitstore-api` — create two categories and a product under the first; reassign the product to the second; assert the first category's `productCount` decrements, the second's increments, and a third, untouched category's status/`productCount` and reconcile-count metric remain unchanged (FR-004/FR-010, SC-003).
- [X] T028 Run T025-T027 and confirm all FAIL.

### Implementation for User Story 3

- [X] T029 [US3] In `gitstore-api/internal/cataloggrpc/server.go`'s `admitProduct` update branch, inside the existing `changedSpecBody` handling, add a categoryRef-diff check: call `s.publishProductEvent(eventbus.Modified, existing)` only when `existing`'s parsed `CategoryRef.Name` differs from `resource.Spec.CategoryRef.Name` (contracts/product-watch-contract.md call site 2) — a spec change that is not a categoryRef change must still write the product but must not publish.
- [X] T030 [US3] Add the `OnUpdate` cache event handler in `gitstore-controller-manager/internal/categorytaxonomy/products.go` (alongside T014/T022): if `old.CategoryRefName != current.CategoryRefName`, call `enqueueCategory` for both sides (when non-empty) (research.md R2).
- [X] T031 [US3] Wire the `OnUpdate` handler from T030 into `registerProductWatch`'s `productCache.AddEventHandler` call (extending T015/T023's registration).
- [X] T032 [US3] Run T025-T027 and confirm all now PASS. Confirm no regressions in `TestAdmitResources_ExistingProduct_Updated` and the existing categorytaxonomy reconciler test suite (spec 039).

**Checkpoint**: All three P1 stories (add/delete/reassign) are complete and independently verified. This is spec.md's full P1 scope.

---

## Phase 6: User Story 4 - Convergence survives a controller restart (Priority: P2)

**Goal**: A product change made shortly before a `gitstore-controller-manager` restart is not lost — the affected category still converges after restart.

**Independent Test**: Push a product change, restart the controller-manager process before it's processed, confirm the affected category's `productCount` still converges after restart (spec.md US4).

### Tests for User Story 4 ⚠️ Write first, confirm failing

- [X] T033 [US4] Add a test to `gitstore-controller-manager/tests/integration/product_category_count_test.go` (extending T027), following `reconcile_retry_resume_test.go`'s existing restart-survival pattern: push a product create/reassignment, simulate a `Runner[Product]` restart (stop and re-`Run` against the same checkpoint store) before the event is processed, and assert the affected category still converges to the correct `productCount` after resume — with no manual re-trigger.
- [X] T034 Run T033 and confirm it FAILS (or passes only if T001-T032 already make it pass incidentally — if so, note in the PR description that restart-survival was inherited "for free" from `Runner[T]`'s existing checkpoint/resume logic per research.md R6, and skip to T035 without new implementation).

### Implementation for User Story 4

- [X] T035 [US4] If T034 revealed a gap: verify `registerProductWatch` (T015) passes a `checkpoint.FilesystemStore`-backed `Store` keyed distinctly from `CategoryTaxonomy`'s (research.md R6 — one checkpoint file per kind) to `Runner[Product]`; fix the checkpoint key if it collides with `CategoryTaxonomy`'s.
- [X] T036 [US4] Run T033 and confirm it now PASSES. Run the full `gitstore-controller-manager` integration suite and confirm no regressions.

**Checkpoint**: Restart-survival for the Product-driven trigger path is verified. All four user stories complete.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation and burst-collapse verification spanning all stories.

- [X] T037 [P] Add a test verifying SC-006 (burst collapse): enqueue the same `CategoryTaxonomy` key many times in rapid succession via simulated Product churn and assert the work queue holds at most one pending item for that key at any time (research.md R5) — extend `gitstore-controller-manager/tests/contract/manager_dispatch_test.go` or add alongside it.
- [X] T038 [P] Add a short "Product-driven fan-out" note to `docs/runbooks/controller-lag.md`'s `CategoryTaxonomy`-specific section, documenting that its queue depth can now also reflect Product-driven enqueues (plan.md Constitution Check, Principle IV).
- [X] T039 Run `make pr-ready` (per AGENTS.md's PR-readiness gate) and resolve any lint/build/test failures before this feature is considered done.
- [X] T040 Run through quickstart.md's "Verifying End-to-End" steps manually against a locally running `make api` + `make controller` to confirm documented behavior matches reality.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies.
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories (the `watchProducts` schema/resolver and `publishProductEvent` helper are shared prerequisites every story's tests exercise).
- **User Story 1 (Phase 3)**: Depends on Foundational only.
- **User Story 2 (Phase 4)**: Depends on Foundational; reuses T012-T015's cache/runner scaffolding from US1 (adds `OnDelete` to the same handler registration) — not independent of US1's *implementation artifacts*, though independently *testable* once US1 code exists.
- **User Story 3 (Phase 5)**: Same relationship as US2 — adds `OnUpdate` to the shared registration from US1.
- **User Story 4 (Phase 6)**: Depends on US1 (needs a real Product-driven enqueue path to test restart-survival against).
- **Polish (Phase 7)**: Depends on all four user stories.

### Within Each User Story

- Tests written and confirmed failing before implementation (Constitution Principle I).
- Cache entity / schema types before handlers before wiring in `main.go`.
- Story complete (tests passing, no regressions) before moving to the next priority.

### Parallel Opportunities

- T004/T005 (Phase 2) touch different files and can run in parallel.
- T006-T009 (US1 tests) touch different files and can run in parallel.
- T012/T013 (US1 impl: cache entity vs. ListWatcher) touch different files and can run in parallel.
- T017/T018 (US2 tests) and T025/T026 (US3 tests) can each run in parallel within their story.
- T037/T038 (Polish) can run in parallel.

---

## Parallel Example: User Story 1

```bash
# Tests (different files):
Task: "Add publish-on-create assertion to gitstore-api/internal/cataloggrpc/server_test.go"
Task: "Add gitstore-api/tests/contract/product_watch_test.go"
Task: "Add gitstore-controller-manager/tests/contract/product_listwatcher_test.go"

# Implementation (different files):
Task: "Add Product cache entity to gitstore-controller-manager/internal/categorytaxonomy/products.go"
Task: "Add ProductListWatcher to gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001).
2. Complete Phase 2: Foundational (T002-T005) — `watchProducts` contract live.
3. Complete Phase 3: User Story 1 (T006-T016).
4. **STOP and VALIDATE**: product-add → category `productCount` convergence works, independently of delete/reassign/restart.
5. Deploy/demo if ready — matches spec.md's stated MVP framing for US1.

### Incremental Delivery

1. Setup + Foundational → `watchProducts` contract ready.
2. Add US1 (add) → validate → demo (MVP).
3. Add US2 (delete) → validate → demo.
4. Add US3 (reassignment) → validate → demo — completes spec.md's full P1 scope.
5. Add US4 (restart-survival) → validate → demo.
6. Polish (burst-collapse test, runbook note, `make pr-ready`).

### Notes

- [P] tasks = different files, no dependencies.
- [Story] label maps task to spec.md's US1/US2/US3/US4 for traceability.
- Verify each story's tests fail before implementing that story.
- US2/US3 share the `productCache`/`registerProductWatch` scaffolding US1 establishes (T012-T015) — this is expected per research.md's design (one cache, three handlers), not a violation of story independence at the *test* level (each story's tests can still run and pass in isolation once the shared scaffolding exists).

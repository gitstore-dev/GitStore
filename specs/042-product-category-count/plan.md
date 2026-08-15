# Implementation Plan: Product Watch Transport for CategoryTaxonomy Count Reconciliation

**Branch**: `042-product-category-count` | **Date**: 2026-08-13 | **Spec**: [spec.md](./spec.md)  
**Input**: Feature specification from `/specs/042-product-category-count/spec.md`

## Summary

Add a dedicated `watchProducts`/`ProductWatchEvent` GraphQL subscription to `gitstore-api` (mirroring the existing core-kind `watchCategories`/`CategoryWatchEvent` from spec 040 — Product is a core kind with a compile-time-known shape, so it gets the same dedicated treatment, not the generic CRD-oriented `watchResources`/`WatchEvent` path), and publish Product admission changes (create, delete, categoryRef change) into `gitstore-api`'s existing `eventbus.Bus` by adding `eventbus.Publish` calls to `admitProduct` and the Product branch of `deleteResource` in `gitstore-api/internal/cataloggrpc/server.go`. On the `gitstore-controller-manager` side, add a `ProductListWatcher` (mirroring the existing `CategoryTaxonomyListWatcher`) and a Product cache with `OnAdd`/`OnUpdate`/`OnDelete` handlers that call the existing `mgr.Enqueue` against the already-registered `CategoryTaxonomy` kind — re-queuing the referenced category (and, on a `categoryRef` change, both the old and new category) rather than registering Product as its own reconciled kind. This is the same "child event drives parent enqueue" pattern already implemented for category-to-parent-category propagation in `cmd/controller/main.go`'s `registerCategoryTaxonomy`, just with Product as the child kind and no reconciler of its own.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`)
**Primary Dependencies**: existing `gitstore-api/internal/eventbus.Bus` (spec 040, already used by `CategoryTaxonomy`'s `publishCategoryTaxonomyEvent`); a new dedicated `watchProducts`/`ProductWatchEvent` GraphQL subscription in `gitstore-api` mirroring the existing `watchCategories`/`CategoryWatchEvent` (spec 040 R1's precedent for core kinds — see research.md R1); existing `gitstore-controller-manager/internal/listwatch.ListWatcher[T]`/`Watcher[T]`/`Runner[T]` (spec 036, already implemented once for `CategoryTaxonomyListWatcher`); existing `gitstore-controller-manager/internal/cache.Cache[T]`/`EventHandler[T]` (spec 026, already used for the category→parent-category enqueue pattern); existing `gitstore-controller-manager/internal/manager.Manager.Enqueue` (spec 026); existing `gitstore-controller-manager/internal/graphqlclient.Client` (spec 039). No new dependency is introduced in either module.
**Storage**: No new storage or schema changes. `gitstore-api` gains no new datastore field — Product admission already carries `RepositoryID`/`Namespace`/`Name` and the `categoryRef.name` needed to identify affected categories; the eventbus itself is in-memory-only per its existing design (no durability across restart, per spec 040 research.md R2/R3, unchanged here). `gitstore-controller-manager` gains a new in-memory Product cache (mirroring the existing `CategoryTaxonomy` cache), no persistent storage.
**Testing**: `go test ./...` in both `gitstore-api` and `gitstore-controller-manager`, following each module's existing `tests/{contract,integration}` structure (`gitstore-api/tests/contract/watch_status_test.go` is the existing precedent for eventbus/watch coverage; `gitstore-controller-manager/tests/contract/listwatch_bootstrap_test.go` and `tests/integration/reconcile_retry_resume_test.go` are the existing precedents for list-then-watch resume and restart-survival coverage this feature must extend to the Product-driven trigger path)
**Target Platform**: Linux server (existing deployment target for both services)
**Project Type**: Backend service extension (one new, additive GraphQL subscription field + event-publish call sites in an existing `gitstore-api` server, one new watcher/cache-handler pair in the existing `gitstore-controller-manager` binary) — no frontend/mobile component
**Performance Goals**: No new performance goal. Reconciliation triggered by this path is level-triggered and at-least-once, same as spec 039/040; SC-001 through SC-003 require "within one reconciliation cycle," not a specific latency bound.
**Constraints**: MUST NOT alter how `CategoryTaxonomy.status.resolved.productCount` is computed (FR-005) — this feature only adds a new trigger into the existing `Reconcile` call, never a second computation path. MUST NOT poll the full product catalogue to find affected categories (FR-008) — the affected category name(s) come directly from the Product event's `categoryRef` (and, on update, the prior `categoryRef`), not from a catalogue scan. MUST NOT reconcile a category unrelated to the triggering Product change (FR-004) — the enqueue must be scoped to only the specific category name(s) extracted from the event. MUST collapse redundant re-enqueues of the same category within a processing burst rather than growing an unbounded queue (Edge Cases, SC-006) — the existing work-queue's per-key dedup semantics (already relied upon by the category→parent-category enqueue path) are the mechanism, not a new one.
**Scale/Scope**: One existing resource kind (`CategoryTaxonomy`) gains a new upstream trigger source; one existing resource kind (`Product`) gains list/watch observation in `gitstore-controller-manager` without becoming a reconciled kind itself (no `Product` status is written, no `Product` `Reconciler` is registered). Scale bounded by the same constitution-level product-catalogue scale constraint already governing spec 039 (up to 5,000,000 products).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Test-First Development (NON-NEGOTIABLE)**: PASS (planned) — Phase 2 tasks will require contract tests for the new `admitProduct`/`deleteResource` `eventbus.Publish` call sites (product add/delete/categoryRef-change emits the expected `Event`), a contract test for the new `ProductListWatcher`, and integration tests (FR-010/FR-011) for product create/delete/reassignment converging category `productCount` and for restart-survival of a product-driven trigger — all written and confirmed failing before implementation.
- **II. API-First Design**: PASS — the one new GraphQL contract this feature adds (`watchProducts`/`ProductWatchEvent` in `shared/schemas/product.graphqls`) is defined and reviewed as part of this plan's Phase 1 design (see contracts/product-watch-contract.md) before any resolver code is written, following the exact precedent `watchCategories`/`CategoryWatchEvent` already set for core-kind watch contracts.
- **III. Clear Contracts & Versioning**: PASS — the new `watchProducts` field and `ProductWatchEvent` type are purely additive (a new `Subscription` field and a new type, no changes to any existing field), consistent with the constitution's "additive changes preferred" schema-evolution guidance; no existing contract changes.
- **IV. Observability & Debuggability**: PASS (planned) — reuses the existing `s.log`/zap structured-logging pattern already used by `publishCategoryTaxonomyEvent` and `deleteResource` for the new Product publish call sites, and the existing `gitstore-controller-manager/internal/health` queue-depth/stall metrics already cover the `CategoryTaxonomy` kind's queue regardless of what triggered an enqueue — no new metric is required, but the existing `docs/runbooks/controller-lag.md` note may need a short addition documenting that `CategoryTaxonomy` queue depth can now also reflect Product-driven fan-out.
- **V. User Story Driven Development**: PASS — spec defines P1 (add/delete/reassign) and P2 (restart-survival) stories; tasks will carry [US1]-[US4] labels matching spec.md.
- **VI. Incremental Delivery**: PASS — US1 (product-add fan-out) alone is a viable, demonstrable slice that proves the Product→CategoryTaxonomy trigger path works; US2/US3 add the delete and reassignment cases without disrupting US1; US4 (restart-survival) layers a robustness guarantee on top without changing US1-3's behavior.
- **VII. Simplicity & YAGNI**: PASS — deliberately reuses every existing primitive (`eventbus.Bus`, `ListWatcher[T]`/`Runner[T]`, `Cache[T].AddEventHandler`, `Manager.Enqueue`) rather than introducing a new dependency-trigger abstraction; the one new piece (`watchProducts`/`ProductWatchEvent`) is a mechanical copy of the existing `watchCategories`/`CategoryWatchEvent` pattern, not a new abstraction. Product is observed but not registered as its own reconciled kind, since nothing needs to write Product status as part of this feature.

No violations requiring the Complexity Tracking table.

## Project Structure

### Documentation (this feature)

```text
specs/042-product-category-count/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
shared/schemas/
└── product.graphqls                  # extend: new watchProducts Subscription field + ProductWatchEvent type, mirroring category.graphqls's watchCategories/CategoryWatchEvent

gitstore-api/
├── internal/
│   ├── cataloggrpc/
│   │   ├── server.go                 # extend: eventbus.Publish calls in admitProduct (add/update-with-categoryRef-change) and the Product case of deleteResource; new publishProductEvent helper mirroring publishCategoryTaxonomyEvent
│   │   └── server_test.go            # extend: assert Publish is called with the right kind/categoryRef payload on add/delete/reassign, and NOT called on a no-op product update
│   └── graph/resolver/
│       ├── product.resolvers.go      # extend: new subscriptionResolver.WatchProducts, mirroring WatchCategories in category.resolvers.go
│       └── product_watch_test.go     # new: WatchProducts resolver unit tests (event mapping, WATCH_EXPIRED)
└── tests/
    └── contract/
        └── product_watch_test.go     # new: end-to-end — admit a product via the gRPC admission path, subscribe to watchProducts, assert the event arrives with the expected categoryRef data

gitstore-controller-manager/
├── internal/
│   ├── listwatch/
│   │   ├── graphql_listwatcher.go     # extend: new ProductListWatcher satisfying ListWatcher[Product]/Watcher[Product], mirroring CategoryTaxonomyListWatcher but against watchProducts/products
│   │   └── graphql_listwatcher_test.go
│   └── categorytaxonomy/
│       └── products.go                # extend or sibling file: Product cache entity type + OnAdd/OnUpdate/OnDelete handlers that resolve affected CategoryTaxonomy name(s) from categoryRef (old+new) and call mgr.Enqueue
├── cmd/controller/
│   └── main.go                        # extend: wire a second Runner[Product] (list/watch only, no registered Reconciler) whose cache event handlers enqueue the affected CategoryTaxonomy key(s)
└── tests/
    ├── contract/
    │   └── product_listwatcher_test.go   # new: List/Watch contract test for ProductListWatcher, following listwatch_bootstrap_test.go's pattern
    └── integration/
        └── product_category_count_test.go # new: FR-010/FR-011 — product create/delete/reassignment converges productCount on the right category(ies) only; a restart mid-flight still converges (extends reconcile_retry_resume_test.go's pattern to the Product trigger source)
```

**Structure Decision**: Pure extension of the two existing modules touched by specs 039/040, plus one small additive schema change — no new package, no new top-level service. On the `gitstore-api` side: one new `Subscription` field + type in `shared/schemas/product.graphqls` (mirroring `watchCategories`/`CategoryWatchEvent`), a new `WatchProducts` resolver in the existing `graph/resolver` package, and two new `eventbus.Publish` call sites in `cataloggrpc/server.go` plus a small helper, following the exact pattern already established by `publishCategoryTaxonomyEvent`. On the `gitstore-controller-manager` side, `internal/listwatch` gains one new `ListWatcher[Product]` implementation alongside the existing `CategoryTaxonomyListWatcher`, and `internal/categorytaxonomy` (or a small new file within it) gains the Product-cache-to-CategoryTaxonomy-enqueue glue — deliberately not a new `internal/product` reconciler package, since Product is observed only to drive `CategoryTaxonomy` enqueues and never gets its own `Reconciler` or status writes in this feature.

## Complexity Tracking

No constitution violations — table not needed.

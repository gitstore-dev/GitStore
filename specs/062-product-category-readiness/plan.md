# Implementation Plan: Product Category and Readiness Reconciliation

**Branch**: `062-product-category-readiness` | **Date**: 2026-09-18 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/062-product-category-readiness/spec.md`

## Summary

Extend `gitstore-controller-manager`'s Product reconciler — today deletion-only
— to resolve each active Product's `spec.categoryRef` against the existing
CategoryTaxonomy cache, write `CategoryResolved`/`Ready` conditions and a
resolved category reference (opaque `uid`) via `updateProductStatus`, and
re-enqueue previously-unresolved Products when a matching CategoryTaxonomy
appears. This requires extending `UpdateProductStatusInput` and
`ResolvedCategoryDefinition` (gitstore-api contract) to carry the fields the
mutation cannot express today, and adding a bounded namespace+category index
to the controller so the reverse fan-out is an indexed lookup, not a full
Product-cache scan, per the declared 5M-product capacity envelope.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`)
**Primary Dependencies**: `github.com/99designs/gqlgen v0.17.90` (schema/resolver codegen), existing `internal/graphqlclient.Client`, existing `internal/status.StatusPatch`/`StatusClient` (`gitstore-controller-manager`), existing `internal/cache.Cache[T]`/`CacheAccessor[T]` (spec 026), existing `internal/listwatch.Runner[T]` (spec 036/042), existing `internal/manager.Manager` reconciler registration (spec 026); no new external dependency in either service.
**Storage**: No new storage. Reuses the existing `datastore.Product.Status` JSON blob (`catalog.ProductStatus`) already read/written by `updateProductStatus`; no schema migration.
**Testing**: `go test` (contract tests in `gitstore-api/internal/graph/resolver`, unit tests in `gitstore-controller-manager/internal/product` and `internal/categorytaxonomy`, integration tests in `tests/integration`), `make check TARGET=config` for RBAC.
**Target Platform**: Linux server (containerized `gitstore-api` and `gitstore-controller-manager` deployments)
**Project Type**: Two-service backend feature (GraphQL API contract change + controller reconciler change) inside an existing multi-service repo — no new project/module.
**Performance Goals**: Category resolution adds one indexed cache lookup per Product reconcile (O(1) amortized), not a per-reconcile full scan; a CategoryTaxonomy create/rename re-enqueues only Products already indexed under that (namespace, categoryName) key.
**Constraints**: MUST NOT introduce a full Product-cache scan on the CategoryTaxonomy-add/rename path (PR-003/PR-004); MUST NOT write status once a Product's foreground-deletion finalizer is set (FR-008); MUST NOT change the existing Product→CategoryTaxonomy count fan-out (spec 042, FR-012).
**Scale/Scope**: Same catalog envelope as spec 055/042: up to 5,000,000 Products per the constitution's Production Capacity Envelope; the new secondary index is sized to the in-memory Product cache already held by the controller (no new unbounded structure — one map entry per Product with a non-empty `categoryRef.name`).
**Replica/Scaling Model**: `gitstore-controller-manager` runs ≥2 replicas; each replica maintains its own Product/CategoryTaxonomy caches and index (process-local, rebuilt from the durable list-then-watch stream on start, per spec 026/036). Status writes use `updateProductStatus`'s existing `resourceVersion` optimistic-concurrency precondition, so two replicas racing to reconcile the same Product converge without an explicit lock (PR-001).
**Authentication/Authorization**: No new identity or policy surface. `updateProductStatus` already requires controller-level authorization (`product.status.write`, already granted to the `controller` RBAC role); no author-facing mutation changes.
**Load/Backpressure Model**: Re-enqueues from a CategoryTaxonomy create/rename go through the existing bounded reconciliation queue/worker pool (spec 026); the index lookup itself is O(matching products) per event, not O(all products), so a burst of category creates cannot starve unrelated reconciliation (PR-004).
**Capacity Profile**: N/A — this feature adds bounded per-item work to an already-capacity-tested reconciliation path (spec 055's Product lifecycle capacity profile, `tests/capacity/profiles/product-lifecycle.js`); no new dedicated capacity profile is introduced. SC-001/SC-002 are verified functionally (reconciliation-window convergence), not via a new load profile.
**Fault Profile**: N/A — no new failure boundary; existing controller checkpoint/replay and `updateProductStatus` conflict-retry paths already cover process replacement (PR-005), exercised functionally rather than via a new chaos profile.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Test-First**: Contract tests for the extended `UpdateProductStatusInput`/`ResolvedCategoryDefinition` and unit tests for the reconciler's resolve/condition/index logic are written before implementation (tasks phase).
- **API/Contract-First**: The `UpdateProductStatusInput`/`ResolvedCategoryDefinition` schema extension is defined in `contracts/` (Phase 1) before any resolver or controller code changes.
- **Core-Service Boundary**: Impact is explicit — `gitstore-api` (schema + `UpdateProductStatus` resolver) and `gitstore-controller-manager` (Product reconciler, CategoryTaxonomy-cache reverse index) both change; `gitstore-git-service` is untouched.
- **Replica Safety**: PASS — status writes remain resourceVersion-gated (existing `updateProductStatus` precondition); the new index is process-local, rebuilt on restart from the existing durable watch stream, matching the established Product/CategoryTaxonomy cache replica model.
- **Multi-User Security**: PASS — no new authorization surface; reuses `product.status.write`, already granted to the controller role.
- **Production Capacity**: PASS — addressed via the bounded index (avoids an O(products) scan per category event) at the declared 5,000,000-product envelope.
- **Repeatable Evidence**: N/A justified above (Capacity/Fault Profile) — this is bounded incremental work on an already capacity-tested path, not a new load-bearing boundary.
- **Bounded Work**: PASS — index lookup and re-enqueue are bounded to matching Products; retry uses the existing bounded-interval requeue mechanism (FR-011), never an unbounded retry loop.
- **Observability**: Reuses existing reconciler logging/metrics conventions (status-write-conflict counters, reconcile result logging) already present in `internal/product`/`internal/categorytaxonomy`; no new signal category introduced.
- **Incremental Delivery**: PASS — additive GraphQL input/output fields (no removal), and the controller change is deployable independently; an old controller replica simply continues not resolving categories until rolled, without breaking new-schema API replicas.
- **Simplicity**: The one new component (namespace+category→Product index) is justified by PR-003/PR-004 above a full-scan alternative that was rejected in research.md.

## Project Structure

### Documentation (this feature)

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    specs/
        062-product-category-readiness/
            plan.md
            research.md
            data-model.md
            quickstart.md
            contracts/
                product-status-category-ref.graphqls
            tasks.md
```

### Source Code (repository root)

```mermaid
%%{init: {"treeView": {"showIcons": true}} }%%
treeView-beta
    shared/
        schemas/
            product.graphqls
    gitstore-api/
        internal/
            catalog/
                status.go
            graph/
                resolver/
                    product.resolvers.go
                    product_status_contract_test.go
    gitstore-controller-manager/
        cmd/
            controller/
                main.go
        internal/
            product/
                reconciler.go
                reconciler_test.go
                graphql_status_client.go
                graphql_status_client_test.go
            categorytaxonomy/
                product_index.go
                product_index_test.go
                products.go
    tests/
        integration/
            product_category_readiness_test.go
```

**Structure Decision**: No new project or service. Changes land in the two
existing Go services (`gitstore-api`, `gitstore-controller-manager`) plus the
shared GraphQL schema and the cross-service integration test suite, following
the same layout every prior status-mutation/reconciler feature in this repo
used (specs 039/040/042/058).

## Complexity Tracking

*No unjustified Constitution Check violations. The one new component (the
namespace+category Product index) is covered under Simplicity above, not a
violation requiring a table entry.*

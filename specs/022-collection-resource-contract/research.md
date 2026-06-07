# Research: Collection Resource Contract with Label Selectors

**Branch**: `022-collection-resource-contract` | **Phase**: 0 | **Date**: 2026-06-07

## Summary

This document resolves all unknowns identified during Technical Context analysis before Phase 1 design begins.

---

## R-001: Migration Strategy — Existing `Collection` Entity

**Question**: Should the existing flat `Collection` entity be migrated in place or replaced entirely?

**Decision**: Replace entirely following the `CategoryTaxonomy` pattern.

**Rationale**: The current `Collection` entity (`ID`, `Name`, `Slug`, `DisplayOrder`, `ProductIDs`, `CreatedAt`, `UpdatedAt`, `Body`) shares no fields with a Kubernetes-style resource envelope. A selective ALTER would leave ambiguous legacy columns. The `category_taxonomy` precedent shows the team replaces rather than mutates incompatible entities.

**Alternatives considered**:
- ALTER existing table: rejected — legacy columns (`slug`, `display_order`, `product_ids`) have no mapping in the new model; mixed-state table increases maintenance burden.
- Dual-write migration: rejected — no consumers of the current `Collection` entity in production paths require continuity; stubs returning errors (as done for CategoryTaxonomy mutations) are sufficient.

**Impact**: `shared/schemas/collection.graphqls` is a full rewrite. Legacy mutations (`createCollection`, `updateCollection`, `deleteCollection`, `reorderCollections`) become stubs returning `"collection mutations are managed via git push"`.

---

## R-002: Label Selector Implementation

**Question**: How should `LabelSelector` / `matchLabels` / `matchExpressions` be implemented? No existing pattern in the codebase.

**Decision**: Implement a minimal Kubernetes-compatible label selector in the Go `catalog` package, evaluated in-memory during reconciliation and at query time for `collection.products`.

**Rationale**: The Kubernetes label selector semantics are well-specified (KEP-0000, `k8s.io/apimachinery/pkg/labels`) and widely understood. Building a minimal compatible subset in pure Go (no external dependency) is ~150 lines and directly testable. Using an external library would add a heavyweight dependency for straightforward logic.

**Selector evaluation rules**:
- `matchLabels`: Each key-value pair must match exactly — logical AND across all entries.
- `matchExpressions`:
  - `In`: label value must be in `values` list.
  - `NotIn`: label value must not be in `values` list.
  - `Exists`: label key must be present (any value); `values` ignored.
  - `DoesNotExist`: label key must be absent; `values` ignored.
- Both `matchLabels` and `matchExpressions` present: logical AND across both.
- Empty/absent `spec.selector`: evaluates to zero members (not universal).

**Stored as JSON in `spec` column** — same pattern as `CategoryTaxonomySpec`. Evaluation happens at admission (for validation) and at query time (for `collection.products`).

**Alternatives considered**:
- `k8s.io/apimachinery/pkg/labels`: rejected — pulls in large Kubernetes dependency tree for a self-contained catalog service.
- SQL/CQL WHERE clause for selector evaluation: rejected — ScyllaDB has no native JSON path + label-matching; requires full namespace product scan + in-process filter (acceptable given SC-002's 10,000-product bound).

---

## R-003: `collection.products` Snapshot Cursor Semantics

**Question**: How are snapshot-at-query-time cursor semantics implemented without a dedicated snapshot store?

**Decision**: At first-page request, evaluate the label selector against all products in the namespace to produce an ordered list of UIDs. Encode this ordered UID list (or a hash + server-side cache entry) in the opaque cursor. Subsequent pages decode the cursor to retrieve the same ordered UID list and slice into it.

**Rationale**: This is the same strategy used for paginating stable result sets in any cursor-based system where the underlying data can change. The UID list is compact (UUIDs, ~36 bytes each, 10,000 products ≈ 360KB before encoding) and can be stored transiently in the server process (in-memory LRU keyed by cursor token) with a configurable TTL (e.g., 5 minutes). No persistent snapshot infrastructure required.

**Alternatives considered**:
- Re-evaluate selector on every page: rejected — would cause skips/duplicates when labels change mid-traversal (violates clarified requirement).
- ScyllaDB materialized view: rejected — ScyllaDB MVs do not support arbitrary in-row JSON label evaluation.
- Store resolved UID list in `status`: rejected — explicitly ruled out in clarification (products accumulate fast; status stores only `memberCount`).

---

## R-004: ScyllaDB Schema for `collection` Tables

**Decision**: Three-table pattern mirroring `category_taxonomy`:

```
collection              — PRIMARY KEY ((namespace), creation_timestamp DESC, uid DESC)
collection_by_name      — PRIMARY KEY ((namespace), name)
collection_by_uid       — PRIMARY KEY (uid)
```

**Rationale**: Identical to `category_taxonomy` / `products_by_namespace` patterns already in the codebase. Namespace-partitioned primary table enables efficient `ListCollections` scans. Lookup tables enable O(1) `GetCollectionByName` and `GetCollectionByUID` without secondary indexes (which require coordinator round-trips in ScyllaDB).

**Columns for primary table**: `namespace`, `creation_timestamp`, `uid`, `api_version`, `kind`, `name`, `generation`, `resource_version`, `revision`, `labels map<text,text>`, `annotations map<text,text>`, `git_commit_sha`, `git_ref`, `spec text` (JSON), `body text`, `status text` (JSON).

**No `selector` column** — selector is stored inside `spec` JSON. No `product_ids` column — membership is derived from label evaluation, not stored.

---

## R-005: `ParseResource` Extension for `Collection`

**Decision**: Add `case "Collection":` to the `ParseResource` switch in `gitstore-api/internal/validate/validator.go`, alongside a new `catalog.CollectionResource` / `CollectionSpec` struct in `gitstore-api/internal/catalog/collection.go`.

**Validation rules**:
- `apiVersion`: must equal `catalog.gitstore.dev/v1beta1`
- `kind`: must equal `Collection`
- `metadata.name`: required, DNS label format (reuse existing `validateMetadataName`)
- `spec.title`: required, non-empty string
- `spec.selector.targetRef.kind`: if present, must equal `Product`
- `spec.selector.matchExpressions[*].operator`: must be one of `In`, `NotIn`, `Exists`, `DoesNotExist`
- `spec.selector.matchExpressions[*].values`: must be non-empty for `In`/`NotIn`; must be empty for `Exists`/`DoesNotExist`
- `spec.media`: zero or more `MediaDefinition` entries (reuse existing validation)

**No cycle detection needed** — Collections are flat (no parent/child hierarchy).

---

## R-006: Admission Dispatch for Collection

**Decision**: Add a `Collection` branch in `AdmitResources` in `gitstore-api/internal/cataloggrpc/server.go`, after the existing `CategoryTaxonomy` / `Product` dispatch. Collections have no intra-push ordering dependency (no parent refs), so they can be admitted in any order or in parallel with products.

**Admission steps** (per collection):
1. Upsert `Collection` entity to datastore (create or update).
2. Evaluate `spec.selector` against all products currently in the namespace → compute `memberCount`.
3. Write `status.resolved.memberCount` and `status.conditions` (`SelectorAccepted`, `MembersResolved`, `Ready`).

**Alternatives considered**:
- Defer member resolution to a separate controller reconciliation loop: deferred to a future spec (mirrors the CategoryTaxonomy → controller split in #244). For this spec, synchronous admission-time resolution is sufficient.

---

## R-007: GraphQL Schema Strategy

**Decision**: Replace `shared/schemas/collection.graphqls` entirely with a Kubernetes-style envelope matching the `Category` shape. Legacy mutation stubs return `"collection mutations are managed via git push"`.

**New top-level types**:
- `Collection implements Node` — resource envelope with `metadata: CollectionObjectMeta`, `spec: CollectionSpec`, `status: CollectionStatus`, `body`, `products(...)`.
- `CollectionObjectMeta` — mirrors `CategoryObjectMeta` (uid, name, namespace, labels, annotations, resourceVersion, generation, creationTimestamp, revision).
- `CollectionSpec` — `title`, `selector: LabelSelector`, `media: [MediaDefinition!]!`.
- `LabelSelector` — `matchLabels: [KeyValuePair!]`, `matchExpressions: [LabelSelectorRequirement!]`.
- `LabelSelectorRequirement` — `key`, `operator: LabelSelectorOperator`, `values: [String!]`.
- `LabelSelectorOperator` — enum: `IN`, `NOT_IN`, `EXISTS`, `DOES_NOT_EXIST`.
- `CollectionStatus` — `conditions`, `observedGeneration`, `lastAppliedRevision`, `resolved: ResolvedCollectionDefinition`.
- `ResolvedCollectionDefinition` — `memberCount: Int!`, `media: [ResolvedFileDefinition!]!` (no `productRefs`).
- `CollectionBy @oneOf` — `id: ID`, `namespacePath: CollectionNamespacePath`.

**`LabelSelector` and `LabelSelectorRequirement` are new shared types** defined in `shared/schemas/schema.graphqls` (alongside `CategoryBy`, `ProductBy`, etc.) to enable reuse when other resources adopt label selectors.

---

## Constitution Compliance Check

| Principle | Status |
|-----------|--------|
| I. Test-First | ✅ — contract + unit tests written before implementation |
| II. API-First | ✅ — GraphQL schema contract defined in Phase 1 before any resolver |
| III. Clear Contracts & Versioning | ✅ — schema follows additive evolution; mutations stubbed not removed |
| IV. Observability | ✅ — admission logging follows existing pattern |
| V. User Story Driven | ✅ — all tasks map to US1–US4 |
| VI. Incremental Delivery | ✅ — P1 (push + query) deliverable independently of P2 (selector membership) |
| VII. Simplicity | ✅ — no new external dependencies; selector evaluation in pure Go |

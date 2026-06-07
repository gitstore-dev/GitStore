# Research: CategoryTaxonomy Frontmatter and Hierarchy Enforcement

**Date**: 2026-06-06 | **Feature**: 021-category-taxonomy

---

## R-001: Kind-Routing in `validate.Parse`

**Decision**: Extend `validate.Parse` to inspect the `kind` field before struct binding and dispatch to a per-kind validator.

**Rationale**: The existing `Parse` function binds directly into `catalog.ProductResource`. Adding `CategoryTaxonomy` as a second kind requires a routing step before binding. The cleanest approach — consistent with how k8s API machinery works — is to extract `kind` from the raw YAML map in `preParseChecks`, then return the resource-typed struct via a common interface (or a discriminated union return type).

**Implementation approach**: Introduce `ParseResource(r io.Reader) (*ParsedResource, []byte, error)` where `ParsedResource` is a struct with a `Kind` field and one-of-typed fields (`Product *ProductResource`, `CategoryTaxonomy *CategoryTaxonomyResource`). Existing callers of `Parse` continue to work unchanged; `AdmitResources` and `ValidateResources` switch to `ParseResource`. This avoids breaking the existing test surface.

**Alternatives considered**:
- New `ParseCategory` function alongside `Parse` — rejected: duplicates all pre-parse/extract logic.
- Interface-based return — rejected: forces type assertion at every call site.

---

## R-002: Cycle Detection — Phase Assignment and Algorithm

**Decision**: Three-tier approach based on what requires DB access and cost:

1. **Direct self-parenting** (`parentRef.name == metadata.name`): Caught in `ValidateResources` (pre-receive) — pure struct check, no DB, always synchronous push rejection.

2. **Intra-push mutual cycle** (e.g., push contains A→parentRef=B AND B→parentRef=A in the same commit): Detected in `AdmitResources` (post-receive) by building a directed graph of all `CategoryTaxonomy` blobs in the push and detecting cycles using DFS/topological sort on the in-memory push graph. Stored as `Acyclic: False` status condition. No DB lookup needed — the cycle is fully within the push.

3. **Cross-push cycle** (A already stored in DB as parent of B; user pushes update to A setting parentRef=B): Deferred to controller reconciliation (GH#244). Walking the full stored ancestor chain is an O(depth) multi-hop DB query — too expensive for the push path. Surfaced as `Acyclic: False` status condition by the controller.

**Rationale**: DB lookups in the pre-receive `ValidateResources` handler are undesirable — they increase push latency and couple the hot path to DB availability. The user confirmed this during task generation. The only pre-receive check that requires no DB is the self-reference check. All other validation that requires DB state is pushed to `AdmitResources` or the controller.

**Intra-push cycle algorithm**: Build `map[string]string{name → parentRefName}` from all `CategoryTaxonomy` blobs in the push. For each node, walk the parent chain using the map (not DB). If a walk visits a node already in the current path, a cycle exists.

**Alternatives considered**:
- All cycle detection in pre-receive with DB lookups — rejected: DB calls on critical push path increase latency and risk cascading failures if DB is slow.
- All cycle detection deferred to controller — rejected: intra-push mutual cycles would be undetected at admission time, potentially storing obviously invalid data.

---

## R-003: Materialized Path Storage and Update Semantics

**Decision**: Store `ancestor_path` as a slash-separated string of `metadata.name` segments from root to the category itself (e.g., `electronics/computers/laptops`). This value is computed and stored in `CategoryTaxonomyStatus.resolved.path`.

**Storage layer**: The `CategoryTaxonomy` datastore entity carries `AncestorPath string` alongside `ParentName string` (the adjacency pointer). This enables prefix-range lookups in both `go-memdb` (string index) and ScyllaDB (text column with LIKE prefix or secondary index).

**Update semantics**: When a category's ancestor is updated (parent renamed or re-parented), the descendant's `ancestor_path` would become stale. During this spec, ancestor path is written only at admission time based on the parent's current stored path. Cascading re-computation of descendants is deferred to the controller (GH#244). If a parent's path changes, affected descendant status conditions will be set to `Unknown` by the controller when it reconciles.

**ScyllaDB column type**: `TEXT` (not `ltree` — Cassandra/ScyllaDB has no native ltree). Prefix lookups use `LIKE 'electronics/%'` or a secondary index on the path column.

**go-memdb index**: `StringFieldIndex{Field: "AncestorPath"}` (non-unique) to support prefix scanning.

**Rationale**: Materialized path avoids recursive DB traversal for ancestry display, supports O(1) depth computation (count slashes + 1), and is the standard ScyllaDB-compatible equivalent of Postgres `ltree`. The adjacency pointer (`ParentName`) is retained for direct parent lookups and for the controller's re-computation.

**Alternatives considered**:
- Adjacency list only — rejected: O(depth) recursive lookups to build ancestry display; expensive for deep trees.
- Closure table — rejected: write amplification on every insert proportional to depth; no native support in ScyllaDB.
- Nested sets — rejected: expensive updates (re-number entire subtree) on every insertion.

---

## R-004: `ValidateResources` Multi-Kind Routing

**Decision**: The existing `ValidateResources` implementation in `cataloggrpc/server.go` iterates blobs and calls `validate.Parse` for Product validation. With this spec, it calls `validate.ParseResource` and dispatches:
- `kind: Product` → existing product validation path (no change)
- `kind: CategoryTaxonomy` → new category validation path
- `kind: <unknown>` → `ValidationError` with message "kind X is not recognized"

The `ValidateResources` RPC signature and proto definition are **unchanged** — no proto changes needed.

**Rationale**: The proto contract sends raw bytes (`ResourceBlob.content`) and receives `ValidationError` messages. Kind-routing is entirely inside the Go handler. This keeps the Rust client completely unchanged.

---

## R-005: parentRef Existence Check — Phase Assignment

**Decision**: parentRef existence check (FR-005) runs in **`ValidateResources` (pre-receive phase)** via DB lookup, not in `AdmitResources`.

**Rationale** (from spec clarification session 2026-06-06): The spec clarification established that parentRef DB lookups belong in the post-receive flow. However, FR-005 says "the push is rejected with an error indicating the referenced parent does not exist." A post-receive rejection cannot surface to the push author as a push error. On further analysis:
- `ValidateResources` has access to the `datastore.Datastore` via `cataloggrpc.Server`.
- A DB lookup for a category by name is O(1) on the `name_namespace` index.
- This is the correct place for cross-resource reference checking (same as how Kubernetes admission webhooks work: validate references against API server state before admission).

The clarification session (Q1) established: "parentRef validation in the post-receive hook (DB lookup, synchronous push rejection)". In this system, the `ValidateResources` gRPC is the pre-receive hook callout — it is the synchronous rejection mechanism. The AdmissionControlHandler (post-receive) fires `AdmitResources` fire-and-forget. So "post-receive flow with DB lookups" in this architecture means `ValidateResources` (which is called from the synchronous pre-receive path). This is the correct interpretation.

**Conclusion**: parentRef existence check in `AdmitResources` (post-receive, fire-and-forget). If parent not found in DB and not in same push, the category is stored with `ParentResolved: False` condition. The push is never rejected for a missing parent. The in-push co-creation case (parent and child in the same commit) is handled by checking all `CategoryTaxonomy` blobs in the push before doing DB lookups — if the parent is found in the push set, `ParentResolved` is optimistically set to `True`.

---

## R-006: Single-Category Product Constraint (FR-011)

**Decision**: Check in `ValidateResources` (pre-receive) — parse `Product` blobs, count `spec.categoryRef` entries. More than one `categoryRef` → reject. (Note: the data model has `categoryRef *ObjectReference` — a single pointer — so the constraint is enforced by the type itself; a product document cannot set multiple `categoryRef` fields. The validation error surfaces only if a user sets `categoryRef` to a list or array — caught by YAML unmarshalling.)

**Rationale**: The product spec already models `categoryRef` as a single `*ObjectReference`. The validation rule is simply: if `spec.categoryRef` is set, it must decode to a single `ObjectReference` with a valid `name`. The "more than one category" error is structurally prevented by the YAML schema. The explicit test (US3 Scenario 1) involves pushing a product that somehow references two categories; this is caught either at YAML unmarshal time or by an explicit validator rule.

---

## R-007: `CategoryTaxonomy` vs Legacy `Category` Entity Coexistence

**Decision**: Introduce a new `CategoryTaxonomy` entity in the datastore (parallel to the existing `Category` struct). The existing `Category` struct and its CRUD operations are **not modified** during this spec. The new `CategoryTaxonomy` entity is the git-backed Kubernetes-style resource; the old `Category` is the legacy admin-UI-created entity. Consolidation is deferred.

**Rationale**: The existing `Category` entity backs the legacy `createCategory` / `updateCategory` GraphQL mutations that are currently stub-implemented (TODO comments in `category.resolvers.go`). Removing or merging it now would be scope creep. The spec only requires that `CategoryTaxonomy` documents pushed via git are stored and queryable — the new entity serves this purpose independently.

**New datastore operations added to `Datastore` interface**:
```go
CreateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error)
ListCategoryTaxonomies(ctx context.Context, namespace string, page PageParams) (*PageResult[CategoryTaxonomy], error)
UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error
```

---

## R-008: GraphQL Schema Impact

**Decision**: The existing `Category` GraphQL type is extended additively with new fields sourced from `CategoryTaxonomy` storage: `apiVersion`, `kind`, `labels`, `title`, `status`. The existing `name`, `slug`, `body`, `parent`, `children`, `depth`, `path` fields are retained. The query resolvers for `category` and `categories` are updated to read from `CategoryTaxonomy` storage when a git-backed category is found. Legacy `createCategory` / `updateCategory` mutations remain unchanged (stub behavior).

**Rationale**: Additive schema changes are non-breaking (Principle III). The existing `CategoryBy` input type is extended with a `name` lookup to enable `category(by: {name: "electronics"})` queries.

---

## R-009: E2E Test Approach

**Decision**: E2E tests follow the same pattern as spec #020: push test documents via `git push`, assert gRPC response or post-push query result. The `cataloggrpc` integration tests (unit-level) mock the datastore. The E2E tests use the `compose.yml` stack with `memdb` backend (fast, no ScyllaDB dependency for CI default).

**Rationale**: Spec #020 established this pattern as working CI infrastructure. The category E2E tests slot into the same Go test binary and compose stack.

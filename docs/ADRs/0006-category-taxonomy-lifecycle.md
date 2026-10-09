# ADR 0006: CategoryTaxonomy Lifecycle

**Status**: Accepted (2026-10-09)

**Open item**: `spec.media[*].fileRef` resolution and the `MediaResolved` condition remain deferred (GH#378).

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`CategoryTaxonomy` is a hierarchical catalog category. Products reference categories
via `spec.categoryRef`. Categories can be nested via `spec.parentRef`, forming a tree
whose root nodes have no parent. The system maintains a materialized `ancestorPath`
(slash-separated names from root to self) for efficient tree queries.

Key invariants:
- The category tree must be acyclic.
- Self-parenting is rejected at push time.
- Hierarchy (`status.resolved.path`/`depth`) is controller-managed, not author-written.
- Deletion of a category with children is rejected (children must be deleted or
  re-parented first).
- Deletion of a category with assigned products is **not** rejected: products are
  decoupled asynchronously instead (see "Delete" below and GH#243/spec 052).

Phase 1 open work from GH#82 includes deletion semantics and controller reconciliation.
This ADR closes those gaps.

**Amendment (GH#243 / spec 052, 2026-08-20)**: the original decision below rejected
deletion for *both* dependent types (children and assigned products). Design review
revised this to a hybrid: children still block deletion (no safe default exists for an
orphaned child — see "Delete" below), but assigned products no longer do. A product
without a category is already a normal, first-class state in this catalog, so a deleted
category's products are decoupled asynchronously (their `CategoryResolved` condition
moves to `False`/`CategoryDeleted`) rather than blocking the category's removal.

**Amendment (GH#382 / spec 057, 2026-10-07)**: four changes.

1. **One hierarchy source.** The client-visible hierarchy is `status.resolved.path`/
   `status.resolved.depth`, written by the controller. The top-level `Category.path`/
   `Category.depth` GraphQL fields, derived from the admission-time `ancestorPath`, went stale
   after an ancestor moved and are removed. There is no fallback: `status.resolved` is `null`
   until the first reconcile. The admission-time `ancestorPath` remains an internal datastore
   value used for admission-time cycle checks only. No resolver reads it.
2. **Git-backed mutations.** They are named `createCategory`/`updateCategory`/`deleteCategory`,
   after the GraphQL `Category` type, matching `updateCategoryStatus` and `watchCategories`.
   The `*CategoryTaxonomy` names below were illustrative. `createCategory` writes only to
   `gitstore-system`. Update and delete follow stored provenance. `deleteCategory` is a Git file
   removal, not a direct datastore transition.
3. **Mutation admission and diagnostics** follow ADR-0015 §4's two lanes (see "Mutation
   admission and diagnostics" below).
4. **Descendant filtering** uses a closure-style ancestor index keyed on
   `status.resolved.path`. It is not a prefix scan over `ancestorPath` (see "Ancestor index"
   below).

## Decision

`CategoryTaxonomy` is **Git-backed**. Desired state is a Markdown file with YAML
frontmatter pushed to a repository. `ancestorPath` and status conditions are
controller-managed fields in the datastore.

### Storage classification

| Layer           | Owner                                   |
|-----------------|-----------------------------------------|
| Desired state   | Git frontmatter (Markdown file in repo) |
| Hydrated record | Datastore (ScyllaDB/memDB)              |
| `ancestorPath`  | Datastore; admission-internal only      |
| `status.resolved` hierarchy | Datastore; controller-managed |
| Ancestor index  | Datastore; derived from `status.resolved`, rebuildable |
| Status          | Datastore; controller-managed           |
| Finalizers      | Datastore; controller-managed           |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.title`,
`spec.parentRef`, `spec.media`, Markdown body.

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.creationTimestamp`,
`metadata.revision`, `metadata.ownerReferences`, `status.*` (including `status.resolved.path`/`depth`).

### Lifecycle rules

#### Create

**Git push path (canonical):**

1. Author creates `categories/<name>.md` in a repository and pushes.
2. Pre-receive validates: envelope, `kind: CategoryTaxonomy`, `spec.title` required,
   `spec.parentRef.name` must not equal `metadata.name` (self-parenting), media
   `fileRef.name` present when `media` is non-empty.
3. Post-receive admission:
   - Namespace and repository `Active`.
   - `ownerReferences` written pointing at repository (pre-existing gap: not yet
     implemented by any admission path — tracked separately from this ADR's
     GH#243/spec 052 amendment below).
   - If `spec.parentRef` is absent: stored as root node; `ancestorPath = name`;
     `ParentResolved=True` (vacuously, no parent required); no parent
     `ownerReferences` entry (there is no parent to point at).
   - If `spec.parentRef` references a category in the same push (co-creation):
     `ancestorPath = parentName/childName` tentatively; `ParentResolved=True` if
     the parent was admitted before this child in the same batch; an
     `ownerReferences` entry `{kind: CategoryTaxonomy, uid: <parent uid>,
     blockOwnerDeletion: true}` is written on the child at the same moment
     (GH#243/spec 052).
   - If `spec.parentRef` references an existing category in the datastore: full
     ancestor path inherited; `ParentResolved=True`; same `ownerReferences` entry
     written as above.
   - If `spec.parentRef` references a category that does not exist anywhere:
     category stored as tentative root; `ancestorPath = name`; `ParentResolved=False`
     with reason `ParentNotFound`; no `ownerReferences` entry yet — the controller
     writes it asynchronously once `ParentResolved` later transitions to `True`.
   - Intra-push cycles (A→B, B→A in the same commit): both stored with
     `Acyclic=False`.
   - `AdmissionAccepted=True`.

**GraphQL mutation path:**

`createCategory` commits `categories/<name>.md` to the namespace's `gitstore-system`
repository and admits that commit synchronously. It never writes the datastore directly.
Because API commits bypass the Git push hooks, the mutation runs the pre-receive checks itself
before committing (see "Mutation admission and diagnostics").

#### Update

1. Author edits the category file and pushes, or issues `updateCategory`. The mutation commits
   to the category's stored provenance (admitted repository and path) and keeps the Markdown
   body unless a new one is supplied.
2. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.
3. `spec.parentRef` change (re-parenting): allowed in Phase 1. The controller
   recomputes `status.resolved.path` for the node and all its descendants after admission,
   and replaces the node's `ownerReferences` parent entry with one pointing at the
   new parent's `uid` (GH#243/spec 052). If the new parent does not exist yet,
   `ParentResolved=False`, the `ownerReferences` parent entry is removed until it
   does, and the controller retries asynchronously.
4. Admission cycle-detection: when `spec.parentRef` changes, admission checks for
   self-loops and direct mutual cycles (A→B, B→A) synchronously. Deep multi-hop
   cycle detection is deferred to the controller to avoid O(depth) synchronous DB
   scans on every push.

#### Delete

Dependent tracking uses Kubernetes-style `metadata.ownerReferences` with
`blockOwnerDeletion` (GH#243/spec 052) rather than a `spec.parentRef.name`/
`spec.categoryRef.name` string-match scan: every child category holds an
`ownerReferences` entry on itself pointing at its parent with
`blockOwnerDeletion: true` (see "Create"/"Update" above); every product holds one
pointing at its resolved category with `blockOwnerDeletion: false`, written by the
controller once `CategoryResolved` transitions to `True`.

1. Author deletes the category file and pushes, or issues `deleteCategory`. The mutation runs
   step 2's child check before committing, then removes the file at stored provenance and
   admits the removal. It returns `{ category, outcome }` with `TERMINATION_STARTED`, or
   `ALREADY_TERMINATING` without a commit.
2. Before any record is removed, admission queries for any resource whose
   `metadata.ownerReferences` contains an entry for this category's `uid` with
   `blockOwnerDeletion: true` (children). If any exist, **rejected** with
   `FailedPrecondition: child categories present`. Products — entries with
   `blockOwnerDeletion: false` — are **not** checked here and never block this
   step; see step 5.
3. If the child check passes, the API adds the `gitstore.dev/foreground-deletion`
   finalizer and sets `metadata.deletionTimestamp`, regardless of how many
   `blockOwnerDeletion: false` (product) entries currently name this category.
4. The controller re-runs the `blockOwnerDeletion: true` query on every reconcile
   of the terminating category; a child created after step 2 (and its own
   admission writing a fresh `blockOwnerDeletion: true` entry) still withholds
   final removal (step 6).
5. Concurrently, for every resource with a `blockOwnerDeletion: false` entry naming
   the terminating category (i.e. every dependent `Product`), the controller both
   removes that `ownerReferences` entry (orphaning the product, mirroring
   Kubernetes' Orphan deletion-propagation policy) and sets that product's
   `CategoryResolved` condition to `False` with reason `CategoryDeleted` — the same
   unresolved-reference pattern already used for a category's own unresolved
   `spec.parentRef` (`ParentResolved=False`/`ParentNotFound`) — without modifying
   the product's git-authored `spec.categoryRef`, which stays under Git's
   ownership; `ownerReferences` is a different, controller-managed field. Admission
   separately rejects any *new* `Product` create/update that targets a category
   already `Terminating`, so no fresh `blockOwnerDeletion: false` entry can be
   created against a category that is about to disappear.
6. Product decoupling is a completion condition. The controller processes
   bounded, idempotent pages from the durable reverse projection and retains the
   category plus finalizer across crashes and replica handoff. Only a fresh
   empty Product-dependent page permits the final step.
7. Final removal conditionally establishes zero blocking dependents and completed
   Product cleanup at the datastore boundary. Category create/re-parent admission
   rejects a `Terminating` parent, closing the check/delete race.

**Move vs delete/recreate:** Moving a category to a different parent is an update to
`spec.parentRef`, not a delete/recreate. The UID is preserved. All descendant
`status.resolved.path` values are recomputed by the controller asynchronously after
re-parenting.

### Cycle prevention

| Phase       | Check                                                              | Behaviour on violation                                                                              |
|-------------|--------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------|
| Pre-receive | Self-parent: `parentRef.name == metadata.name`                     | Reject; no record stored                                                                            |
| Admission   | Direct mutual cycle: A→B where B already has `parentRef.name == A` | Both stored with `Acyclic=False`; controller sets error condition                                   |
| Controller  | Deep cycle detection on re-parent (walk ancestor chain up to root) | Sets `Acyclic=False` with reason `CycleDetected`; `status.resolved.path` frozen at last known acyclic value |

The controller must not update `status.resolved.path` while `Acyclic=False`. Authors must fix
the cycle in git and push a corrected manifest.

### Hierarchy recomputation on parent move

When a category's `spec.parentRef` changes, the controller must:

1. Recompute `status.resolved.path`/`depth` for the moved category by walking `parentRef` up
   its cache (depth limit 128).
2. Enqueue its direct children, found by `parentRef`, not by an `ancestorPath` prefix query.
3. Recompute each child the same way, level by level, until the subtree is covered.
4. Update `observedGeneration` on each affected record.

This is potentially a wide fan-out for deep hierarchies. Processing level by level avoids
partial path states. Recomputation is idempotent and level-triggered. Each status write also
updates that category's ancestor-index entries (see "Ancestor index").

### Ancestor index (descendant filtering)

`categories(namespace, filter: { descendantOf, includeSelf, maxDepth })` returns a subtree. It
is served from a derived, rebuildable closure index with one entry per (namespace, ancestor,
descendant). Each entry carries the descendant's relative depth, ordered by
`(ancestor, depth, name)`.

- **Source.** The index is built from `status.resolved.path` only. A category that has never
  been reconciled has no entries, so it appears in no subtree and in no `children` list until
  the first reconcile.
- **Matching.** Matching is by whole segment: `computers` never matches `computers-refurb`.
  Filtering takes a category name, not a path, because names are unique per namespace and paths
  change when ancestors move. An unknown name returns an empty result.
- **Maintenance.** The API maintains the index in the same write paths as `status.resolved`
  (controller status writes) and final record removal. Each write replaces only that category's
  own O(depth ≤ 128) entries. A re-parent of N nodes at depth d therefore changes O(N·d) entries
  in total, spread across the existing cascade. There is no new fan-out mechanism and no
  request-time aggregate.
- **Reads.** A subtree query reads one ancestor's slice and paginates natively. `maxDepth` is a
  range on the depth clustering column. `Category.children` is `descendantOf: self, maxDepth: 1`.
  `Category.parent` resolves `parentRef` when `ParentResolved=True`.
- **Convergence.** Index writes are idempotent. A partial write must be retried or repaired from
  the authoritative record. Existing categories are backfilled after the index migration, because
  an unchanged status produces no write that would populate them.
- **Freshness.** Results are eventually consistent with the cascade. `AncestorPathReady` is the
  convergence signal.

### Mutation admission and diagnostics

Post-receive admission is asynchronous for a push, which may carry thousands of objects and
must not be held open. It is synchronous inside a mutation, which carries exactly one object
within the request deadline. A mutation never batches manifests. Category admission returns its
decision to the caller instead of only logging it.

- **Pre-receive (Lane A).** API commits bypass the hooks, so the mutation itself
  runs everything pre-receive enforces before committing:
  - schema and policy validation;
  - self-parent rejection;
  - `Terminating` parent rejection;
  - cross-namespace `parentRef` rejection;
  - `media[*].fileRef.name` presence;
  - for delete, the `blockOwnerDeletion: true` child check.

  A denial writes no commit.
- **Post-receive (Lane B).** A denial after the ref has moved keeps the last
  accepted generation, records `AdmissionAccepted=False`/`AdmissionReportFailed`, and fails the
  mutation. A push-path denial is recorded the same way, not just logged. A mutation succeeds
  only when the returned record is the generation produced by its own commit. If a different
  commit to the same file wins the ref, the mutation reports superseded.
- **Wire contract.**
  - Every mutation error uses one kind-neutral envelope with at most four keys:
    - `code`: always present.
    - `diagnostics[]`: present when the error has detail.
    - `phase` (`PRE_RECEIVE`|`POST_RECEIVE`): only on `ADMISSION_REJECTED`.
    - `commit`: only on `POST_RECEIVE`.
  - Each diagnostic is `{ reason, message, level, file?, field? }`. Apart from `reason`, these
    are a subset of the `AdmissionReport` annotation fields.
  - Codes are chosen by cause: `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND`, `CONFLICT`,
    `FAILED_PRECONDITION`. Namespace's `NAMESPACE_*` codes migrate onto the same set.
  - The error `message` is the same flattened `file: message; …` text a push rejection carries.
  - Warnings on a successful mutation go in the top-level response `extensions.admission`, as
    `[{ path, commit, diagnostics }]` keyed by response path. That key sits alongside other
    top-level keys, such as cost reporting.

### File location convention

When a repository name is not specified in a GraphQL mutation:

```
categories/<metadata.name>.md
```

### Git write path

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: laptops
  namespace: acme-store
spec:
  title: Laptops
  parentRef:
    kind: CategoryTaxonomy
    name: computers
  media:
  - fileRef:
      kind: File
      name: laptops-hero
---

Laptop category description.
```

### GraphQL mutation delegation

| Operation              | Action                    | Behaviour                                                                                             |
|------------------------|---------------------------|-------------------------------------------------------------------------------------------------------|
| `createCategory`       | `categoryTaxonomy.create` | Pre-receive checks; commits `categories/<name>.md` to `gitstore-system`; admits synchronously.          |
| `updateCategory`       | `categoryTaxonomy.update` | Pre-receive checks; commits to stored provenance; admits synchronously. Name and namespace immutable.   |
| `deleteCategory`       | `categoryTaxonomy.delete` | Child check; removes the file at stored provenance; admission adds the `foregroundDeletion` finalizer and sets `Terminating`. Assigned products never block; the controller decouples them asynchronously. Returns `{ category, outcome }`. |
| `completeCategoryDeletion` | `categoryTaxonomy.purge` | Controller-only. Removes a `Terminating` category, its finalizer and its ancestor-index entries once no children remain and product decoupling is complete. Replaces the deprecated `completeDeletion` flag on `updateCategoryStatus`. |
| `category(by:)`        | `categoryTaxonomy.read`   | Read-only datastore query; hierarchy via `status.resolved`.                                           |
| `categories(filter:)`  | `categoryTaxonomy.list`   | Read-only, namespace-scoped; optional subtree filter served from the ancestor index.                  |

No direct datastore write path exists for `CategoryTaxonomy` authoring. Only the
controller-only `updateCategoryStatus` writes status.

### Validation and admission rules

| Phase       | Rule                                                                                                                                                                                 |
|-------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive | Envelope valid; `kind: CategoryTaxonomy`; `spec.title` required; self-parent rejected; `media[*].fileRef.name` present when `media` non-empty.                                       |
| Admission   | Namespace and repository `Active`; no cross-namespace `parentRef` in Phase 1; direct mutual cycle check.                                                                             |
| Controller  | `spec.parentRef` full resolution; deep cycle detection; `ancestorPath` computation and propagation to descendants; `spec.media[*].fileRef` resolution (deferred to Phase 2, GH#244). |

Cross-namespace `spec.parentRef` is **rejected at admission time** in Phase 1.

### Status and reconciliation behaviour

| Condition           | Meaning                                                                                 |
|---------------------|-----------------------------------------------------------------------------------------|
| `AdmissionAccepted` | Category stored in datastore.                                                           |
| `ParentResolved`    | `spec.parentRef` was found and is in the same namespace.                                |
| `Acyclic`           | Category's ancestor chain contains no cycle.                                            |
| `AncestorPathReady` | `status.resolved.path` is up-to-date and reflects the current parent chain.             |
| `MediaResolved`     | All `spec.media[*].fileRef` entries found (deferred to Phase 2, GH#244).                |
| `Ready`             | Parent resolved, acyclic, ancestor path current.                                        |
| `Terminating`       | `foregroundDeletion` finalizer present; `blockOwnerDeletion: true` dependents (children) must be drained before removal. `blockOwnerDeletion: false` dependents (assigned products) are decoupled asynchronously, not drained, and never gate removal. |

When `AncestorPathReady=False`, subtree-filtered queries and `children` may return stale
results. This is a transient state during large tree re-parents and resolves within one
controller reconcile pass.

### Durable controller watch

CategoryTaxonomy reconciliation consumes `watchCategories` from the generic durable
resource journal, like every other watched kind. Its CDC source reads the authoritative
`category_taxonomies_by_namespace` row, including the hierarchy columns (`parent_name`,
`ancestor_path`), and is registered in the shared catalog CDC source registry. Because
every write goes through admission into that row, CDC observes every create, status
write, Terminating transition and removal. Watches are authorized as
`categoryTaxonomy.watch`.

## Consequences

Positive:
- Category hierarchy is fully reviewable through git history.
- Deletion is safe for structural dependents: children block deletion, and
  `ownerReferences`/`blockOwnerDeletion` makes that check an indexed lookup rather
  than a full-table name scan.
- Deletion of a category with assigned products is not blocked, but is not silent
  either: products are decoupled (orphaned `ownerReferences`, `CategoryResolved=
  False`/`CategoryDeleted`) instead of being left pointing at nothing.
- Re-parenting preserves UIDs; cascade `status.resolved.path` recomputation is safe.
- Subtree queries are bounded, single-slice index reads; API and Git authoring give identical
  admission decisions and diagnostics.
- Cycle detection is layered: instant self-loop rejection at push, async deep detection
  by controller.
- The `ownerReferences`/`blockOwnerDeletion` mechanism is reusable as-is for the
  File resource's future reverse-reference tracking (ADR-0008, doc 034 Phase 2),
  rather than requiring a second, bespoke dependent-tracking mechanism.

Negative:
- Re-parenting a deep category triggers a potentially large controller fan-out to
  update descendant `status.resolved.path` values and their ancestor-index entries
  (O(subtree × depth) entry changes); reconcile may take seconds for large trees.
- A just-created category has `status.resolved = null` and is invisible to subtree filters
  until its first reconcile.
- Deep cycle detection is not synchronous at push time; a multi-hop cycle can be
  stored temporarily with `Acyclic=False` before the controller detects it.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`.
- [ADR-0004](0004-product-lifecycle.md) — Products reference categories via
  `spec.categoryRef`, recorded as a `blockOwnerDeletion: false` `ownerReferences`
  entry once resolved (GH#243/spec 052). Product deletion does not affect the
  category. Category deletion is **not** blocked by assigned products; instead the
  controller decouples each one (`CategoryResolved=False`/`CategoryDeleted`) as
  part of the category's deletion reconcile.
- [ADR-0007](0007-collection-lifecycle.md) — Collections may reference categories in
  their selector; category rename/move does not auto-update collection selectors.
- [ADR-0008](0008-file-lifecycle.md) — `spec.media[*].fileRef` resolved
  asynchronously (Phase 2, GH#244).

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)
        └─► CategoryTaxonomy (this ADR)
              ├── spec.parentRef → CategoryTaxonomy (self-referencing, same namespace)
              └── spec.media[*].fileRef → File (ADR-0008)   [async, Phase 2]
```

**No circular dependency risk:** Although `CategoryTaxonomy` self-references via
`parentRef`, the tree invariant (cycle prevention) breaks any potential cycle. The
parent reference always points to an existing node or a not-yet-admitted node (resolved
asynchronously); it never creates a structural dependency loop.

## Alternatives considered

### Reject push when parent is missing (synchronous strict validation)

Rejected. This would break single-commit authoring of a full category tree. The
async `ParentResolved=False` pattern allows teams to push a category hierarchy in one
commit without worrying about ordering.

### Store `ancestorPath` in the git frontmatter

Rejected. `ancestorPath` is derived state, not desired state. Committing it to git
would make it author-writable, creating a conflict between authored paths and the
controller-computed canonical paths. It would also make re-parenting require two commits
(one for `parentRef`, one for `ancestorPath`), which is error-prone.

### Expose the admission-time `ancestorPath` as a pre-reconcile fallback

Rejected (spec 057). Clients reached the stale top-level field first. Keeping two sources,
with a precedence rule between them, preserved the confusion it was meant to fix.

### Prefix filtering over a path clustering key (`categories_by_path`)

Rejected in favour of the closure index. One row per category is cheaper to write. But it puts
a namespace's whole taxonomy in one partition, `maxDepth` needs in-memory filtering, and
segment-safe ranges (`a/b/` up to `a/b0`) are easy to get wrong.

### Filter by string prefix of `ancestorPath`

Rejected. `ancestorPath` goes stale after ancestor moves, and a plain string prefix matches
sibling names (`computers` vs `computers-refurb`).

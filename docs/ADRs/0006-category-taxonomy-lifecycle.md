# ADR 0006: CategoryTaxonomy Lifecycle

**Status**: Proposed

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
- `ancestorPath` is controller-managed, not author-written.
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

## Decision

`CategoryTaxonomy` is **Git-backed**. Desired state is a Markdown file with YAML
frontmatter pushed to a repository. `ancestorPath` and status conditions are
controller-managed fields in the datastore.

### Storage classification

| Layer           | Owner                                   |
|-----------------|-----------------------------------------|
| Desired state   | Git frontmatter (Markdown file in repo) |
| Hydrated record | Datastore (ScyllaDB/memDB)              |
| `ancestorPath`  | Datastore; controller-managed           |
| Status          | Datastore; controller-managed           |
| Finalizers      | Datastore; controller-managed           |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.title`,
`spec.parentRef`, `spec.media`, Markdown body.

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.creationTimestamp`,
`metadata.revision`, `metadata.ownerReferences`, `status.*`, `status.ancestorPath`.

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

`createCategoryTaxonomy` commits `categories/<name>.md` to the named repository (or
`gitstore-system`) and delegates to git admission. No direct datastore write.

#### Update

1. Author edits the category file and pushes, or issues `updateCategoryTaxonomy`.
2. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.
3. `spec.parentRef` change (re-parenting): allowed in Phase 1. The controller
   recomputes `ancestorPath` for the node and all its descendants after admission,
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

1. Author deletes the category file and pushes, or issues `deleteCategoryTaxonomy`.
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
6. Once the `blockOwnerDeletion: true` query returns zero results, the controller
   removes the finalizer and the datastore record is hard-deleted. The product
   decouple in step 5 is not a precondition for this step — it can complete
   before, during, or after removal, but MUST eventually converge to
   `CategoryResolved=False`/`CategoryDeleted` (and an orphaned `ownerReferences`
   entry) for every product that referenced the deleted category.

**Move vs delete/recreate:** Moving a category to a different parent is an update to
`spec.parentRef`, not a delete/recreate. The UID is preserved. All descendant
`ancestorPath` values are recomputed by the controller asynchronously after
re-parenting.

### Cycle prevention

| Phase       | Check                                                              | Behaviour on violation                                                                              |
|-------------|--------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------|
| Pre-receive | Self-parent: `parentRef.name == metadata.name`                     | Reject; no record stored                                                                            |
| Admission   | Direct mutual cycle: A→B where B already has `parentRef.name == A` | Both stored with `Acyclic=False`; controller sets error condition                                   |
| Controller  | Deep cycle detection on re-parent (walk ancestor chain up to root) | Sets `Acyclic=False` with reason `CycleDetected`; `ancestorPath` frozen at last known acyclic value |

The controller must not update `ancestorPath` while `Acyclic=False`. Authors must fix
the cycle in git and push a corrected manifest.

### ancestorPath recomputation on parent move

When a category's `spec.parentRef` changes, the controller must:

1. Recompute `ancestorPath` for the moved category.
2. Enumerate all descendants (direct and transitive) by querying the datastore for
   records whose stored `ancestorPath` contains this category's name as a prefix.
3. Recompute and update `ancestorPath` for each descendant.
4. Update `observedGeneration` on each affected record.

This is potentially a wide fan-out for deep hierarchies. The controller must process
descendants level by level to avoid partial path states. Recomputation is idempotent
and level-triggered.

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

| Mutation                 | Phase 1 behaviour                                                                                    |
|--------------------------|------------------------------------------------------------------------------------------------------|
| `createCategoryTaxonomy` | Commits `categories/<name>.md` to the named repository (or `gitstore-system`); waits for admission.  |
| `updateCategoryTaxonomy` | Commits updated manifest; waits for admission.                                                       |
| `deleteCategoryTaxonomy` | Validates no `blockOwnerDeletion: true` dependents (children); adds `foregroundDeletion` finalizer; sets `Terminating`. Assigned products (`blockOwnerDeletion: false`) never block; the controller decouples them asynchronously. |
| `getCategoryTaxonomy`    | Read-only datastore query; includes controller-computed `ancestorPath`.                              |
| `listCategoryTaxonomies` | Read-only datastore query, namespace-scoped; filterable by `ancestorPath` prefix.                    |

No direct datastore write path exists for `CategoryTaxonomy` in Phase 1.

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
| `AncestorPathReady` | `ancestorPath` is up-to-date and reflects the current parent chain.                     |
| `MediaResolved`     | All `spec.media[*].fileRef` entries found (deferred to Phase 2, GH#244).                |
| `Ready`             | Parent resolved, acyclic, ancestor path current.                                        |
| `Terminating`       | `foregroundDeletion` finalizer present; `blockOwnerDeletion: true` dependents (children) must be drained before removal. `blockOwnerDeletion: false` dependents (assigned products) are decoupled asynchronously, not drained, and never gate removal. |

When `AncestorPathReady=False`, queries that filter by ancestor path may return stale
results. This is a transient state during large tree re-parents and resolves within one
controller reconcile pass.

## Consequences

Positive:
- Category hierarchy is fully reviewable through git history.
- Deletion is safe for structural dependents: children block deletion, and
  `ownerReferences`/`blockOwnerDeletion` makes that check an indexed lookup rather
  than a full-table name scan.
- Deletion of a category with assigned products is not blocked, but is not silent
  either: products are decoupled (orphaned `ownerReferences`, `CategoryResolved=
  False`/`CategoryDeleted`) instead of being left pointing at nothing.
- Re-parenting preserves UIDs; cascade `ancestorPath` recomputation is safe.
- Cycle detection is layered: instant self-loop rejection at push, async deep detection
  by controller.
- The `ownerReferences`/`blockOwnerDeletion` mechanism is reusable as-is for the
  File resource's future reverse-reference tracking (ADR-0008, doc 034 Phase 2),
  rather than requiring a second, bespoke dependent-tracking mechanism.

Negative:
- Re-parenting a deep category triggers a potentially large controller fan-out to
  update descendant `ancestorPath` values; reconcile may take seconds for large trees.
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

# ADR 0003: Repository Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, git-service, and deployment authors.

## Context

`Repository` is the git repository declaration that holds all git-backed catalog and
configuration resources. It is the second tier in the control-plane ownership chain:

```
Namespace → Repository → Product / ProductVariant / CategoryTaxonomy / Collection / File
```

No git-backed resource can exist without a containing repository. Repository lifecycle
therefore gates every other catalog ADR.

Like `Namespace`, `Repository` has a bootstrap problem: the `gitstore-system` repository
is created by the API during namespace bootstrap before any git push is possible. All
other repositories are git-backed via the `gitstore-system` repository of the
parent namespace.

The `createRepository` and `deleteRepository` mutations already exist in the API
reference. This ADR formalises their delegation model and adds the finalizer protocol.

## Decision

`Repository` is **hybrid** for the same bootstrap reason as `Namespace`. The
`gitstore-system` repository created by namespace bootstrap is a direct datastore write.
All other repositories are git-backed: their manifests live under
`repositories/<name>.md` inside the namespace's `gitstore-system` repository.

### Storage classification

| Layer         | Owner                                                             |
|---------------|-------------------------------------------------------------------|
| Desired state | Git frontmatter in `gitstore-system` (non-bootstrap repositories) |
| Hydrated record | Datastore (ScyllaDB/memDB)                                      |
| Status        | Datastore; controller-managed                                     |
| Finalizers    | Datastore; controller-managed                                     |

### Lifecycle rules

#### Create

**Bootstrap path (`gitstore-system` only):**

1. Auto-provisioned by namespace bootstrap; no git commit.
2. Record is written directly to the datastore with `source: bootstrap` in the audit
   log.
3. `storageClass: system` is set; this field is immutable after creation.

**Git-backed path (all other repositories):**

1. Author pushes a `Repository` manifest to `gitstore-system` in the owning namespace.
2. Pre-receive validates envelope, name format, `spec.defaultBranch`, and
   `spec.visibility`.
3. Post-receive admission writes the repository record and sets
   `AdmissionAccepted=True`.
4. Controller reconciles: provisions the bare git repository on the git-service
   filesystem, sets `StorageProvisioned=True`, and then `Ready=True`.

**GraphQL mutation path:**

When a caller issues `createRepository` with an explicit namespace and name, the API
commits a `Repository` manifest to `gitstore-system` and waits for admission before
returning. This is the delegation-to-git path — the mutation does not write directly
to the datastore. See the GraphQL mutation delegation table below.

#### Update

Mutable fields: `spec.description`, `spec.visibility`, `spec.defaultBranch`,
`spec.storageClass` (upgrade only, not downgrade).

Immutable fields: `metadata.name`, `metadata.namespace`.

**Rename vs move:** GitStore does not support in-place rename of a repository. A rename
is a delete/recreate that creates a new UID and resets all catalog resource
`ownerReferences`. See the edge-case section below.

**Transfer:** Transferring a repository between namespaces is not supported in Phase 1.
Cross-namespace owner references are disallowed; a transfer would invalidate all
catalog `ownerReferences`. Planned for Phase 2 with a dedicated transfer
operation.

For `updateRepository` mutations: the API commits an updated manifest to
`gitstore-system`. Admission validates the delta and updates the datastore record.

#### Delete

1. Caller issues `deleteRepository` mutation or removes the manifest from
   `gitstore-system`.
2. Before any record is removed, the API checks whether any git-backed catalog
   resources (Product, ProductVariant, CategoryTaxonomy, Collection, File) exist in
   the repository.
   - If catalog resources exist, the delete is **rejected** with
     `FailedPrecondition: catalog resources present`.
3. If the repository is clear, the API adds the `gitstore.dev/foreground-deletion`
   finalizer and sets `spec.deletionTimestamp`.
4. Repository enters `Terminating` status.
5. The controller drains platform records referencing this repository
   (`HydrationRecord`, `AdmissionResult`, `ReconcileJob`, `WatchEvent`) and then
   triggers git-service to archive or remove the bare repository from disk.
6. After the git-service confirms removal, the controller removes the finalizer.
7. Once all finalizers are cleared, the datastore record is hard-deleted.

The `gitstore-system` repository cannot be deleted while the namespace exists. Its
deletion is only permitted as part of namespace finalisation (see
[ADR-0002](0002-namespace-lifecycle.md)).

### Git write path

Canonical file location inside `gitstore-system`:

```
repositories/<name>.md
```

```markdown
---
apiVersion: core.gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme-store
spec:
  defaultBranch: main
  visibility: private
  storageClass: standard
  description: Primary catalog repository.
---

Long-form repository description.
```

### GraphQL mutation delegation

| Mutation                     | Phase 1 behaviour                                                                                                                  |
|------------------------------|------------------------------------------------------------------------------------------------------------------------------------|
| `createRepository`           | Commits `repositories/<name>.md` to `gitstore-system`; waits for admission; returns the hydrated record.                          |
| `updateRepository`           | Commits updated manifest to `gitstore-system`; waits for admission.                                                               |
| `renameRepository`           | **Not supported in Phase 1.** Returns `Unimplemented`. Callers must delete and recreate.                                           |
| `transferRepository`         | **Not supported in Phase 1.** Returns `Unimplemented`.                                                                            |
| `deleteRepository`           | Validates no catalog resources exist; adds `foregroundDeletion` finalizer; sets `Terminating`.                                     |
| `getRepository`              | Read-only datastore query.                                                                                                         |
| `listRepositories`           | Read-only datastore query, namespace-scoped.                                                                                       |

Direct datastore writes are only permitted for the bootstrap `gitstore-system`
repository. All other repositories must flow through git admission.

### Validation and admission rules

| Phase        | Rule                                                                                                       |
|--------------|------------------------------------------------------------------------------------------------------------|
| Pre-receive  | Envelope valid; `kind: Repository`; `metadata.name` format; `spec.defaultBranch` is a valid ref name.     |
| Admission    | Name uniqueness within namespace; `spec.storageClass` is a known value; namespace is `Active` (not `Terminating`). |
| Controller   | Bare git repository provisioned on git-service; `StorageProvisioned=True`; then `Ready=True`.              |

### Status and reconciliation behaviour

| Condition             | Meaning                                                                   |
|-----------------------|---------------------------------------------------------------------------|
| `AdmissionAccepted`   | Repository record written to datastore.                                   |
| `StorageProvisioned`  | Bare git repository exists on git-service filesystem.                     |
| `Ready`               | Repository accepts git pushes.                                            |
| `Suspended`           | Repository is readable but pushes are rejected (operator-set).            |
| `Terminating`         | `foregroundDeletion` finalizer present; catalog resources must drain.     |

`observedGeneration` is updated after each successful reconcile. The controller
re-queues with exponential backoff if `StorageProvisioned=False`.

### Authorization boundaries

- Only principals with `repository:create` capability in the owning namespace may
  create or delete repositories.
- Catalog pushes to a repository are authorised by `repository:push` capability
  (see GH#126 / [ADR-0002 auth cross-ref](0002-namespace-lifecycle.md)).
- The `gitstore-system` repository additionally requires `system:admin` capability for
  direct manifest pushes.

### Edge cases

#### Rename (delete/recreate)

Renaming a repository is semantically a delete followed by a recreate with a new
`metadata.name`. All git-backed resources inside the old repository lose their
`ownerReferences` when the old record is deleted, and the new repository's controller
must reconcile all resources again. Callers should:
1. Create the new repository.
2. Push all catalog resources into the new repository.
3. Delete the old repository after confirming all resources are re-admitted.

#### Namespace entering Terminating while repository exists

If a namespace enters `Terminating` state while a repository is still `Active`, the
namespace controller must reject the finalizer removal until all repositories are
`Terminating` and their finalizers are cleared. The ordering is:

```
Namespace Terminating → Repository Terminating → Repository finalizer removed →
  Namespace finalizer removed
```

#### Repository in Terminating state but git-service unreachable

The controller must retry storage removal with exponential backoff. The repository
remains in `Terminating` status until the git-service confirms removal. The controller
must not remove the finalizer until storage is confirmed absent.

## Consequences

Positive:
- Repository lifecycle is reviewable and auditable for non-bootstrap repositories.
- Deletion is safe: catalog resources must drain before repository disappears.
- The `gitstore-system` convention provides a stable authoring target for namespace-
  and repository-level manifests without requiring an external registry.

Negative:
- Rename is not supported; teams that change repository names must do a full migration.
- Callers that create repositories via mutation must wait for admission and controller
  provisioning before the repository accepts pushes.
- The `gitstore-system` repository is a system concern that operators must understand.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace is the owning resource; namespace
  deletion is blocked until all repositories are drained.
- [ADR-0004 through ADR-0008](0004-product-lifecycle.md) — All catalog resources carry
  `ownerReferences` pointing at the repository record.
- [ADR-0001](0001-secretref-reference-contract.md) — Repository may reference
  `SecretRef` for object-storage credentials via `gitstore-system` configuration.

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (this ADR)
        ├─► Product (ADR-0004)
        ├─► ProductVariant (ADR-0005)
        ├─► CategoryTaxonomy (ADR-0006)
        ├─► Collection (ADR-0007)
        └─► File (ADR-0008)
```

## Alternatives considered

### Repository as fully datastore-only

Rejected. `docs/resources/git-backed.md` classifies `Repository` under the Control
Plane git-backed table. Keeping it datastore-only would diverge the implementation
from the documented architecture and lose reviewability for repository configuration.

### Allow cross-namespace repository references

Rejected. Cross-namespace owner references are disallowed by the architecture
(confirmed by `docs/categories/category-taxonomy-spec.md`: "if the repository or its
namespace cannot be resolved, the push admission is aborted"). Allowing cross-namespace
repository references would require catalog resources to carry unresolvable owner
references and would break the GC chain.

## Open questions

- Should `deleteRepository` accept a `force: true` flag that triggers cascading
  deletion of catalog resources? Phase 1 decision: no. The caller must drain
  catalog resources explicitly to avoid silent data loss.
- Should `Suspended` repositories still allow reads? Phase 1 decision: yes, reads are
  unaffected; only pushes are rejected.

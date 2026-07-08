# ADR 0002: Namespace Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and deployment authors.

## Context

`Namespace` is the tenant boundary that scopes all repositories, catalog resources, and
runtime state. It is the root owner in the control-plane ownership chain:

```
Namespace → Repository → Product / ProductVariant / CategoryTaxonomy / Collection / File
```

Every other lifecycle ADR depends on namespace semantics being stable first.

Two competing forces shape its storage model. Namespace lifecycle is reviewable
desired state and belongs in Git by default — the `docs/resources/git-backed.md`
control-plane table already lists it there. But Namespace is also the container that
holds the git repositories a client pushes to, creating a bootstrap problem: the first
namespace and its repositories must exist before any git push is possible.

Existing API mutations (`createNamespace`, `deleteNamespace`) treat namespace as
datastore-only today. This ADR formalises a model where API mutations delegate to git
while introducing a bootstrap mechanism on API startup.

## Decision

`Namespace` is **hybrid**: at bootstrap time the API ensures the system namespaces (`gitstore-system` and `default`) exist;
all subsequent management is git-backed via the `gitstore-system/gitstore-system` repository that the bootstrap operation auto-provisions.

### Storage classification

| Layer           | Owner                                                                           |
|-----------------|---------------------------------------------------------------------------------|
| Desired state   | Git frontmatter in `gitstore-system/gitstore-system` repository (non-bootstrap) |
| Hydrated record | Datastore (ScyllaDB/memDB)                                                      |
| Status          | Datastore; controller-managed                                                   |
| Finalizers      | Datastore; controller-managed                                                   |

The `gitstore-system` & `default` namespaces and the `gitstore-system` repository it creates are datastore-only
records. All other namespaces are git-backed and should only be created in `gitstore-system/gitstore-system`
repository unless the operator explicitly designates additional bootstrap namespaces.

### Well-known system repository

When a namespace is bootstrapped the API automatically creates a repository named
`gitstore-system` within the new namespace. This repository:

- is created with `spec.storageClass: system` to distinguish it from user repositories;
- is the default target for all GraphQL catalog mutations that do not specify a
  repository name (see [ADR-0004](0004-product-lifecycle.md) through [ADR-0008](0008-file-lifecycle.md));
- holds `Repository` manifests for any non-bootstrap resources managed declaratively.

### Lifecycle rules

#### Create

**Bootstrap path (required for the first namespace):**

1. API checks for the existence of bootstrap namespaces.
2. API creates bootstrap namespaces if it does not exist.
3. API validates name uniqueness, display-name length, tier, limits, and defaults.
4. API writes the namespace record to the datastore.
5. API provisions `gitstore-system` repository inside each bootstrap namespace.
6. No git commit is created for the namespace record itself.

**Git-backed path (all other namespaces):**

1. Author pushes a `Namespace` manifest to `gitstore-system/gitstore-system` repository.
2. Pre-receive validates envelope, name format, and required fields.
3. Pre-receive rejects `Namespace` manifest to any other repository other than `gitstore-system/gitstore-system`.
4. Post-receive admission writes the namespace record and sets
   `AdmissionAccepted=True`.
5. Controller reconciles: checks for conflicts, provisions `gitstore-system` repository in the
   new namespace, and sets `Ready=True`.

#### Update

1. Author edits the `Namespace` manifest in `gitstore-system/gitstore-system` and pushes.
2. Admission re-validates the spec delta and updates the datastore record.
3. Enforced immutable fields: `metadata.name`, `spec.tier` (demotion not allowed in
   Phase 1).

For bootstrap namespaces, `updateNamespace` mutation delegates to the git edit API on the platform operator's system repository.

#### Delete

1. Caller issues `deleteNamespace` mutation or deletes the manifest from git.
2. Before any record is removed, the API checks whether repositories exist in the
   namespace.
   - If repositories exist, the delete is **rejected** with
     `FailedPrecondition: repositories present`.
3. If the namespace is clear, the API sets `metadata.deletionTimestamp` and adds
   the `gitstore.dev/foreground-deletion` finalizer to the datastore record.
4. The namespace enters `Terminating` status.
5. The controller drains any remaining platform records (e.g. `HydrationRecord`,
   `AuditLog` entries, `gitstore-system` repository) and then removes the finalizer.
6. Once all finalizers are removed, the datastore record is hard-deleted.

**Cascade rule:** Repositories must be deleted (and their own finalizers drained) before
namespace deletion can proceed. `deleteNamespace` must never trigger silent cascade
deletion of repositories — the caller must delete repositories explicitly.

> Bootstrap namespaces cannot be deleted.

See [ADR-0003](0003-repository-lifecycle.md) for the Repository finalizer protocol.

### Git write path

For git-backed namespaces, the canonical file location inside `gitstore-system/gitstore-system` is:

```
namespaces/<name>.md
```

```markdown
---
apiVersion: core.gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme-store
spec:
  displayName: Acme Store
  tier: ORGANIZATION
  repositoryDefaults:
    defaultBranch: main
  limits:
    maxRepositories: 100
---

Optional description.
```

The `gitstore-system` repository for the `gitstore-system` namespace is the authoring target,
not a repository inside the new namespace.

### GraphQL mutation delegation

| Mutation          | Phase 1 behaviour                                                                                                        |
|-------------------|--------------------------------------------------------------------------------------------------------------------------|
| `createNamespace` | Bootstrap: Not applicable. Non-bootstrap: commits manifests to `gitsore-system/gitstore-system` and waits for admission. |
| `updateNamespace` | Commits an updated manifest to `gitstore-system/gitstore-system`; returns the resource version after admission.          |
| `deleteNamespace` | Validates no repositories exist, then adds `foregroundDeletion` finalizer and sets `Terminating`.                        |
| `listNamespaces`  | Read-only datastore query. No git delegation.                                                                            |
| `getNamespace`    | Read-only datastore query. No git delegation.                                                                            |

Direct datastore writes that bypass git are only permitted for the bootstrap namespaces
and for operator-level administrative operations explicitly marked with `source: bootstrap` in the audit log.

### Validation and admission rules

| Phase       | Rule                                                                                                                                      |
|-------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive | Envelope valid; `kind: Namespace`; `metadata.name` format; `spec.tier` is a known value; Accept only in `gitstore-system/gitstore-system` |
| Admission   | Name uniqueness within the platform; `spec.limits` values within operator-configured maximums; `spec.tier` downgrade rejected.            |
| Controller  | `Ready=True` after `gitstore-system` is provisioned and reachable.                                                                        |

### Status and reconciliation behaviour

| Condition           | Meaning                                                              |
|---------------------|----------------------------------------------------------------------|
| `AdmissionAccepted` | Namespace record written to datastore.                               |
| `SystemRepoReady`   | `gitstore-system` repository exists and is ready.                    |
| `Ready`             | Namespace is fully operational.                                      |
| `Terminating`       | `foregroundDeletion` finalizer present; repositories must drain.     |

`observedGeneration` is set after each successful reconcile pass. The controller re-queues if `SystemRepoReady=False`.

## Consequences

Positive:
- Bootstrap path is preserved: the first namespace needs no git repository.
- Subsequent namespace management is reviewable, auditable, and reversible.
- Deletion is safe: repositories must be drained before the namespace disappears.

Negative:
- Two code paths exist for create (bootstrap vs git-backed); they must stay in sync.
- The bootstrap namespace cannot be rolled back through Git history.
- Phase 1 callers creating non-bootstrap namespaces via mutation must wait for
  admission to complete before the namespace is usable.

## Cross-references

- [ADR-0003](0003-repository-lifecycle.md) — Repository is the immediate child; its
  deletion lifecycle is the gating condition for namespace deletion.
- [ADR-0004 through ADR-0008](0004-product-lifecycle.md) — All catalog resources are
  namespace-scoped through their parent repository.
- [ADR-0001](0001-secretref-reference-contract.md) — `SecretRef` resolution uses
  namespace as part of the resolution identity.

## Alternatives considered

### Namespace as datastore-only (keep current status quo)

Rejected for non-bootstrap namespaces. The docs already classify Namespace as
git-backed under the Control Plane section of `git-backed.md`. Keeping it
datastore-only would permanently diverge the architecture from the documented model
and lose reviewability for namespace configuration changes.

### Namespace as fully git-backed with no bootstrap exception

Rejected. The chicken-and-egg bootstrap problem has no clean solution without a
meta-namespace or operator-owned system repository that requires additional
bootstrapping infrastructure. The hybrid model is the least risky assumption for
Phase 1. The bootstrap path can be deprecated or wrapped once the platform matures.

## Open questions

- Should sub-namespaces push to the *parent's* `gitstore-system`, or to a
  dedicated `namespaces/` repository? The current decision (parent's `gitstore-system`)
  is simplest for Phase 1; a dedicated repository hierarchy is a Phase 2 option.
- Should the `createNamespace` mutation accept a `waitForReady` option that blocks
  until `SystemRepoReady=True`, or should callers poll?

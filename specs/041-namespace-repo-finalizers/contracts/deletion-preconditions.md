# Contract: Namespace/Repository Deletion Preconditions and System Repository Bootstrap

**Feature**: [../spec.md](../spec.md) | **Data model**: [../data-model.md](../data-model.md)

No GraphQL schema changes. This contract documents the behavior change to three
existing mutations' resolver-level semantics; the wire-level input/output types
(`DeleteNamespaceInput`/`DeleteNamespacePayload`,
`DeleteRepositoryInput`/`DeleteRepositoryPayload`,
`CreateNamespaceInput`/`CreateNamespacePayload` in
`shared/schemas/namespace.graphqls` and `shared/schemas/repository.graphqls`) are
unchanged.

## `deleteNamespace`

**Existing schema docstring** (already documents the intended behavior; this feature
makes it true):
> Deletion is blocked when the namespace contains repositories.

### Preconditions checked (new)

1. Caller-supplied `identifier` resolves to an existing namespace the caller is
   authorized to delete (unchanged — existing `AuthorizedNamespaceForDeletion`
   check).
2. **New**: `HasRepositories(ctx, namespace.ID)` returns `false`.

### Behavior

| Condition | Result |
|---|---|
| Namespace not found / caller not authorized | Unchanged existing error (`gqlerror.Errorf("namespace deletion authorization context is missing")` or existing not-found path) |
| Namespace found, `HasRepositories` = `true` | **New**: mutation returns error, namespace and all repositories unchanged. Error message: `namespace %q contains repositories and cannot be deleted` (existing message text at `service.go:318` — behavior now actually enforced instead of unreachable) |
| Namespace found, `HasRepositories` = `false` | Existing success path: `s.store.DeleteNamespace(ctx, ns.ID)` proceeds; payload returns `deletedIdentifier` |

### Test cases (contract level)

- Given a namespace with 1 repository, `deleteNamespace` returns an error; a
  subsequent `namespace(by: {identifier: ...})` query still finds the namespace.
- Given a namespace with 0 repositories, `deleteNamespace` succeeds; a subsequent
  `namespace(by: {identifier: ...})` query returns `null`.
- Given a namespace with repositories that are themselves later deleted down to 0,
  a subsequent `deleteNamespace` call succeeds.

## `deleteRepository`

**Existing schema docstring** (generic; this feature adds the precondition it was
missing):
> Delete a repository and its storage.

### Preconditions checked (new)

1. Caller-supplied `repositoryId` resolves to an existing repository (unchanged —
   existing `s.store.GetRepository` lookup).
2. **New**: `HasCatalogResources(ctx, repoID)` returns `false`.

### Behavior

| Condition | Result |
|---|---|
| Repository not found | Unchanged existing error (`gqlerror.Errorf("repository not found")`) |
| Repository found, `HasCatalogResources` = `true` | **New**: mutation returns error, repository storage and metadata unchanged, all catalog resources unchanged. Error message (new): `repository %q contains catalog resources and cannot be deleted`, matching the style of the existing namespace-deletion rejection message |
| Repository found, `HasCatalogResources` = `false` | Existing success path: `s.gitWriter.DeleteRepository` → `s.store.DeleteNamespaceMapping` → `s.store.DeleteRepository` proceeds unchanged; payload returns `deletedRepositoryId` |

### Test cases (contract level)

- Given a repository with 1 admitted `CategoryTaxonomy` (or Product, ProductVariant,
  or Collection), `deleteRepository` returns an error; the repository's storage,
  metadata, and the catalog resource all remain queryable afterward.
- Given a repository with 0 catalog resources, `deleteRepository` succeeds; storage
  is removed and the repository is no longer queryable.
- Given a repository that had catalog resources which were all subsequently removed
  (e.g., deleted via git push), a later `deleteRepository` call succeeds.
- Given `HasCatalogResources` returns `true`, verify none of `s.gitWriter.DeleteRepository`,
  `s.store.DeleteNamespaceMapping`, or `s.store.DeleteRepository` are called (the
  precondition check must short-circuit before any mutation, not just before the
  final commit step).

## `createNamespace`

**Existing schema docstring**: unchanged (no docstring update needed — creating the
system repository is an internal implementation detail, not a caller-visible input
change).

### Behavior (new)

| Step | Result |
|---|---|
| Namespace record created successfully (unchanged) | Proceed to system repository provisioning (new) |
| System repository does not yet exist for this namespace | **New**: `gitstore-system` repository is created for the namespace as part of this call |
| System repository already exists for this namespace (retry case) | **New**: treated as success (idempotent no-op), per research.md Decision 4 — no error surfaced, no duplicate repository created |
| Namespace record creation itself fails (e.g., `ErrAlreadyExists` on identifier) | Unchanged existing behavior — error returned before system repository provisioning is ever attempted |

### Test cases (contract level)

- Given a new, unique namespace identifier, `createNamespace` succeeds and an
  immediate `repositories(namespaceId: ...)` query lists exactly one repository
  matching the well-known system repository name.
- Given `createNamespace` is called twice for the same identifier (second call
  expected to fail on the existing identifier-uniqueness check), the system
  repository provisioning path for the first, successful call is unaffected — this
  test guards against provisioning logic being incorrectly triggered on the failed
  second attempt.
- (Retry simulation) Given a namespace already has its system repository
  provisioned, directly invoking the provisioning step again (simulating a retried
  partial-creation) does not create a second, conflicting repository and does not
  return an error to the namespace-creation caller.

## Cross-cutting: error shape consistency

Both new `FailedPrecondition`-style rejections (namespace-has-repositories,
repository-has-catalog-resources) use the existing `gqlerror.Errorf(...)` mechanism
already used throughout `service.go` and `*.resolvers.go` — no new error type,
extension code, or GraphQL error classification is introduced. This is consistent
with every other rejection path in the existing mutation resolvers (e.g. the
existing namespace-authorization-missing error, the existing repository-not-found
error).

# Quickstart: Repository Git-Backed Lifecycle, Admission, and Reconciler

## Test-first implementation order

1. **Rust** (`gitstore-git-service`): add a failing pre-receive test asserting a `Repository`-kind manifest pushed to a repository other than the target namespace's own `gitstore-system` is rejected, and a second test asserting a manifest whose `metadata.namespace` disagrees with the push repository's owning namespace is rejected. Implement the rule until it passes.
2. **Go admission** (`gitstore-api/internal/cataloggrpc`): add failing tests for the new `"Repository"` dispatch case — create, update of mutable fields, immutable-field-change rejection (`metadata.name`/`metadata.namespace`), `spec.storageClass` downgrade rejection, bootstrap-name rejection, and rejection when the owning namespace does not exist or is `Terminating`. Implement `admitRepository` until green, reusing `repository_contract.go`'s existing `NormalizeRepositoryContract`/`AdvanceRepositorySpecVersion`/`AdvanceRepositorySystemVersion` unchanged.
3. **Go resolvers** (`gitstore-api/internal/graph/resolver`): add failing tests for `CreateRepository` committing via `GitWriter.CommitFile` and awaiting admission, for a new `UpdateRepository` doing the same for mutable-field changes, and for both rejecting bootstrap-repository-name targets. Add failing tests for `RenameRepository`/`TransferRepository` now returning `Unimplemented` unconditionally, with zero record/manifest mutation. Add failing tests for `DeleteRepository` setting `DeletionTimestamp`/`Finalizers` instead of hard-deleting, and for redundant delete requests against an already-`Terminating` repository. Implement until green.
4. **GraphQL schema** (`shared/schemas/repository.graphqls`): add `updateRepository`, `UpdateRepositoryInput`/`UpdateRepositoryPayload`, and the envelope-shaped `CreateRepositoryInput`/`MetadataInput`/`RepositorySpecInput`. `MetadataInput` is shared by declarative mutation envelopes; its `apiVersion` and `kind` envelope companions default to `gitstore.dev/v1beta1` and `Repository`. Add a failing schema-introspection test asserting those defaults and that `renameRepository`/`transferRepository` are `@deprecated` with a non-empty reason, then add `@deprecated(reason: "...")` (citing ADR-0003's Phase 2 deferral) to both fields' existing definitions; regenerate gqlgen code (never hand-edited).
5. **Repository watch** (`gitstore-api/internal/watchjournal`, datastore backends, GraphQL, and security): write failing tests first for Repository CDC classification, journal ordering/recovery/fencing, bootstrap `BOOKMARK`, typed/generic parity, `repository.watch` authorization before cursor disclosure, `WATCH_EXPIRED`, and two-replica resume. Add the Repository-specific CDC/journal migration and backend adapters by extending the shipped spec-050 substrate; do not use process-local event history.
6. **Go controller-manager** (`gitstore-controller-manager/internal/repository` and `internal/listwatch/repository_listwatcher.go`): add failing bootstrap/list/drain, resume, expiry-relist, and idempotent reconciliation tests. The reconciler provisions the bare Git repository, sets `StorageProvisioned`/`Ready`, removes the finalizer only after the drain and storage checks, and retries with backoff. Register its `ListWatcher`/`Runner`/checkpoint/cache in `cmd/controller/main.go`.
7. **Integration**: add end-to-end coverage spanning push → admission → read, mutation → commit → admission → read, create → update → delete → `Terminating` → removed, Repository watch cross-replica/recovery behavior, and `renameRepository`/`transferRepository` → `Unimplemented` + `@deprecated` introspection.
8. **Status mutation**: add failing controller-authorization, partial-merge, spec-write rejection, and stale-resourceVersion tests for `updateRepositoryStatus`; stale writes must return a GraphQL `RESOURCE_VERSION_CONFLICT` error carrying `currentResourceVersion` in extensions, never a conflict payload.

## Two-replica lifecycle and capacity gate

Run the Repository-specific deployment gate only against two distinct API
processes, two distinct controller-manager processes, and their shared Scylla
journal. The replacement trigger must be watched by the deployment harness and
must replace the selected API process in place:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=production \
  REPOSITORY_API_A=http://localhost:4000 \
  REPOSITORY_API_B=http://localhost:4001 \
  REPOSITORY_CONTROLLER_A=http://localhost:5001 \
  REPOSITORY_CONTROLLER_B=http://localhost:5002 \
  REPOSITORY_API_REPLACEMENT=http://localhost:4001 \
  REPOSITORY_REPLACEMENT_TRIGGER_FILE=/tmp/gitstore-repository-replace \
  REPOSITORY_TOKEN_FILE=/path/to/untracked/token \
  CAPACITY_OBSERVABILITY=prometheus \
  CAPACITY_PROMETHEUS_TARGETS=host.docker.internal:4000,host.docker.internal:4001 \
  CAPACITY_CONFIG_MANIFEST=/path/to/config-manifest.json \
  CAPACITY_ENVIRONMENT_MANIFEST=/path/to/environment-manifest.json
```

Production evidence enforces the full 60-minute, 1,000-subscriber,
10,000-event replay, 1,000-transition overflow, burst, resource, and recovery
contract. A diagnostic run is useful while assembling the deployment but is
not evidence for T035/T037.

## Manual verification

```bash
# 1. Start the stack
make dev   # or: make compose

# 2. Confirm a namespace's bootstrap repository exists
# (via GraphQL) query { repository(by: { namespacePath: { namespace: "acme-store", name: "gitstore-system" } }) { metadata { name } status { conditions { type status } } } }

# 3. Create a repository via git push
git clone <acme-store/gitstore-system clone URL> /tmp/gs-acme-system
cd /tmp/gs-acme-system
mkdir -p repositories
cat > repositories/catalog.md <<'EOF'
---
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme-store
spec:
  defaultBranch: main
  visibility: private
  storageClass: standard
---
EOF
git add repositories/catalog.md
git commit -m "Add catalog repository"
git push

# 4. Confirm admission and read the result
# query { repository(by: { namespacePath: { namespace: "acme-store", name: "catalog" } }) { spec { defaultBranch visibility } status { conditions { type status } } metadata { generation resourceVersion } } }

# 5. Create a repository via mutation instead (no manual git push)
# mutation {
#   createRepository(input: {
#     apiVersion: "gitstore.dev/v1beta1"
#     kind: "Repository"
#     metadata: { name: "media", namespace: "acme-store" }
#     spec: { defaultBranch: "main", visibility: PRIVATE, storageClass: "standard" }
#   }) {
#     repository { metadata { name generation resourceVersion } }
#   }
# }

# 6. Update a repository's mutable fields via mutation
# mutation {
#   updateRepository(input: {
#     apiVersion: "gitstore.dev/v1beta1"
#     kind: "Repository"
#     metadata: { name: "media", namespace: "acme-store" }
#     spec: { defaultBranch: "main", visibility: PRIVATE, storageClass: "premium" }
#   }) {
#     repository { metadata { generation resourceVersion } spec { storageClass } }
#   }
# }

# 7. Confirm renameRepository/transferRepository now return Unimplemented
#    AND are marked @deprecated in the schema (both ship together)
# mutation { renameRepository(input: { repositoryId: "<id>", newName: "renamed" }) { repository { metadata { name } } } }
#   -> expect an Unimplemented error; repository name unchanged
# mutation { transferRepository(input: { repositoryId: "<id>", targetNamespaceId: "<other-ns-id>" }) { repository { metadata { namespace } } } }
#   -> expect an Unimplemented error; repository namespace unchanged
# (via introspection) query { __type(name: "Mutation") { fields(includeDeprecated: true) { name isDeprecated deprecationReason } } }
#   -> expect renameRepository/transferRepository: isDeprecated=true, deprecationReason citing ADR-0003 Phase 2
#   -> this is the deliberate supersession of spec 045's Acceptance Scenario #4/SC-003 for these two mutations only

# 8. Delete an empty repository and observe Terminating
# mutation { deleteRepository(input: { repositoryId: "<id>" }) { deletedRepositoryId } }
# query { repository(by: { id: "<id>" }) { status { conditions { type status } } } }   # Terminating=True until the controller confirms storage removal and removes the finalizer

# 9. Verify the Repository controller's canonical durable watch
# subscription { watchRepositories(namespace: "acme-store") { type namespace name resourceVersion repository { id metadata { uid resourceVersion } } } }
# Use the controller's ListWatcher bootstrap/list/drain path for an initial
# view; on WATCH_EXPIRED discard the checkpoint/cache and bootstrap again.
```

## Expected query shape (post-admission, fully reconciled)

```json
{
  "repository": {
    "spec": { "defaultBranch": "main", "visibility": "PRIVATE", "storageClass": "standard" },
    "status": {
      "conditions": [
        { "type": "AdmissionAccepted", "status": "TRUE" },
        { "type": "StorageProvisioned", "status": "TRUE" },
        { "type": "Ready", "status": "TRUE" }
      ]
    },
    "metadata": { "generation": 1, "resourceVersion": "2" }
  }
}
```

# Quickstart: Repository Git-Backed Lifecycle, Admission, and Reconciler

## Test-first implementation order

1. **Rust** (`gitstore-git-service`): add a failing pre-receive test asserting a `Repository`-kind manifest pushed to a repository other than the target namespace's own `gitstore-system` is rejected, and a second test asserting a manifest whose `metadata.namespace` disagrees with the push repository's owning namespace is rejected. Implement the rule until it passes.
2. **Go admission** (`gitstore-api/internal/cataloggrpc`): add failing tests for the new `"Repository"` dispatch case — create, update of mutable fields, immutable-field-change rejection (`metadata.name`/`metadata.namespace`), `spec.storageClass` downgrade rejection, bootstrap-name rejection, and rejection when the owning namespace does not exist or is `Terminating`. Implement `admitRepository` until green, reusing `repository_contract.go`'s existing `NormalizeRepositoryContract`/`AdvanceRepositorySpecVersion`/`AdvanceRepositorySystemVersion` unchanged.
3. **Go resolvers** (`gitstore-api/internal/graph/resolver`): add failing tests for `CreateRepository` committing via `GitWriter.CommitFile` and awaiting admission, for a new `UpdateRepository` doing the same for mutable-field changes, and for both rejecting bootstrap-repository-name targets. Add failing tests for `RenameRepository`/`TransferRepository` now returning `Unimplemented` unconditionally, with zero record/manifest mutation. Add failing tests for `DeleteRepository` setting `DeletionTimestamp`/`Finalizers` instead of hard-deleting, and for redundant delete requests against an already-`Terminating` repository. Implement until green.
4. **GraphQL schema** (`shared/schemas/repository.graphqls`): add `updateRepository`, `UpdateRepositoryInput`/`UpdateRepositoryPayload`, and the envelope-shaped `CreateRepositoryInput`/`RepositoryMetadataInput`/`RepositorySpecInput`; regenerate gqlgen code (never hand-edited).
5. **Go controller-manager** (`gitstore-controller-manager/internal/repository`): add failing reconciler tests — provisions the bare Git repository on git-service, sets `StorageProvisioned`/`Ready`, removes the finalizer once `HasCatalogResources` is false **and** storage removal is confirmed, retries with backoff while git-service is unreachable. Implement until green. Register in `cmd/controller/main.go`.
6. **Integration**: add end-to-end coverage spanning push → admission → read, mutation → commit → admission → read (both `createRepository` and `updateRepository`), create → update → delete → `Terminating` → removed, and `renameRepository`/`transferRepository` → `Unimplemented` regression coverage.

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
apiVersion: core.gitstore.dev/v1beta1
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
#     apiVersion: "core.gitstore.dev/v1beta1"
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
#     apiVersion: "core.gitstore.dev/v1beta1"
#     kind: "Repository"
#     metadata: { name: "media", namespace: "acme-store" }
#     spec: { defaultBranch: "main", visibility: PRIVATE, storageClass: "premium" }
#   }) {
#     repository { metadata { generation resourceVersion } spec { storageClass } }
#   }
# }

# 7. Confirm renameRepository/transferRepository now return Unimplemented
# mutation { renameRepository(input: { repositoryId: "<id>", newName: "renamed" }) { repository { metadata { name } } } }
#   -> expect an Unimplemented error; repository name unchanged
# mutation { transferRepository(input: { repositoryId: "<id>", targetNamespaceId: "<other-ns-id>" }) { repository { metadata { namespace } } } }
#   -> expect an Unimplemented error; repository namespace unchanged

# 8. Delete an empty repository and observe Terminating
# mutation { deleteRepository(input: { repositoryId: "<id>" }) { deletedRepositoryId } }
# query { repository(by: { id: "<id>" }) { status { conditions { type status } } } }   # Terminating=True until the controller confirms storage removal and removes the finalizer
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

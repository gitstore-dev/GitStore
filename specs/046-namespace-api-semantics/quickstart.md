# Quickstart: Namespace API Semantics: Spec Writes, Status Updates, Concurrency

## Test-first implementation order

1. **Rust** (`gitstore-git-service`): add a failing pre-receive test asserting a `Namespace`-kind manifest pushed to a repository other than `gitstore-system/gitstore-system` is rejected. Implement the rule until it passes.
2. **Go datastore** (`gitstore-api/internal/datastore`): add failing tests for `NormalizeNamespaceContract`/`AdvanceNamespaceSpecVersion`/`AdvanceNamespaceSystemVersion` and for `UpdateNamespace`'s optimistic-concurrency conflict behavior (memdb and Scylla). Add the new columns, the memdb schema entry, and `004_namespace_resource_contract.cql`. Implement until green.
3. **Go admission** (`gitstore-api/internal/cataloggrpc`): add failing tests for the new `"Namespace"` dispatch case — create, update, tier-demotion rejection, bootstrap-name rejection. Implement `admitNamespace` until green.
4. **Go resolvers** (`gitstore-api/internal/graph/resolver`): add failing tests for `CreateNamespace`/`UpdateNamespace` committing via `GitWriter.CommitFile` and awaiting admission, and for both rejecting bootstrap-namespace targets. Add failing tests for `DeleteNamespace` setting `DeletionTimestamp`/`Finalizers` instead of hard-deleting, and for redundant delete requests against an already-`Terminating` namespace. Implement until green.
5. **Go startup**: add a failing test asserting `gitstore-system`/`default` exist with their system repositories after startup, idempotently on repeated runs. Implement the bootstrap step until green.
6. **Go controller-manager** (`gitstore-controller-manager/internal/namespace`): add failing reconciler tests — provisions the per-namespace system repo, sets `SystemRepoReady`/`Ready`, removes the finalizer once `HasRepositories` is false. Implement until green. Register in `cmd/controller/main.go`.
7. **Integration**: add end-to-end coverage spanning push → admission → read, mutation → commit → admission → read, and create → update → delete → `Terminating` → removed.

## Manual verification

```bash
# 1. Start the stack
make dev   # or: make compose

# 2. Confirm bootstrap namespaces exist
# (via GraphQL) query { namespace(by: {name: "gitstore-system"}) { metadata { name } status { conditions { type status } } } }
# (via GraphQL) query { namespace(by: {name: "default"}) { metadata { name } } }

# 3. Create a namespace via git push
git clone <gitstore-system/gitstore-system clone URL> /tmp/gs-system
cd /tmp/gs-system
mkdir -p namespaces
cat > namespaces/acme-store.md <<'EOF'
---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme-store
spec:
  title: Acme Store
  tier: USER
---
EOF
git add namespaces/acme-store.md
git commit -m "Add acme-store namespace"
git push

# 4. Confirm admission and read the result
# query { namespace(by: {name: "acme-store"}) { spec { title tier } status { conditions { type status } } metadata { generation resourceVersion } } }

# 5. Create a namespace via mutation instead (no manual git push)
# mutation { createNamespace(input: {identifier: "beta-store", tier: USER}) { namespace { metadata { name generation resourceVersion } } } }

# 6. Delete an empty namespace and observe Terminating
# mutation { deleteNamespace(input: {identifier: "beta-store"}) { deletedIdentifier } }
# query { namespace(by: {name: "beta-store"}) { status { conditions { type status } } } }   # Terminating=True until the controller removes the finalizer
```

## Expected query shape (post-admission, fully reconciled)

```json
{
  "namespace": {
    "spec": { "title": "Acme Store", "tier": "USER" },
    "status": {
      "conditions": [
        { "type": "AdmissionAccepted", "status": "TRUE" },
        { "type": "SystemRepoReady", "status": "TRUE" },
        { "type": "Ready", "status": "TRUE" }
      ]
    },
    "metadata": { "generation": 1, "resourceVersion": "2" }
  }
}
```

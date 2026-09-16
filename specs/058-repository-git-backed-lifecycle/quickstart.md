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

The checked-in laptop alpha harness builds release images and starts two
distinct API processes, one singleton Git-service process, two concurrently
active controller-manager processes, and a shared three-node Scylla journal. It also
generates the sanitized manifests and token, replaces API B when the verifier
requests it, captures the evidence, and removes the isolated stack:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=alpha
```

This is the only public interface for the managed laptop deployment. It owns
startup, readiness, token bootstrap, replacement, evidence capture, and
cleanup through the shared `compose.capacity.yml` capacity overlay. Production
mode remains an externally managed deployment, configured through the
canonical dispatcher variables below.

The singleton Git service owns the shared bare-repository volume; this gate
does not claim Git sharding, replication, or high availability. Both
controller managers reconcile concurrently. The API does not lease or fence
controller replicas: repeated work is safe because reconciliation is
level-triggered and idempotent, while status/finalizer writes use optimistic
resource-version concurrency. The Scylla LWT lease and fencing described by
the watch design applies to CDC journal materialization, not controller
ownership.

Alpha is the laptop-safe evidence tier: at least 10 minutes, 100 subscribers,
1,000 replay events, 5 replay samples, a 20-resource pool, 256 overflow
transitions (exceeding both 64-event buffers), bursts of 20, and a 500ms
transition interval. The slow-consumer probe waits 31 seconds after admission,
exceeding the configured 30-second backpressure bound, before it reads the
terminal overflow error. It requires
visibility p95 ≤2 seconds and p99 ≤3 seconds. It still requires the real
two-API/two-controller topology, an observed API-B replacement, cursor-resumed
recovery, and zero lost acknowledged transitions; duplicate delivery remains
permitted and idempotent. Alpha is valid alpha evidence; it is not production
soak evidence.

Because alpha measures a cold-start 10-minute process, every API must satisfy
both retained-RSS gates: less than 75% growth and less than 256 MiB absolute
RSS. Production retains the less-than-10% RSS-growth gate. Normalized API CPU
must remain below 80% in both tiers.

Run the production tier only on hardware sized for the full gate. It uses the
same real topology and correctness/replacement requirements, but raises the
minimums to 60 minutes, 1,000 subscribers, 10,000 replay events, 20 replay
samples, a 50-resource pool, 1,000 overflow transitions, and bursts of 100,
with a 100ms transition interval, visibility p95 ≤1 second, and p99 ≤3 seconds.
Configure an externally managed deployment with the canonical dispatcher:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=production \
  REPOSITORY_API_A=http://localhost:4000 \
  REPOSITORY_API_B=http://localhost:4001 \
  REPOSITORY_OVERFLOW_API=http://localhost:4002 \
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
10,000-event replay, 1,000-transition overflow, burst, resource, replacement,
and recovery contract. A diagnostic run is useful while assembling the
deployment but is not evidence for T035/T037. Alpha can provide the laptop-safe
two-replica lifecycle evidence, but it must never be reported as a production
soak pass.

`REPOSITORY_OVERFLOW_API` may equal API A when the network path applies normal
TCP backpressure. Behind a buffering proxy, use a reader-only API replica on
the same datastore/journal and configure that replica with
`watch.namespace.subscriber_buffer=1` and
`watch.namespace.subscriber_backpressure_millis=1`. Keep API A and API B at
their production settings so the load and latency measurements remain valid.

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

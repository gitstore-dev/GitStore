# Plan: Make `deleteResource`'s lookup-then-delete atomic

**Status**: 📝 Plan only — no implementation yet
**Type**: Bug fix / hardening of existing delete behavior (not new capability), per this
repo's convention that `/plan` — not `/speckit.specify` + `/speckit.plan` — is the right
artifact for hardening work. No `spec.md` exists for this item and none is being created;
this document is deliberately plan-shaped, not spec-shaped.
**Source**: item #5 of the 2026-08-29 deflake bug backlog memory
(`project_deflake_bug_backlog.md`), a Codex P1 finding on PR #393.

## 1. Problem statement

`gitstore-api/internal/cataloggrpc/server.go`'s `deleteResource` (line ~979) does a plain
read (`lookupResourceByIdentity`, line 980) and then, after an ownership guard added in
PR #393 that only reads fields off the *same* already-fetched `existing` value, calls an
unconditional delete-by-UID:

```go
case *datastore.Product:
    uid = r.UID
    deleteErr = s.store.DeleteProduct(ctx, r.UID)          // line 1027
...
case *datastore.Collection:
    uid = r.UID
    deleteErr = s.store.DeleteCollection(ctx, r.UID)       // line 1065
case *datastore.ProductVariant:
    uid = r.UID
    deleteErr = s.store.DeleteProductVariant(ctx, r.UID)   // line 1068
...
case *datastore.File:
    uid = r.UID
    deleteErr = s.store.DeleteFile(ctx, r.UID)             // line 1075
```

`DeleteProduct`/`DeleteCollection`/`DeleteProductVariant`/`DeleteFile` take only a UID —
no version precondition. Across `gitstore-api` replicas, another admission (same code
path, different push) can re-admit the same UID between `deleteResource`'s read and its
delete call — e.g. re-owning it under a different `RepositoryID`/`GitRef`, or updating its
spec — and the delete still unconditionally removes the now-differently-owned row. The
ownership guard added in PR #393 (lines 1009-1020) does not close this window: it only
re-checks fields on the *stale* `existing` snapshot, not a fresh read taken atomically with
the delete.

This is pre-existing behavior (not introduced by PR #393), confirmed still present as of
this session against `main`.

### Why this is not already covered by the Scylla backend's own CAS calls

The Scylla implementations of `DeleteProduct`/`DeleteCollection`/`DeleteProductVariant`/
`DeleteFile` (`gitstore-api/internal/datastore/scylla/backend.go`, `scylla/file.go`) already
re-read the row at the top of the call (e.g. `p, err := s.GetProduct(ctx, uid)`) and pass
`p.ResourceVersion` — the version from *that* fresh read — into an `IF resource_version=?`
lightweight transaction (`deleteAuthoritative`, `backend.go:1891`). That protects against a
write racing the delete's *own* internal multi-step mutation sequence (name/UID index
cleanup, owner-reference sync), which is the concern `executeDelete`'s
apply/compensate machinery exists for. It does **not** protect against the actual race in
this bug report, which spans the gap between `deleteResource`'s read (used for the
ownership decision) and the call into `DeleteProduct` — by the time `DeleteProduct` runs,
it re-reads fresh and conditions against *that* row, silently accepting whatever a
concurrent admission wrote in between. The fix must thread the resourceVersion
**observed by `deleteResource`** through to the delete, not let the delete backend pick a
fresher one for itself.

### The correct pattern already exists in this codebase — twice

- `datastore.CategoryTaxonomyDeletionStore.MarkCategoryTaxonomyDeletion(ctx, namespace,
  name, expectedResourceVersion string, at time.Time) (*CategoryTaxonomy, error)`
  (`datastore.go:145`): takes the caller's observed `resourceVersion`, and both backends
  reject with `datastore.ErrConflict` if the stored value has since moved.
- `Datastore.DeleteNamespaceWithResourceVersion(ctx, uid, expectedResourceVersion string)
  error` (`datastore.go:356`) is an even closer precedent: it is exactly "conditional
  delete-by-UID, keyed on a caller-observed resourceVersion," already implemented in both
  backends, sitting right alongside the existing unconditional `DeleteNamespace`. This is
  the pattern this plan proposes to replicate for Product/Collection/ProductVariant/File.

  - memdb (`memdb/backend.go:918-936`):
    ```go
    func (m *memdbDatastore) DeleteNamespaceWithResourceVersion(_ context.Context, uid, expectedResourceVersion string) error {
        txn := m.db.Txn(true)
        raw, _ := txn.First("namespaces", "id", uid)
        if raw == nil {
            txn.Abort()
            return fmt.Errorf("%w: namespace uid %s", datastore.ErrNotFound, uid)
        }
        current := raw.(*datastore.Namespace)
        if current.ResourceVersion != expectedResourceVersion {
            txn.Abort()
            return datastore.ErrConflict
        }
        if err := txn.Delete("namespaces", raw); err != nil { ... }
        txn.Commit()
        return nil
    }
    ```
  - Scylla (`scylla/backend.go:2079-2100`): fetches the row, cleans up its secondary
    indexes (with restore-on-failure), then does
    `"DELETE FROM namespaces_by_uid WHERE uid=? IF resource_version=?"` via
    `ExecCASRelease()`, returning `datastore.ErrConflict` (and restoring the indexes it
    just tore down) if `!applied`.

Both backends' `ErrConflict` sentinel matches `MarkCategoryTaxonomyDeletion`'s and every
other optimistic-concurrency write in this datastore (`UpdateNamespace`, `UpdateRepository`,
`UpdateCategoryTaxonomyStatus`, `ApplyFileStatusPatch`, `ApplyNamespaceStatusPatch`, all
returning the same `datastore.ErrConflict`).

## 2. New datastore method signatures

Naming convention already established by `DeleteNamespaceWithResourceVersion`: append
`WithResourceVersion` to the existing unconditional method name rather than inventing a new
verb or a generic options struct. Add four new `Datastore` interface methods, each additive
alongside its existing unconditional sibling (no existing method signature changes):

```go
// Product operations
DeleteProductWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

// Collection operations
DeleteCollectionWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

// ProductVariant operations
DeleteProductVariantWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error

// File operations
DeleteFileWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error
```

All four:
- Return `datastore.ErrNotFound` if no row with `uid` exists (matches every other
  `Get`/`Delete` method's behavior).
- Return `datastore.ErrConflict` if a row exists but its current `ResourceVersion` does not
  equal `expectedResourceVersion` — **and leave the row untouched** (no partial delete).
- Otherwise perform exactly the same deletion work (index cleanup, owner-reference
  projection sync for Product/File) as the existing unconditional method, just gated by the
  version check.

`CategoryTaxonomy` is explicitly out of scope here — it already has the equivalent
`MarkCategoryTaxonomyDeletion` and is not part of this backlog item.

## 3. `deleteResource` changes (`gitstore-api/internal/cataloggrpc/server.go`)

`existing` (captured at line 980) already carries `.ResourceVersion` on every concrete type
(`Product`, `Collection`, `ProductVariant`, `File` all embed it — confirmed in
`datastore/entities.go`). Thread it into the same `switch r := existing.(type)` block that
already reads `r.UID`, replacing the four unconditional calls:

```go
case *datastore.Product:
    uid = r.UID
    deleteErr = s.store.DeleteProductWithResourceVersion(ctx, r.UID, r.ResourceVersion)
...
case *datastore.Collection:
    uid = r.UID
    deleteErr = s.store.DeleteCollectionWithResourceVersion(ctx, r.UID, r.ResourceVersion)
case *datastore.ProductVariant:
    uid = r.UID
    deleteErr = s.store.DeleteProductVariantWithResourceVersion(ctx, r.UID, r.ResourceVersion)
...
case *datastore.File:
    uid = r.UID
    deleteErr = s.store.DeleteFileWithResourceVersion(ctx, r.UID, r.ResourceVersion)
```

No change is needed to the ownership guard (lines 1009-1020) — it stays as a fast-path skip
for phantom branch-deletion paths; the resourceVersion check is the actual concurrency
guard and fires regardless of whether the ownership guard ran.

### Conflict handling: reject, do not retry in-process

`deleteErr != nil` already falls through to the existing generic error handling (lines
1079-1087), which wraps and returns the error, which propagates up through
`applyResourceOperations` (line 814-816) and fails the whole admission call — the git push
is rejected. This plan recommends **keeping that behavior for `ErrConflict`** rather than
adding an in-process retry loop, for three reasons:

1. **Precedent already in this exact function.** The `CategoryTaxonomy` branch of this same
   `switch` (lines 1053-1062) calls `MarkCategoryTaxonomyDeletion` with the observed
   `r.ResourceVersion` and, on `markErr != nil`, returns the error immediately — no retry.
   This plan's Product/Collection/ProductVariant/File handling should match that sibling
   branch's behavior for consistency within the same function.
2. **The conflict is expected to be rare** (a genuine concurrent re-admission of the exact
   same UID within the delete's read-to-write window) and self-resolving: rejecting the
   push causes the git client to see a rejected-push error; a retried push re-runs
   `deleteResource` from a fresh read and will correctly either no-op (resource legitimately
   gone/re-owned) or succeed (resource still eligible).
3. **Avoiding an in-process retry loop keeps `deleteResource` side-effect-simple** —
   consistent with `errCategoryDeletionBlocked`'s existing pattern of surfacing a distinct,
   identifiable error rather than looping. An in-process retry would need its own bounded
   attempt count, re-lookup, and re-run of the ownership guard — meaningfully more surface
   area for a rare race, and the GraphQL-mutation status-write call sites in
   `internal/graph/resolver/status_generic.go` establish the same "surface the conflict,
   let the caller retry" convention for `ErrConflict` elsewhere in this codebase (there via
   a typed `Conflict` payload instead of a rejected push, since that path is a direct
   client mutation rather than an admission side effect).

Wrap the conflict for a clearer rejected-push message, mirroring `errCategoryDeletionBlocked`:

```go
var errResourceDeleteConflict = errors.New("resource delete conflict: resource was concurrently re-admitted")
```

and in the generic error path, when `errors.Is(deleteErr, datastore.ErrConflict)`, wrap with
`errResourceDeleteConflict` (or just let the existing `fmt.Errorf("delete %s %s/%s: %w", ...,
deleteErr)` wrap `datastore.ErrConflict` directly — either is acceptable; the important
property is that the message reaching the git client is distinguishable from a permanent
failure so a human/CI retry is not surprising). Final call on message wording is an
implementation-time decision, not required by this plan.

## 4. Backend implementation shape

### memdb (`gitstore-api/internal/datastore/memdb/backend.go`)

Mirror `DeleteNamespaceWithResourceVersion` exactly, reusing each existing unconditional
method's body with one inserted check. E.g. for Product:

```go
func (m *memdbDatastore) DeleteProductWithResourceVersion(_ context.Context, uid, expectedResourceVersion string) error {
    txn := m.db.Txn(true)
    raw, _ := txn.First("product", "id", uid)
    if raw == nil {
        txn.Abort()
        return fmt.Errorf("%w: product uid %s", datastore.ErrNotFound, uid)
    }
    current := raw.(*datastore.Product)
    if current.ResourceVersion != expectedResourceVersion {
        txn.Abort()
        return datastore.ErrConflict
    }
    if err := deleteOwnerReferenceProjections(txn, "Product", uid); err != nil {
        txn.Abort()
        return err
    }
    if err := txn.Delete("product", raw); err != nil {
        txn.Abort()
        return fmt.Errorf("memdb: delete product: %w", err)
    }
    txn.Commit()
    return nil
}
```

Same shape for Collection and ProductVariant (no owner-reference cleanup needed — their
existing unconditional deletes don't call `deleteOwnerReferenceProjections` either) and File
(does call it, same as Product). All four run inside a single `m.db.Txn(true)`, so the
version check and the delete are already atomic with respect to memdb's own locking — no
new locking primitive needed.

### ScyllaDB (`gitstore-api/internal/datastore/scylla/backend.go`, `scylla/file.go`)

Each existing unconditional method already re-reads the row (`GetProduct`/`GetCollection`/
`GetProductVariant`/`GetFile`) and passes its own `ResourceVersion` into an
`IF resource_version=?` CAS delete (`deleteProductAuthoritative`,
`deleteCollectionAuthoritative`, `deleteProductVariantAuthoritative`,
`deleteFileAuthoritative`, all funneling through `deleteAuthoritative`, which already
returns `datastore.ErrConflict` on `!applied`). The gap is only that this internal
CAS check runs against the *freshly re-read* version, not the caller-supplied
`expectedResourceVersion`.

Fix: add the caller's `expectedResourceVersion` as an explicit precondition check
**immediately after the fresh read, before running any mutation/index-cleanup steps**:

```go
func (s *scyllaDatastore) DeleteProductWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error {
    p, err := s.GetProduct(ctx, uid)
    if err != nil {
        return err
    }
    if p.ResourceVersion != expectedResourceVersion {
        return datastore.ErrConflict
    }
    // ...identical mutationAction sequence to DeleteProduct, still passing
    // p.ResourceVersion (== expectedResourceVersion, just confirmed) into
    // deleteProductAuthoritative...
}
```

The inner `IF resource_version=?` CAS delete stays as belt-and-suspenders protection for the
(much smaller) window between this check and the CAS statement executing — it already
returns `ErrConflict` correctly if that residual window is lost, no changes needed there.
Same shape for `DeleteCollectionWithResourceVersion`, `DeleteProductVariantWithResourceVersion`
(`backend.go`), and `DeleteFileWithResourceVersion` (`scylla/file.go`).

Consider factoring `DeleteProduct`/`DeleteProductWithResourceVersion` (etc.) so the
unconditional variant is implemented as `DeleteProductWithResourceVersion(ctx, uid,
p.ResourceVersion)` after its own fresh read — i.e. the conditional method becomes the one
real implementation and the unconditional one is a two-line wrapper. This is an
implementation-time call, not required by this plan, but would remove duplicate mutation
sequences between the two methods in each backend file.

## 5. Other Datastore-interface implementers that must gain the four new methods

Adding four methods to the `Datastore` interface requires every full implementer to add
them, or the module fails to compile — this is not a rolling-upgrade risk (it's caught at
build time, not at runtime), but it is a checklist for this change:

- `gitstore-api/internal/datastore/memdb/backend.go` (real implementation — §4 above)
- `gitstore-api/internal/datastore/scylla/backend.go` + `scylla/file.go` (real
  implementation — §4 above)
- `gitstore-api/internal/datastore/instrumented.go` (`InstrumentedDatastore` decorator —
  add four pass-through wrappers, exactly mirroring the existing
  `DeleteNamespaceWithResourceVersion` wrapper at `instrumented.go:381-386`:
  ```go
  func (d *InstrumentedDatastore) DeleteProductWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error {
      start := time.Now()
      err := d.next.DeleteProductWithResourceVersion(d.withFindingObserver(ctx), uid, expectedResourceVersion)
      d.observe("DeleteProductWithResourceVersion", start, err)
      return err
  }
  ```
  )
- `gitstore-api/internal/testutil/stubstore.go` (`StubStore` test fake — add four
  one-line stubs alongside the existing unconditional ones, e.g.
  `func (s *StubStore) DeleteProductWithResourceVersion(_ context.Context, _, _ string) error { return nil }`).

No other `datastore.Datastore` implementers exist in the repo (confirmed via
`grep -rln "datastore.Datastore\b"` across `gitstore-api/internal/`); resolvers, admission
policies, and the git-HTTP handler all consume the interface, not implement it.

## 6. Test coverage plan

Constitution requirement: `gitstore-api` changes must cover multi-replica correctness. This
change is entirely about that property, so it needs dedicated conflict tests, not just
happy-path coverage.

### 6.1 memdb: read-twice-then-race unit tests (one per resource kind)

Model on `TestMemdb_NamespaceContractRoundTripAndConflict`
(`gitstore-api/internal/datastore/memdb/namespace_contract_test.go:17`) and
`TestMemdb_UpdateCategoryTaxonomyStatus_StaleResourceVersionReturnsConflict`
(`gitstore-api/internal/datastore/memdb/backend_test.go:265`), adapted to a delete instead of
an update. For each of Product, Collection, ProductVariant, File, add e.g.
`TestMemdb_DeleteProductWithResourceVersion_ConflictWhenReAdmittedConcurrently`:

1. Create the resource; capture its initial `ResourceVersion` (simulating admission A's
   `lookupResourceByIdentity` read at the top of `deleteResource`).
2. Simulate a concurrent re-admission (admission B): call the existing `Update*` method to
   change an owned field (e.g. `RepositoryID`/`GitRef`, or any spec field) — this advances
   `ResourceVersion`.
3. Call the new `Delete*WithResourceVersion` using the **stale** version captured in step 1.
   Assert it returns `datastore.ErrConflict` (via `errors.Is`).
4. Assert via `Get*` that the resource **still exists** with the fields admission B wrote —
   i.e. the stale delete did not remove admission B's data. This is the actual regression
   this bug describes; asserting only the returned error without also asserting survival
   would miss a backend that returns `ErrConflict` but still deletes anyway.
5. As a control, call `Delete*WithResourceVersion` again with the **current** (post-update)
   `ResourceVersion` and assert success + `Get*` now returns `ErrNotFound`.

Also add a `TestMemdb_Delete*WithResourceVersion_NotFound` for the missing-UID case
(mirrors existing `ErrNotFound` coverage on the unconditional siblings).

### 6.2 ScyllaDB: same conflict shape, tagged integration test

This repo's "Datastore Hardening (tagged ScyllaDB + failure injection)" CI job pattern
(`gitstore-api/internal/datastore/scylla/backend_test.go`,
`scylla/backend_recovery_test.go`) is the model — specifically
`TestProjectionRepairServiceProtectsConcurrentWriter`
(`scylla/repair_test.go:100`) for how this codebase already simulates a concurrent writer
against a live/test Scylla session. Add
`TestScylla_DeleteProductWithResourceVersion_ConflictWhenReAdmittedConcurrently` (and the
Collection/ProductVariant/File equivalents) against a real or test Scylla session,
reusing the same "capture stale version, mutate via `UpdateProduct`, attempt delete with
stale version, assert `ErrConflict` + survival, then delete with current version and assert
success" shape as §6.1. These are the tests that actually exercise the `IF
resource_version=?` LWT path end-to-end, so they belong in whichever existing file/suite
already runs against `SCYLLA_TEST_ADDR` (likely `scylla/backend_test.go`, following its
existing `+build scylla_integration`-style tagging — confirm exact tag by checking an
existing conditional-write test in that file before adding).

### 6.3 `cataloggrpc` handler-level regression test

Add a `server_test.go` case (alongside existing `TestAdmission_BranchDeletion`-style tests)
that exercises `deleteResource` end-to-end against the `StubStore`/real backend: admit a
Product, then simulate two concurrent branch-deletion pushes racing on the same UID (or
directly unit-test `deleteResource` with a datastore double that returns `ErrConflict` from
the new method) and assert the admission call returns an error (push rejected) rather than
silently succeeding while the "winning" concurrent admission's data survives.

## 7. Rollout / migration note

This is purely additive to the `Datastore` interface — no existing method signature
changes, and the existing unconditional `DeleteProduct`/`DeleteCollection`/
`DeleteProductVariant`/`DeleteFile` are **kept**, unchanged, because they have a second,
separate caller each: the direct GraphQL `deleteProduct`-style mutations in
`gitstore-api/internal/graph/resolver/service.go` (e.g. `service.go:382`,
`s.store.DeleteProduct(ctx, uid)`) call these unconditionally today. That call site has the
same theoretical unconditional-delete race and could reasonably be migrated to the new
conditional methods too (it likely already has a resourceVersion available from its own
GraphQL input) — but per this backlog item's stated scope ("item #5 only... `deleteResource`
lookup-then-delete"), that resolver path is **not** touched by this plan and is noted here
only as a related, out-of-scope observation for a possible future backlog item.

Net blast radius of the recommended change:
- `datastore.go`: +4 interface methods.
- `memdb/backend.go`: +4 methods (~15 lines each, copy-adapted from existing siblings).
- `scylla/backend.go` + `scylla/file.go`: +4 methods (~10 lines each on top of existing
  fresh-read + mutation sequence).
- `instrumented.go`: +4 pass-through wrappers (~5 lines each).
- `testutil/stubstore.go`: +4 one-line stubs.
- `cataloggrpc/server.go`: 4 call-site edits inside the existing `deleteResource` switch
  (no new control flow), all within lines 1024-1076.
- New tests per §6, no changes to `applyResourceOperations`'s caller, the ownership guard,
  or any Rust/git-service code.

No datastore schema/migration changes on either backend (ScyllaDB's `resource_version`
column and memdb's in-memory field already exist and are already maintained on every
write). No coordination needed with rolling upgrades beyond the normal "deploy the binary
with the new interface method everywhere it's required" build-time constraint — there is no
wire-format or schema change for an old replica to misinterpret from a new one, or vice
versa, during a rolling restart.

## 8. Explicitly out of scope

- `CategoryTaxonomy` deletion (already has `MarkCategoryTaxonomyDeletion`; separate resource
  and a foreground-deletion/finalizer lifecycle this bug does not touch).
- Backlog items #1-#4 from the same memory file (condition-status casing, branch-deletion
  validation-unavailable flakiness, `TestCollection_SelectorNotIn`,
  `TestCollection_SelectorDoesNotExist` scaling) — being handled by other agents in their own
  worktrees; this plan does not touch `gitstore-controller-manager/internal/categorytaxonomy/`
  or `tests/integration/collection_test.go`.
- The direct GraphQL `deleteProduct`/`deleteCollection`/`deleteProductVariant`/`deleteFile`
  mutation resolvers' own unconditional-delete call sites (§7) — flagged but not fixed here.
- Any retry/backoff behavior for a rejected push on conflict — left to the git client /
  operator, per §3's reasoning.

# Research: Namespace/Repository Deletion Ordering and System Repository Bootstrap

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

## Decision 1: Implement only the synchronous precondition-check half of ADR-0002/ADR-0003's deletion flow

**Decision**: Implement steps 1-2 of the ADR-0002 §Delete and ADR-0003 §Delete flows
(check for existing repositories/catalog resources, reject synchronously with
`FailedPrecondition` if found) as an inline check inside the existing
`DeleteNamespace`/`DeleteRepository` service methods. Do **not** implement steps 3-7
(setting `metadata.deletionTimestamp`, adding a `gitstore.dev/foreground-deletion`
finalizer, entering a `Terminating` status, async controller-driven drain, and
finalizer removal before hard-delete).

**Rationale**: Both ADRs describe a full Kubernetes-style async finalizer state
machine. But as verified directly against the code on `main`:

- Neither the `Namespace` nor `Repository` datastore entity
  (`gitstore-api/internal/datastore/entities.go:20-29,201-215`) has a `Status` field,
  `deletionTimestamp`, or a finalizers list — unlike `Product`, `CategoryTaxonomy`,
  and `Collection`, which all have a `Status json.RawMessage` field precisely to
  support this kind of async, controller-written state.
- No controller exists for either `Namespace` or `Repository` in
  `gitstore-controller-manager/internal/` (only a `categorytaxonomy` package exists)
  to ever drive a `Terminating → drained → finalizer removed` transition.
- The feature spec's own Assumptions section explicitly places the declarative
  `.spec`/`.status` schema for both resources out of scope (tracked separately in
  GH#170/#249) and the Namespace/Repository watch/reconcile loop out of scope
  (GH#174).

Building the full async finalizer machinery here would mean adding a `Status` field
to two entities, a datastore schema change on both `go-memdb` and ScyllaDB backends,
and a new controller package — all explicitly deferred to other, not-yet-started
specs. Per Constitution Principle VII (Simplicity & YAGNI), this plan implements the
minimal synchronous check that satisfies every functional requirement in spec.md
(FR-001 through FR-011 are all expressible as synchronous check-then-reject/accept
behavior; none of them require an intermediate `Terminating` state to be observable).

**Alternatives considered**:
- *Build the full async finalizer state machine now*: Rejected. Requires schema
  changes and a new controller that are out of scope per the spec's Assumptions, and
  no functional requirement in the spec needs the intermediate `Terminating` state to
  be externally visible — a synchronous reject-or-proceed check satisfies every
  acceptance scenario.
- *Do a partial async version (set a `deletionTimestamp`-like marker but drain
  synchronously in the same request)*: Rejected. This is more complex than the
  synchronous check for no behavioral benefit, since there is no controller to make
  "async" meaningful — it would just be a slower version of the same synchronous
  check with extra state to keep consistent.

## Decision 2: The "reuse the existing File/CategoryTaxonomy rejection pattern" premise in the original request is incorrect — no such pattern exists in code today

**Decision**: Do not attempt to reuse an existing reference-check implementation,
because none exists. Design the existence-check methods fresh, but keep them narrow
and consistent with the datastore layering the codebase already uses elsewhere
(`Datastore` interface method + per-backend implementation, mirroring
`ListRepositoriesByNamespace`).

**Rationale**: The original feature request (see spec.md's Input line) asserted that
repository- and namespace-deletion checks should "mirror the existing rejection
pattern already used by File deletion (ADR-0008) and CategoryTaxonomy deletion
(ADR-0006)." This was verified directly against the code and found to be **incorrect**:

- `gitstore-api/internal/graph/resolver/category.resolvers.go:39-53`: `CreateCategory`,
  `UpdateCategory`, and `DeleteCategory` are all stubs that unconditionally return
  `errors.New("category mutations are managed via git push")`. There is no GraphQL
  mutation path for category deletion at all.
- `gitstore-api/internal/graph/resolver/collection.resolvers.go:68-70`:
  `DeleteCollection` unconditionally returns `fmt.Errorf("deleteCollection is
  deprecated: manage collections via git push")`. Same situation.
- The actual deletion code path for both resources is git-push-driven, in
  `gitstore-api/internal/cataloggrpc/server.go`'s `deleteResource()` (line 645-694).
  That function performs a lookup-then-delete with **no precondition check of any
  kind** — no check for child categories, no check for assigned products, no check
  for `fileRef` references. It simply calls `s.store.DeleteCategoryTaxonomy(ctx,
  r.UID)` / `s.store.DeleteCollection(ctx, r.UID)` unconditionally once the resource
  is found.
- File does not exist as an implemented resource at all (confirmed in the prior
  file-media-lifecycle-architecture research), so there is no File-deletion
  reference-check to reuse either.

This means ADR-0006's "reject if children/products exist" and ADR-0008's "reject if
fileRef references exist" are, like ADR-0002/ADR-0003's finalizer flow, **documented
but unimplemented** — the exact same class of gap this spec is closing for
Namespace/Repository, just for a different pair of resources. This spec does not fix
the CategoryTaxonomy/Collection gap (that is a separate, pre-existing issue outside
this spec's stated scope — GH#165/GH#173 name Namespace/Repository specifically), but
it means this spec establishes the *first* real implementation of this
check-then-reject pattern in the codebase, rather than the third.

**Alternatives considered**:
- *Fix CategoryTaxonomy/Collection's missing checks in the same spec since they're the
  same bug class*: Rejected — out of scope for GH#165/GH#173, would expand this
  spec's blast radius beyond Namespace/Repository, and deserves its own tracked issue
  since it's a distinct, pre-existing gap discovered as a side effect of this
  research rather than something the user asked to fix here.

## Decision 3: Existence checks are indexed lookups against the existing `RepositoryID`/`NamespaceID` fields — no new denormalized counter needed

**Decision**: Implement the repository-has-catalog-resources check as an existence
query (`EXISTS`/limit-1 semantics, not a full count) filtered by `RepositoryID`
against each of the four catalog entity tables (`Product`, `ProductVariant`,
`CategoryTaxonomy`, `Collection`). Implement the namespace-has-repositories check the
same way, filtered by `NamespaceID` against the `Repository` table. Do not add a
denormalized "resource count" field to `Namespace` or `Repository`.

**Rationale**: Every catalog entity already stores `RepositoryID`
(`gitstore-api/internal/datastore/entities.go` lines 62, 102, 139, and — confirmed by
the same struct shape — `ProductVariant`), and `Repository` already stores
`NamespaceID` (line 203). This means both checks can use bounded existence queries
under both backends:
- `go-memdb`: an indexed lookup on the existing `RepositoryID`/`NamespaceID` field,
  same style as existing indexed lookups in `memdb/backend.go`.
- ScyllaDB: repositories use the existing `NamespaceID` secondary index. Catalog
  resources first resolve the repository's namespace, bind that existing namespace
  partition, then filter by `RepositoryID` with `LIMIT 1`. This avoids a global table
  scan and does not add a production schema migration.

A denormalized counter would require incrementing/decrementing on every catalog
resource create/delete across four resource kinds and would introduce a consistency
hazard (counter drift) for no benefit — an existence check only needs to know "is
there at least one," not "how many."

**Alternatives considered**:
- *Denormalized count field on `Repository`/`Namespace`*: Rejected. Adds a
  consistency-maintenance burden (must be kept correct across every catalog mutation
  path — GraphQL mutations, git-push admission, and any future bulk operation) for a
  check that only needs a boolean answer.
- *Full table scan filtered client-side*: Rejected. `Repository`/catalog tables are
  explicitly scoped for "up to 5,000,000 products" per the constitution's Scale
  Constraints — a full scan is not acceptable even for an infrequent deletion check.

## Decision 4: `gitstore-system` provisioning idempotency uses "create if absent," not a distributed lock

**Decision**: `CreateNamespace` provisions `gitstore-system` by checking whether the
repository already exists for that namespace (by well-known name) and creating it
only if absent, treating a concurrent "already exists" error from the create call
itself as success (idempotent outcome), not a failure to surface to the caller.

**Rationale**: FR-008 requires no duplicate/conflicting system repository on retried
namespace creation. `CreateRepository` already returns `datastore.ErrAlreadyExists`
on a uniqueness conflict (per the existing pattern in `CreateNamespace` at
`service.go:260-263`, which already handles `ErrAlreadyExists` for the namespace
identifier itself). Reusing that same error-handling shape for the nested
`gitstore-system` creation call — attempt create, treat `ErrAlreadyExists` as a
successful idempotent no-op — avoids introducing a new locking primitive for a
narrow, low-concurrency bootstrap path (namespace creation is not a hot path per the
constitution's Scale Constraints).

**Alternatives considered**:
- *Check-then-create (look up by name, create only if not found)*: Rejected as the
  sole mechanism — it has a TOCTOU race under concurrent retries of the same
  namespace creation. Combined with treating the create call's own
  `ErrAlreadyExists` as success, the check-then-create becomes a fast-path
  optimization on top of a safe fallback, which is the approach taken.
- *Distributed lock around namespace creation*: Rejected. Disproportionate complexity
  for a bootstrap operation that is not expected to be highly concurrent for the same
  namespace identifier (namespace identifiers are unique by construction — the
  concurrency case is only "the same create request retried," not "many different
  callers fighting over one namespace").

## Decision 5: Concurrent deletion races resolve via the datastore's existing not-found semantics, not new locking

**Decision**: FR-011 (deterministic resolution of concurrent deletion attempts) is
satisfied by the existing `datastore.ErrNotFound` handling already present in
`DeleteNamespace`/`DeleteRepository` (`service.go:321-324`, `599-601`) — the first
successful delete removes the record; any subsequent concurrent attempt's lookup or
delete call receives `ErrNotFound` and is translated to the existing "not found"
GraphQL error, which is already distinct from the new "`FailedPrecondition`"
rejection error introduced by this spec.

**Rationale**: This requires no new mechanism. The existing datastore delete
operations are already atomic at the single-record level (a `DELETE` on a row either
finds and removes it or does not), so two concurrent `DeleteRepository` calls against
the same `repoID` cannot both succeed — the second one's underlying store call
already returns `ErrNotFound`, which existing code already maps to a distinct error
message from precondition-failure. The precondition check (does this repository have
catalog resources) and the delete itself are not required to be a single atomic
transaction for this spec's correctness: if a catalog resource is created in the
narrow window between the check and the delete, that is an acceptable, extremely
rare race that a future spec covering the full async finalizer flow (Decision 1)
would close via the `Terminating`-state admission-time rejection (ADR-0003's
"namespace is `Active` (not `Terminating`)" admission rule) — not a gap this
synchronous-check spec needs to solve.

**Alternatives considered**:
- *Wrap check-then-delete in a single serializable transaction*: Rejected for this
  phase. Neither `go-memdb` nor the existing ScyllaDB backend usage in this codebase
  wraps multi-step check-then-act sequences in cross-table transactions today (e.g.
  `DeleteRepository`'s existing storage-then-metadata sequence in
  `service.go:568-604` is already not fully transactional, relying on ordering — git
  storage removed first, metadata second — rather than a distributed transaction).
  Introducing transactional guarantees here would be inconsistent with the rest of
  the codebase's existing concurrency posture and is not required by any FR.

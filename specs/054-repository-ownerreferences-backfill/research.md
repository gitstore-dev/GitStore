# Research: Resource OwnerReferences and Deletion Semantics

**Feature**: [spec.md](./spec.md)
**Date**: 2026-11-06

## Decisions

### 1. Reuse the reverse owner-reference projection; do not add another query index

**Decision**: Keep `datastore.OwnerReferenceStore` and the existing Scylla
`owner_reference_dependents` table as the catalog-resource reverse index. Keep
the memdb projection as the backend-neutral contract implementation. Continue
to use the established Namespace/Repository deletion preconditions for those
infrastructure kinds rather than forcing those differently scoped checks
through a catalog-owner lookup.

**Rationale**: Both backends already maintain owner-reference projections, and
blocking dependent queries are indexed and bounded. The Scylla table is in the
applied baseline migration `001_infra.cql`; changing that migration would
invalidate migration checksums. Namespace and Repository already have
resource-specific existence checks with established concurrency behavior.

**Alternatives considered**:

- Add a second generic owner-reference index: rejected because the existing
  table already represents the catalog ownership edges and another projection
  would duplicate durable state.
- Replace every Namespace/Repository deletion check with one generic owner
  query: rejected because `OwnerReferenceScope` is scoped to a namespace and
  owner repository, while infrastructure deletion has different scope and
  established preconditions.

### 2. Add a bounded, resumable `gitctl` backfill and a durable completion gate

**Decision**: Add an operator-invoked `gitctl` owner-reference backfill command.
It scans resource kinds in stable, bounded pages, repairs authoritative
`ownerReferences` with UID/resource-version preconditions, and lets the normal
datastore write path synchronize the reverse projection. Store per-kind cursors
and overall completion state durably in Scylla through a new incremental
migration; implement equivalent state behavior in memdb tests. The command is
idempotent and resumable. New indexed deletion enforcement is enabled only
after a full pass and verification completes; until then the existing
resource-specific safety checks remain active and unsupported indexed decisions
fail closed.

**Rationale**: Create/update-only self-healing cannot guarantee safety for
untouched legacy dependents. A local-only cursor cannot safely resume from a
different operator host, and a full uncheckpointed scan of the five-million-
Product scale is not operationally bounded. Existing write paths already
synchronize the projection and preserve optimistic concurrency.

**Alternatives considered**:

- Backfill only when each resource is next touched: rejected because an
  untouched blocking dependent could be missed by deletion.
- Rebuild the projection by scanning all rows on every API startup: rejected
  because it is unbounded startup work and duplicates operational recovery
  controls.
- Add a standalone command under `cmd/`: rejected in favor of the repository's
  consolidated `gitctl` operator tooling.
- Enable deletion immediately and repair later: rejected because an incomplete
  index is not evidence that no blocker exists.

### 3. Preserve existing relationship-specific deletion workflows

**Decision**: Define a uniform outcome from `blockOwnerDeletion`, while
retaining resource-specific entry points, authorization, status, and finalizer
workflows. A `true` reference blocks owner removal; a `false` reference permits
owner removal and leaves the dependent alive as an orphan, with stale
system-owned resolution metadata cleared. No reference value requests cascade
deletion. Keep the existing asynchronous CategoryTaxonomy/Product finalizer
workflows separate from foreground/background propagation.

**Rationale**: Product and CategoryTaxonomy already use reverse projections,
optimistic concurrency, and bounded controller completion. Namespace and
Repository have synchronous deletion gates. The user-visible semantics can be
consistent without replacing these different lifecycle mechanisms.

**Alternatives considered**:

- Automatically delete dependents for foreground deletion: rejected because
  the specification requires the dependent to block removal and current
  Product/CategoryTaxonomy contracts do not cascade.
- Treat background deletion as asynchronous finalizer completion: rejected
  because that conflates propagation behavior with how long a resource takes
  to finish its own deletion.
- Replace all current deletion flows with one controller: rejected as a
  broader architectural change than the ownership contract requires.

### 4. Keep ServiceAccount a cluster-scoped root

**Decision**: Include ServiceAccount in the common GraphQL metadata contract
and explicitly return its existing non-nil empty `ownerReferences` list. Do not
add a fabricated Namespace edge or treat its authentication `metadata.owner`
subject as a resource owner. Preserve its current immediate deletion behavior
because it has no resource dependents in the ownership matrix.

**Rationale**: `ObjectMeta` and GraphQL `OwnerReference` are already shared;
the ServiceAccount projection already supplies an empty list. The persistent
ServiceAccount entity is deliberately not a Git-backed resource envelope and
currently has no resource-owner relationship.

**Alternatives considered**:

- Add a Namespace owner reference: rejected because ServiceAccount is
  cluster-scoped and rejects a namespace.
- Persist an empty owner-reference column solely for symmetry: rejected
  because no relationship is stored and the existing API projection already
  guarantees the required empty list. A future ServiceAccount dependency must
  define its own lifecycle contract.

### 5. Roll out metadata before indexed enforcement

**Decision**: Deploy API versions that write the complete ownership matrix
while retaining old deletion checks; wait until old API replicas have drained;
run and verify the backfill; then mark the backfill complete and enable
index-backed behavior. Keep the migration additive and retain old checks as
defense for Namespace/Repository. Rollback must not remove migrated metadata,
projection rows, or progress state.

**Rationale**: API replicas share Scylla, but an older writer may not maintain
new relationships. A deployment-stage gate avoids mixing incomplete old
writers with enforcement. No Git service change is required.

**Alternatives considered**:

- Run the backfill while old writers remain active: rejected because they may
  create or update rows without the new relationship metadata.
- Rewrite applied migration files: rejected because migration history is
  checksum-validated.

### 6. Reuse current capacity and fault-evidence entry points

**Decision**: Extend existing owner/deletion correctness oracles in the
supported `repository/lifecycle` capacity scenario and use the existing
`controller-restart` fault profile for replica replacement evidence. Add
focused datastore contract, integration, and backfill-interruption tests;
do not create a feature-specific public Make target.

**Rationale**: The repository lifecycle scenario already exercises two API and
controller replicas, bounded repository lifecycle load, Git push, and the
retained five-million-Product dataset. The root Makefile is the canonical
capacity interface.

**Alternatives considered**:

- Add a new capacity target or public profile for this feature: rejected
  because it duplicates the supported scenario interface.
- Use only unit tests as production evidence: rejected because they do not
  establish deployed replica, load, or recovery behavior.

## Evidence

- `gitstore-api/internal/catalog/status.go`: shared `OwnerReference` fields,
  including internal `repositoryID` scope excluded from YAML.
- `gitstore-api/internal/datastore/datastore.go`: bounded paginated
  `OwnerReferenceStore` and `OwnerReferenceScope`.
- `gitstore-api/internal/datastore/scylla/migrations/001_infra.cql`:
  existing `owner_reference_dependents` table; the migration rule prohibits
  editing applied migration checksums.
- `gitstore-api/internal/datastore/scylla/owner_references.go` and
  `gitstore-api/internal/datastore/memdb/owner_references.go`: projection
  synchronization, bounded dependent queries, and CategoryTaxonomy deletion
  compare-and-set behavior.
- `gitstore-api/internal/cataloggrpc/server.go`: existing Product and
  CategoryTaxonomy owner resolution, deletion validation, and fail-closed
  behavior.
- `gitstore-api/internal/admin/graph/resolver/serviceaccount_service.go`:
  ServiceAccount's non-null empty owner-reference projection.
- `gitstore-api/internal/datastore/entities.go` and
  `gitstore-api/internal/datastore/scylla/serviceaccount.go`: ServiceAccount
  is cluster-scoped and has no persisted resource-owner metadata.
- `specs/052-categorytaxonomy-deletion-semantics/data-model.md` and
  `specs/055-product-lifecycle/data-model.md`: bounded orphaning and
  finalizer/CAS precedents.
- `tests/capacity/README.md` and `tests/integration/repository_lifecycle_capacity_test.go`:
  supported `repository/lifecycle` capacity evidence and two-replica workload.

## Resolved Planning Questions

- No new third-party dependencies are required.
- No GraphQL field shape is required; the shared `OwnerReference` type already
  exists. Its schema documentation must describe blocking/orphaning rather than
  implying cascade deletion.
- No Git service changes are required; resource metadata remains computed by
  API admission and datastore lifecycle paths.
- Scylla changes, if any, must be incremental. The plan adds only durable
  backfill progress state; it does not change `001_infra.cql`.
- No unresolved `[NEEDS CLARIFICATION]` items remain.

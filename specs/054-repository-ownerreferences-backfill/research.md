# Research: Resource OwnerReferences and Deletion Semantics

**Feature**: [spec.md](./spec.md)
**Date**: 2026-11-07

## Decisions

### 1. Reuse the existing reverse owner-reference projection

Keep `datastore.OwnerReferenceStore` and Scylla's existing
`owner_reference_dependents` table as the catalog-resource reverse index. The
projection already supports bounded blocking-dependent lookups and is
maintained by the normal datastore write path. Do not add a second index.

Namespace and Repository retain their resource-specific existence and
concurrency checks; their scope differs from the catalog owner lookup.

### 2. Repair metadata on normal create/update

The initial ownership set is derived from the admitted resource and its
resolved Repository/Product/CategoryTaxonomy relationships. When a resource
is legitimately updated, admission recomputes its managed owner references
and the datastore write refreshes its reverse projection. An untouched legacy
resource is not included in a guaranteed full inventory.

A fleet-wide scan of the retained five-million-Product dataset, durable
progress, and a deletion gate tied to full-scan completion are deferred until
there is a concrete rollout need. A partial exploratory scan is not evidence
of complete historical coverage.

### 3. Preserve relationship-specific deletion workflows

A `true` reference blocks owner removal; a `false` reference permits owner
removal and leaves the dependent alive as an orphan. Neither value cascades
deletion. Keep each kind's authorization, status, finalizer, CAS, and existing
deletion workflow. CategoryTaxonomy/Product orphan handling remains bounded
and does not rewrite authored references.

### 4. Keep ServiceAccount a cluster-scoped root

ServiceAccount has no resource-owner relationship in the current matrix.
Continue to expose its non-null empty owner-reference list; its authentication
`metadata.owner` is a principal, not a resource reference.

### 5. Reuse existing validation entry points

Use backend-neutral owner-reference contracts, API admission and resolver
tests, Scylla projection tests, and the repository lifecycle capacity profile.
Do not add a backfill CLI, migration, or feature-specific Make target.

## Evidence

- `gitstore-api/internal/catalog/status.go`: shared owner-reference metadata.
- `gitstore-api/internal/datastore/datastore.go`: bounded
  `OwnerReferenceStore` and owner scope.
- `gitstore-api/internal/datastore/scylla/migrations/001_infra.cql`:
  existing `owner_reference_dependents` table.
- `gitstore-api/internal/datastore/scylla/owner_references.go` and
  `gitstore-api/internal/datastore/memdb/owner_references.go`: projection
  synchronization and bounded dependent queries.
- `gitstore-api/internal/cataloggrpc/server.go`: admission relationship
  resolution and system-managed reference repair.
- `gitstore-api/internal/admin/graph/resolver/serviceaccount_service.go`:
  ServiceAccount's non-null empty owner-reference projection.
- `specs/052-categorytaxonomy-deletion-semantics/data-model.md` and
  `specs/055-product-lifecycle/data-model.md`: bounded orphaning and
  finalizer/CAS precedents.
- `tests/capacity/README.md` and
  `tests/integration/repository_lifecycle_capacity_test.go`: existing
  repository lifecycle capacity acceptance.

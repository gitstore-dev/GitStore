# Implementation Plan: Resource OwnerReferences and Deletion Semantics

**Branch**: `054-repository-ownerreferences-backfill` | **Date**: 2026-11-07 | **Spec**: [spec.md](./spec.md)
**Input**: [Resource OwnerReferences and Deletion Semantics](./spec.md)

## Summary

Maintain system-managed owner references for Namespace, Repository, Product,
ProductVariant, CategoryTaxonomy, Collection, File, and ServiceAccount. Reuse
the current resource metadata and `owner_reference_dependents` projection.
Ordinary create/update admission repairs managed references on the resource
being written. A `true` reference blocks owner deletion; a `false` reference
allows owner deletion and leaves the dependent as an orphan. Neither value
cascades deletion.

This feature does not add a fleet-wide legacy scan, durable backfill progress,
or a completion gate. Existing resources are repaired when legitimately
updated; full historical coverage remains a separate rollout decision.

## Technical Context

- **Language/Version**: Go; use versions pinned by the API module.
- **Primary Dependencies**: Existing catalog admission, `OwnerReferenceStore`,
  Scylla, memdb, and controller lifecycle interfaces; no new dependencies.
- **Storage**: Reuse authoritative resource rows and the existing
  `owner_reference_dependents` projection. No new migration is needed for this
  behavior.
- **Testing**: Backend-neutral datastore, API admission/resolver, Scylla
  projection, concurrency, controller, and existing capacity tests.
- **Constraints**: References are system-managed; Git remains canonical
  authored state; no cascades; preserve resource-specific authorization,
  finalizer, CAS, and deletion checks. Clarify schema documentation without
  changing GraphQL shape.
- **Replica/Scaling Model**: API replicas share datastore rows and reverse
  projections. Writes remain idempotent and use existing version preconditions.
  Git remains exactly one active process with non-overlapping replacement.
- **Authentication/Authorization**: Preserve existing per-kind AuthN/AuthZ
  and namespace/repository isolation for mutations and deletion.
- **Load/Backpressure**: Keep request-path relationship work bounded. No
  catalog-wide scan is added to admission or startup.
- **Capacity Profile**: Reuse
  `make capacity TARGET=repository PROFILE=lifecycle MODE=production`.
  Existing five-million-Product data is retained; the owner-reference feature
  does not require scanning or reseeding it.
- **Fault Profile**: Reuse the existing explicitly test-owned
  `controller-restart` profile where deployed recovery evidence is required.

## Design

1. **Maintain ownership during normal writes**: Resolve the Repository and
   applicable domain owners from the admitted resource. Replace stale or
   duplicate managed references while preserving unrelated references, then
   let the normal datastore write update the existing reverse projection.
2. **Preserve deletion semantics**: Keep resource-specific deletion entry
   points, authorization, status, and finalizer behavior. Use the existing
   reverse projection for blocking and orphan decisions.
3. **Be explicit about legacy coverage**: A resource not created or updated
   after this behavior is deployed may still lack managed references. Do not
   claim full historical coverage or add a completion gate to imply it.
4. **Validate bounded behavior**: Extend existing tests and capacity oracles
   without adding a feature-specific command, migration, service, or public
   Make target.

## Structure

Implementation remains within the existing API, datastore, controller, and
integration-test components. No new service, standalone migration, operator
command, broker, generic lifecycle framework, or second owner-reference
projection is introduced. The Git service remains unchanged.

## Validation

- Run backend-neutral memdb contracts and focused API admission/resolver tests.
- Run Scylla projection tests when a test Scylla endpoint is available.
- Verify owner create/update, blocking deletion, orphaning, no-cascade, and
  concurrency behavior.
- Run repository lifecycle capacity and controller restart evidence only
  against explicitly test-owned deployments; do not treat a partial legacy
  metadata scan as proof of full coverage.

**ADR status**: ADRs 0003–0006 are already Accepted. This feature does not
implement the core decisions of Proposed ADRs 0007 or 0008; do not flip those
ADRs or their index.

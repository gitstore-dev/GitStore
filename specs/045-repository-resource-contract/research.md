# Research: Repository Resource Contract

**Branch**: `045-repository-resource-contract` | **Phase**: 0 | **Date**: 2026-08-16

## D-001: Additive GraphQL envelope

**Decision**: Add non-null `apiVersion`, `kind`, `metadata`, `spec`, and
`status` fields to `Repository` while retaining every existing flat field and
mutation contract. Mark duplicate legacy output fields deprecated using the
same future-major-release wording as Namespace PR #345.

**Rationale**: The active feature specification explicitly requires existing consumers to remain unaffected. Additive schema evolution satisfies that requirement and the constitution's API versioning rule.

**Alternatives considered**:
- Replace the flat type: rejected because it breaks existing consumers.
- Introduce a second `RepositoryResource` query/type: rejected because it duplicates identity and lookup semantics.

## D-002: Resource identity placement

**Decision**: Use constant `apiVersion: "gitstore.dev/v1beta1"` and
`kind: "Repository"`. Put the author-controlled repository name in
`metadata.name`, not `spec.name`.

**Rationale**: This matches PR #345's Namespace contract and the established
Kubernetes resource envelope. Name is author-controlled metadata, while spec
contains desired configuration.

**Alternatives considered**:
- Keep name duplicated in spec: rejected because it creates two authoritative
  locations for the same identity.
- Omit apiVersion/kind because Repository is statically typed: rejected because
  the declarative contract must match other GitStore resources.

## D-003: Shared metadata and conditions

**Decision**: Reuse the existing GraphQL `ObjectMeta` and `Condition` types. Populate unused catalog-only metadata fields with their existing empty/null representations.

**Rationale**: Repository needs the same UID, namespace, resourceVersion, generation, and creationTimestamp vocabulary as other resources. Reuse makes the contract predictable and avoids Repository-specific equivalents.

**Alternatives considered**:
- Define `RepositoryMeta`: rejected as needless contract divergence.
- Keep identity/versioning only as legacy flat fields: rejected because the three-group declarative shape would remain incomplete.

## D-004: Repository configuration contract

**Decision**: `RepositorySpec` contains `defaultBranch`, `visibility`, and a
non-null `pushPolicy`. Default branch and existing maximum-size values project
persisted configuration; maximum-size values remain non-null because the
current datastore uses explicit zero as the unlimited sentinel.
`RepositoryPushPolicy` otherwise mirrors PR #345's Namespace
push-policy defaults: maximum pack/file sizes, receive-pack hook toggles,
schema-validation settings, and admission-control settings.
Visibility and the extended policy groups are reserved read-contract fields in
this feature: visibility projects `PRIVATE` and the extended groups project null
until a future feature defines write and persistence semantics.

**Rationale**: Namespace defaults and Repository overrides must use the same
vocabulary for future effective-policy resolution. The two maximum-size values
already exist on `datastore.Repository`; the remaining groups project null
until their persistence/write semantics land.

**Alternatives considered**:
- Expose only `defaultBranch`: rejected because it omits existing Repository
  push-policy configuration and prevents a consistent inheritance contract.
- Put storage class in spec: rejected because callers cannot set it today and
  it remains system-resolved state.
- Invent Repository-specific hook/validation types: rejected because the
  Namespace contract already defines the shared policy vocabulary.

## D-005: Status shape

**Decision**: Match Namespace status conventions with
`observedGeneration`, `lastAppliedRevision`, and `conditions`, plus a non-null
Repository-specific `resolved` object containing storage path and storage
class. This feature defines no Repository-specific condition types and emits an
empty condition list until a future condition-producing writer defines its
vocabulary and ownership rules.

**Rationale**: The common status fields make controller behavior predictable,
while derived storage values have a clear system-owned location.

**Alternatives considered**:
- Keep storage fields directly on status: rejected because `resolved` cleanly
  separates observed condition state from computed values.
- Make status/resolved nullable: rejected because legacy repositories require a
  complete default projection.

## D-006: Minimal persisted state

**Decision**: Add `Generation int64`, `ResourceVersion string`, and `Status json.RawMessage` to the existing datastore Repository entity.

**Rationale**: Generation and resourceVersion have independent transition semantics and cannot be reconstructed reliably from timestamps. A JSON status blob matches existing catalog-resource storage and permits condition evolution without another schema migration. Storage path remains derived from repository ID and storage class remains an existing column.

**Alternatives considered**:
- Derive both counters from `UpdatedAt`: rejected because generation must not change on system-only updates.
- Create a repository-status table: rejected because it adds a join and consistency boundary for a single read model.
- Store storage path: rejected because the established repository identity design derives it from ID.

## D-007: Canonical legacy defaults

**Decision**: Treat missing/zero legacy fields as generation `1`,
resourceVersion `"1"`, visibility `PRIVATE`, and status
`{"observedGeneration":0,"conditions":[]}` on every read and before every
mutation.

**Rationale**: Existing rows must expose a complete non-null contract. Normalizing before mutation preserves meaningful history: the first legacy rename advances to generation/resourceVersion `2`, rather than presenting the rename as the initial state.

**Alternatives considered**:
- Expose zero/empty values: rejected because they do not match the established resource contract.
- Require an eager data backfill: rejected because read-time normalization is safe, supports memdb, and avoids making deployment depend on a separate batch job.

## D-008: Version transitions

**Decision**:

- Create: generation `1`, resourceVersion `"1"`.
- Rename (`metadata.name`): increment generation and resourceVersion.
- Transfer: preserve UID and generation; increment resourceVersion.
- Future default-branch, visibility, or push-policy writes: use the shared spec-transition helper to increment generation and resourceVersion.
- Future status/system-derived changes: preserve generation; increment resourceVersion.

**Rationale**: Name is currently mutable author-controlled metadata, while
default branch and maximum-size limits are persisted declarative configuration.
Namespace ownership, storage path/class, and status are system-managed.
ResourceVersion changes for every supported resource modification; generation
changes for the current rename path and must change for future spec-write paths.

**Alternatives considered**:
- Increment generation on transfer: rejected because namespace ownership is system metadata, not RepositorySpec.
- Reset counters on transfer: rejected because transfer is not delete-and-recreate.

## D-009: Forward Scylla migration

**Decision**: Add a sequential migration that alters `repositories` with `generation`, `resource_version`, and `status`; update gocqlx table metadata and row conversion.

**Rationale**: The feature explicitly covers pre-existing repositories. A forward migration supports upgraded installations and fresh databases through the same ordered migration chain.

**Alternatives considered**:
- Rewrite `001_initial_schema.cql`: rejected because existing installations would not receive the new columns.
- Add a replacement table: rejected because keys and access patterns are unchanged.

## D-010: Dependency on Namespace contract types

**Decision**: Reuse `Long`, `RepositoryVisibility`, `ReceivePackHookDefaults`,
`HookToggle`, `SchemaValidationDefaults`, and `AdmissionControlDefaults` from PR
#345. Planning pins the prerequisite at PR head
`fefadbea951959c42a982d5e0d7824dbf175209c`. Implementation verifies that
revision or a merged descendant containing the same definitions. If they are
absent, implementation stops with prerequisite guidance rather than merging or
rebasing another branch as a task side effect.

**Rationale**: Shared types ensure Namespace defaults and Repository overrides
compose without conversion-specific naming or semantic drift.

**Alternatives considered**:
- Duplicate all types under Repository names: rejected because gqlgen would
  generate parallel models for identical concepts.

## D-011: No controller, watch, or status mutation API

**Decision**: Define and expose status, but do not introduce a Repository controller, watch subscription, reconciler, or status mutation.

**Rationale**: These are explicitly out of scope. The initial status is useful and complete without asynchronous infrastructure.

**Alternatives considered**:
- Add generic `updateResourceStatus` support for Repository now: rejected as speculative behavior not required by the active specification.
- Publish Repository watch events: rejected because no watch semantics are requested.

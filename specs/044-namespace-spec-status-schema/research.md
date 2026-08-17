# Research: Namespace Resource Contract

## Decision 1: Limit this feature to contract and read hydration

**Decision**: Define the declarative Namespace schema and return the fully hydrated shape from existing GraphQL reads and mutation payloads. Do not implement Git-backed create, update, delete, status-write, validation, admission, watch, or policy-resolution behavior in this feature.

**Rationale**: GH#171 establishes the stable resource contract that GH#172, GH#173, GH#174, and GH#249 consume. Implementing those behaviors here would combine independently testable features and violate the clarified scope.

**Alternatives considered**:
- Implement Git-delegating mutations now: rejected because GH#172 owns write semantics and concurrency.
- Define documentation only without changing the read schema: rejected because the specification requires API reads to expose the new contract.

## Decision 2: Use a dedicated `NamespaceMetadata` GraphQL type

**Decision**: Keep the standard `Namespace { id, apiVersion, kind, metadata, spec, status }` envelope, but define `NamespaceMetadata` rather than reusing the existing GraphQL `ObjectMeta` type.

`NamespaceMetadata` is field-equivalent to the shared metadata contract for `name`, `labels`, `annotations`, `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`, and `finalizers`, but has no `namespace` field.

**Rationale**: The existing shared GraphQL `ObjectMeta.namespace` field is non-null and correctly enforces the invariant for Product, CategoryTaxonomy, Collection, and ProductVariant. Making it nullable would weaken every namespace-scoped resource contract. A dedicated type preserves the shared semantics while representing a top-level resource accurately.

**Alternatives considered**:
- Make `ObjectMeta.namespace` nullable: rejected because it creates an unrelated breaking change for all namespace-scoped resources.
- Populate `metadata.namespace` with an empty string: rejected because the clarified contract requires the field to be omitted.
- Keep Namespace metadata fields flat: rejected because it violates the standard resource envelope.

## Decision 3: Keep the existing Namespace datastore schema unchanged

**Decision**: Do not add memdb indexes/fields, Scylla columns, or a migration in GH#171. Hydrate the declarative API resource from the existing flat Namespace row.

**Rationale**: Current Namespace tables persist `id`, `identifier`, `display_name`, `tier`, and audit timestamps/actors. Actual Git-backed spec writes, generation/resource-version advancement, status writes, and policy persistence belong to GH#172/GH#173. A migration now would create persistence semantics before their owning feature defines them.

**Alternatives considered**:
- Add all declarative columns now: rejected as premature and behaviorally incomplete.
- Store the new contract as an opaque JSON blob: rejected because it duplicates existing columns and weakens type safety.

## Decision 4: Define stable hydration defaults for legacy rows

**Decision**: Convert an existing Namespace row as follows:

- GraphQL `id` and `metadata.uid`: existing `Namespace.ID`
- `apiVersion`: `gitstore.dev/v1beta1`
- `kind`: `Namespace`
- `metadata.name`: existing `Identifier`
- `metadata.labels`, `metadata.annotations`: empty maps
- `metadata.resourceVersion`: `"1"`
- `metadata.generation`: `1`
- `metadata.creationTimestamp`: existing `CreatedAt`
- `metadata.revision`: null
- `metadata.ownerReferences`, `metadata.finalizers`: empty lists
- `spec.title`: existing `DisplayName`
- `spec.tier`: existing `Tier`
- `spec.repositoryDefaults`, `spec.pushPolicyDefaults`: null
- `status.observedGeneration`: `0`
- `status.lastAppliedRevision`: null
- `status.conditions`: empty list

**Rationale**: These values are deterministic, non-null where the contract requires it, and match the initial numeric-string/integer conventions used by existing declarative resources without pretending that legacy rows have been admitted from Git. GH#172 owns advancement after successful changes.

**Alternatives considered**:
- Derive resource version from timestamps: rejected because it reinvents the established numeric counter semantics.
- Backfill incrementing values into storage: rejected because it requires a migration and write semantics outside this feature.
- Set status observed generation to `1`: rejected because no system process has observed the legacy spec.

## Decision 5: Keep Relay transport identity alongside resource identity

**Decision**: Retain top-level GraphQL `id: ID!` for Relay compatibility and expose the canonical resource identity as `metadata.uid`.

**Rationale**: Existing GitStore GraphQL resources use a transport-level Relay ID in addition to Kubernetes-style metadata. Removing `id` would cause unnecessary framework and consumer breakage unrelated to the declarative contract.

**Alternatives considered**:
- Remove `id` and use only `metadata.uid`: rejected because Namespace remains a Relay node in the API.

## Decision 6: Define typed repository and push-policy defaults without behavior

**Decision**:

- `repositoryDefaults.visibility`: `RepositoryVisibility` enum with `PUBLIC`, `PRIVATE`, `INTERNAL`
- `repositoryDefaults.defaultBranch`: optional string
- `pushPolicyDefaults.maxPackSizeBytes`, `maxFileSizeBytes`: optional signed 64-bit integers
- hook settings: optional typed objects with `enabled: Boolean`
- `schemaValidation.phase`, `admissionControl.phase`: optional strings preserving Git hook spellings
- `schemaValidation.timeoutSeconds`: optional GraphQL `Int`
- `admissionControl.branchPattern`: optional string

Add a small `Long` GraphQL scalar mapped to Go `int64` for byte limits; no external dependency is required.

**Rationale**: Existing protobuf/datastore types use 64-bit byte counts, GraphQL `Int` is only 32-bit, and phase validation is explicitly deferred to GH#173. Typed nested objects prevent an unvalidated policy map while preserving omission as "no Namespace override."

**Alternatives considered**:
- Use GraphQL `Int` for byte limits: rejected because it cannot represent the existing `int64` contract.
- Define hook phases as GraphQL enums: deferred because current wire/config values contain Git-native hyphenated names and validation belongs to GH#173.
- Use JSON for all defaults: rejected because it violates the typed-contract requirement.

## Decision 7: Reuse the shared condition contract

**Decision**: `NamespaceStatus` contains `observedGeneration: Int!`, nullable `lastAppliedRevision`, and non-null `[Condition!]!`. The documentation defines `Ready`, `AdmissionAccepted`, and `DeletionBlocked` as the initial Namespace vocabulary, while the shared string-valued condition type remains open; the initial condition set is empty.

**Rationale**: This matches existing catalog status shapes and avoids a Namespace-specific status model or phase field.

**Alternatives considered**:
- Add `status.phase`: rejected because condition-based status is the established contract.
- Add a Namespace-specific `resolved` object: rejected because no computed Namespace aggregate exists in this feature.

## Decision 8: Add the declarative fields and deprecate the flat fields

**Decision**: Add the declarative Namespace fields to the existing GraphQL type, retain every previous flat output field with an explicit deprecation reason, and migrate in-repository consumers to the declarative fields. Removal is reserved for a future major GraphQL API release.

**Rationale**: The constitution requires deprecation before removal and a major version bump for breaking public-interface changes. Additive delivery keeps User Story 1 independently deployable while allowing low-priority consumers to migrate later.

**Alternatives considered**:
- Remove flat fields immediately: rejected because it violates mandatory public-interface versioning and breaks incremental delivery.
- Introduce a second query returning the new shape: rejected because additive fields on the existing resource provide a simpler migration path.

## Decision 9: Follow contract-first and test-first generation order

**Decision**:

1. Add failing GraphQL schema/contract tests, deprecation tests, converter tests, and manifest-example validation.
2. Author the Namespace SDL contract.
3. Regenerate gqlgen output.
4. Implement model conversion, keep deprecated compatibility projections, and update in-repository consumers.
5. Add documentation and end-to-end contract examples.

**Rationale**: This satisfies the constitution's API-first and test-first principles and keeps generated files downstream of the reviewed SDL.

**Alternatives considered**:
- Edit gqlgen-generated files directly: rejected because generated output is not the source of truth.
- Implement converters before schema tests: rejected by the test-first gate.

## Decision 10: Add no runtime dependency or service

**Decision**: Reuse Go 1.25, gqlgen v0.17.90, existing scalar infrastructure, existing datastore backends, and existing validation/test tooling.

**Rationale**: The feature is a schema and projection change. New libraries, services, caches, or queues would add complexity without providing value.

**Alternatives considered**:
- Add a Kubernetes API machinery dependency for metadata types: rejected as disproportionate and incompatible with the repository's existing lightweight contracts.

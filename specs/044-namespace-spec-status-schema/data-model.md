# Data Model: Namespace Resource Contract

## Namespace

`Namespace` is a top-level GitStore resource. It uses the same declarative envelope as Git-backed catalog resources but has no owning namespace.

| Field        | GraphQL type         | Ownership | Required | Notes                                                       |
|--------------|----------------------|-----------|----------|-------------------------------------------------------------|
| `id`         | `ID!`                | System    | Yes      | Relay transport identity; maps to the existing Namespace ID |
| `apiVersion` | `String!`            | Contract  | Yes      | Constant `gitstore.dev/v1beta1`                             |
| `kind`       | `String!`            | Contract  | Yes      | Constant `Namespace`                                        |
| `metadata`   | `NamespaceMetadata!` | Mixed     | Yes      | Dedicated namespace-less metadata                           |
| `spec`       | `NamespaceSpec!`     | Author    | Yes      | Desired state                                               |
| `status`     | `NamespaceStatus!`   | System    | Yes      | Observed state; never absent                                |

The existing flat GraphQL output fields remain on `Namespace` with deprecation reasons until a future major GraphQL API release:

| Deprecated field | Preferred field |
|---|---|
| `identifier` | `metadata.name` |
| `displayName` | `spec.title` |
| `tier` | `spec.tier` |
| `createdAt` | `metadata.creationTimestamp` |
| `createdBy` | No declarative equivalent |
| `updatedAt` | No declarative equivalent in GH#171 |
| `updatedBy` | No declarative equivalent |

## NamespaceMetadata

`NamespaceMetadata` mirrors the shared object metadata semantics without a `namespace` field.

| Field               | GraphQL type         | Ownership | Mutable          | Legacy-row hydration                                                                   |
|---------------------|----------------------|-----------|------------------|----------------------------------------------------------------------------------------|
| `name`              | `String!`            | Author    | No               | Existing `Identifier`                                                                  |
| `labels`            | `Map`                | Author    | Yes, through Git | Empty map                                                                              |
| `annotations`       | `Map`                | Author    | Yes, through Git | Empty map                                                                              |
| `uid`               | `ID!`                | System    | No               | Existing `ID`                                                                          |
| `resourceVersion`   | `String!`            | System    | System only      | `"1"` |
| `generation`        | `Int!`               | System    | System only      | `1`                                                                                    |
| `creationTimestamp` | `DateTime!`          | System    | No               | Existing `CreatedAt`                                                                   |
| `revision`          | `String`             | System    | System only      | Null until Git-backed admission exists                                                 |
| `ownerReferences`   | `[OwnerReference!]!` | System    | System only      | Empty list                                                                             |
| `finalizers`        | `[String!]!`         | System    | System only      | Empty list                                                                             |

### Identity rules

- `metadata.name` is the globally unique human-readable Namespace identifier.
- `metadata.uid` is the immutable system identity.
- GraphQL `id` remains the Relay identity and maps to the same current ID source.
- `metadata.namespace` does not exist.

## NamespaceSpec

| Field                | GraphQL type                  | Required | Meaning                                                                 |
|----------------------|-------------------------------|----------|-------------------------------------------------------------------------|
| `title`              | `String`                      | No       | Human-friendly Namespace title                                          |
| `tier`               | `NamespaceTier!`              | Yes      | Existing Namespace tier                                                 |
| `repositoryDefaults` | `NamespaceRepositoryDefaults` | No       | Partial defaults inherited by Repository resources                      |
| `pushPolicyDefaults` | `NamespacePushPolicyDefaults` | No       | Partial trusted policy defaults used during effective-policy resolution |

Every author-controlled change advances `metadata.generation` once persistent write semantics are implemented by GH#172.

## NamespaceRepositoryDefaults

| Field           | GraphQL type           | Required | Notes                                   |
|-----------------|------------------------|----------|-----------------------------------------|
| `visibility`    | `RepositoryVisibility` | No       | `PUBLIC`, `PRIVATE`, or `INTERNAL`      |
| `defaultBranch` | `String`               | No       | Default initial branch for repositories |

An omitted field means the Namespace supplies no override for that setting.

## RepositoryVisibility

| Value      | Meaning                                                                       |
|------------|-------------------------------------------------------------------------------|
| `PUBLIC`   | Repository may be visible without membership, subject to authorization policy |
| `PRIVATE`  | Repository requires explicit authorization                                    |
| `INTERNAL` | Repository is visible within the trusted GitStore installation boundary       |

Authorization behavior is not implemented by this feature.

## NamespacePushPolicyDefaults

| Field              | GraphQL type               | Required | Notes                                                          |
|--------------------|----------------------------|----------|----------------------------------------------------------------|
| `maxPackSizeBytes` | `Long`                     | No       | Signed 64-bit byte count; omission means no Namespace override |
| `maxFileSizeBytes` | `Long`                     | No       | Signed 64-bit byte count; omission means no Namespace override |
| `receivePackHooks` | `ReceivePackHookDefaults`  | No       | Per-hook enablement defaults                                   |
| `schemaValidation` | `SchemaValidationDefaults` | No       | Schema validation phase and timeout defaults                   |
| `admissionControl` | `AdmissionControlDefaults` | No       | Admission phase and branch-selection defaults                  |

Explicit zero retains the existing lower-level "unlimited" representation but may be rejected by GH#173 when it weakens an operator safety boundary.

## ReceivePackHookDefaults

Each field is an optional `HookToggle`. Omission means no Namespace override.

| Field                  | Git hook                |
|------------------------|-------------------------|
| `preReceive`           | `pre-receive`           |
| `update`               | `update`                |
| `postReceive`          | `post-receive`          |
| `procReceive`          | `proc-receive`          |
| `postUpdate`           | `post-update`           |
| `referenceTransaction` | `reference-transaction` |

### HookToggle

| Field     | GraphQL type | Required                              |
|-----------|--------------|---------------------------------------|
| `enabled` | `Boolean!`   | Yes when the toggle object is present |

## SchemaValidationDefaults

| Field            | GraphQL type | Required | Notes                                                 |
|------------------|--------------|----------|-------------------------------------------------------|
| `phase`          | `String`     | No       | Git-native hook phase spelling; validated by GH#173   |
| `timeoutSeconds` | `Int`        | No       | Timeout seconds; numeric bounds are defined by GH#173 |

## AdmissionControlDefaults

| Field           | GraphQL type | Required | Notes                                                                |
|-----------------|--------------|----------|----------------------------------------------------------------------|
| `phase`         | `String`     | No       | Git-native hook phase spelling; validated by GH#173                  |
| `branchPattern` | `String`     | No       | Regular-expression source; compilation/validation is owned by GH#173 |

## NamespaceStatus

| Field                 | GraphQL type    | Required | Initial value |
|-----------------------|-----------------|----------|---------------|
| `observedGeneration`  | `Int!`          | Yes      | `0`           |
| `lastAppliedRevision` | `String`        | No       | Null          |
| `conditions`          | `[Condition!]!` | Yes      | Empty list    |

Namespace has no `status.phase` and no kind-specific `status.resolved`.

### Documented condition vocabulary

The Namespace contract documentation defines the initial vocabulary:

- `Ready`
- `AdmissionAccepted`
- `DeletionBlocked`

Each condition uses the existing shared fields:

`type`, `status`, `observedGeneration`, `lastTransitionTime`, `reason`, and `message`.

`Condition.type` remains a shared string-valued field rather than a closed GraphQL enum, so future specifications may add condition names without changing this schema. No condition is required before a status-writing process has observed the resource.

## Legacy-row projection

The existing datastore entity remains unchanged in GH#171.

| Existing field             | Declarative field                             |
|----------------------------|-----------------------------------------------|
| `ID`                       | `id`, `metadata.uid`                          |
| `Identifier`               | `metadata.name`                               |
| `DisplayName`              | `spec.title`                                  |
| `Tier`                     | `spec.tier`                                   |
| `CreatedAt`                | `metadata.creationTimestamp`                  |
| N/A | `metadata.resourceVersion: "1"` |
| `CreatedBy`, `UpdatedBy`   | not exposed by the new resource contract      |

Missing declarative values receive the defaults listed above. Deprecated flat GraphQL fields continue to project their existing datastore values. No write or backfill occurs.

## State and lifecycle boundaries

This schema defines values but not their persistent transitions.

- GH#172 defines Git-driven create/update/delete, resource-version changes, generation increments, and status writes.
- GH#173 defines field validation, immutable-field rejection, policy ceilings, phase/regex validation, and admission outcomes.
- GH#174 defines watch events and resume semantics.
- GH#249 defines Repository override merging and effective push-policy resolution.

Deprecated flat GraphQL fields may be removed only in a future major GraphQL API release.

## GraphQL `Long` scalar

`Long` represents a signed 64-bit integer and maps to Go `int64`.

- Accept integer inputs within the `int64` range.
- Reject fractional, string, overflow, and underflow inputs with explicit GraphQL errors.
- Marshal as a GraphQL integer token.
- Add no external dependency.

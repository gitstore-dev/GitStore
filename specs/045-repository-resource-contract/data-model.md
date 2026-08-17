# Data Model: Repository Resource Contract

## Repository

The existing repository row remains the persistence aggregate.

| Field              | Type           | Authority           | Rules                                                                                                                                                                       |
|--------------------|----------------|---------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ID`               | string UUIDv7  | System              | Stable across rename and transfer; encoded as GraphQL Relay `id`/`metadata.uid`.                                                                                            |
| `NamespaceID`      | string UUIDv7  | System              | Resolves to the namespace identifier exposed as `metadata.namespace`.                                                                                                       |
| `Name`             | string         | Author metadata     | Required; mirrored by legacy `name` and `metadata.name`.                                                                                                                    |
| `DefaultBranch`    | string         | Owner spec          | Required after defaulting; mirrored by legacy `defaultBranch` and `spec.defaultBranch`.                                                                                     |
| `StorageClass`     | string         | System              | Existing field; exposed in legacy `storageClass` and `status.resolved.storageClass`.                                                                                        |
| `MaxPackSizeBytes` | int64          | Owner spec override | Existing push-policy limit; `0` means unlimited at the repository layer.                                                                                                    |
| `MaxFileSizeBytes` | int64          | Owner spec override | Existing push-policy limit; `0` means unlimited at the repository layer.                                                                                                    |
| `Generation`       | int64          | System              | Canonical minimum `1`; currently increments on rename (`Name`). Future write APIs for `DefaultBranch`, visibility, or push policy must use the same spec-transition helper. |
| `ResourceVersion`  | numeric string | System              | Canonical initial `"1"`; increments whenever the Repository resource changes.                                                                                               |
| `Status`           | JSON           | System              | Stores observed generation, last-applied revision, and conditions; missing/empty becomes the canonical initial status.                                                      |
| `CreatedAt`        | timestamp      | System              | Exposed as legacy `createdAt` and `metadata.creationTimestamp`.                                                                                                             |
| `CreatedBy`        | string         | System              | Existing audit field; retained unchanged.                                                                                                                                   |
| `UpdatedAt`        | timestamp      | System              | Existing audit field; retained unchanged.                                                                                                                                   |
| `UpdatedBy`        | string         | System              | Existing audit field; retained unchanged.                                                                                                                                   |

Repository visibility and the extended hook/schema/admission override shape are
reserved read-contract fields inspired by the Namespace defaults in PR #345.
All rows project `PRIVATE` visibility and null extended override groups until a
future feature defines their write and persistence semantics; the existing
default-branch and maximum-size fields project persisted values immediately.

## GraphQL Repository

The existing type gains the complete resource envelope:

| Field | Value |
|---|---|
| `apiVersion` | Constant `gitstore.dev/v1beta1` |
| `kind` | Constant `Repository` |
| `metadata` | Shared namespace-scoped `ObjectMeta` |
| `spec` | Author-controlled desired state |
| `status` | System-owned observed and resolved state |

Every duplicate legacy output field remains temporarily available with an
`@deprecated` reason pointing to its declarative replacement.

### `metadata: ObjectMeta!`

| Field | Source |
|---|---|
| `name` | `Repository.Name` |
| `namespace` | owning `Namespace.Identifier` |
| `uid` | Relay-encoded `Repository.ID` |
| `resourceVersion` | normalized `Repository.ResourceVersion` |
| `generation` | normalized `Repository.Generation` |
| `creationTimestamp` | `Repository.CreatedAt` |
| `labels` / `annotations` | null |
| `revision` | null |
| `ownerReferences` | empty list |

### `spec: RepositorySpec!`

| Field | Type | Validation |
|---|---|---|
| `defaultBranch` | `String!` | Same value and rules as the existing default branch. |
| `visibility` | `RepositoryVisibility!` | Reserved in this feature; always projects `PRIVATE` until persistence/write semantics exist. |
| `pushPolicy` | `RepositoryPushPolicy!` | Existing Repository policy plus optional extended overrides. |

`RepositoryPushPolicy` mirrors PR #345's Namespace policy-default vocabulary:

| Field | Type | Projection |
|---|---|---|
| `maxPackSizeBytes` | `Long!` | Existing `MaxPackSizeBytes`; zero remains explicit unlimited at this layer. |
| `maxFileSizeBytes` | `Long!` | Existing `MaxFileSizeBytes`; zero remains explicit unlimited at this layer. |
| `receivePackHooks` | `ReceivePackHookDefaults` | Null until Repository hook override persistence exists. |
| `schemaValidation` | `SchemaValidationDefaults` | Null until Repository validation override persistence exists. |
| `admissionControl` | `AdmissionControlDefaults` | Null until Repository admission override persistence exists. |

### `status: RepositoryStatus!`

| Field | Type | Source |
|---|---|---|
| `observedGeneration` | `Int!` | Stored status; defaults to `0` before any status writer observes the resource. |
| `lastAppliedRevision` | `String` | Stored status; defaults to null. |
| `conditions` | `[Condition!]!` | Decoded from `Repository.Status`; defaults to an empty list. |
| `resolved` | `ResolvedRepositoryDefinition!` | Always-present system-computed storage values. |

`ResolvedRepositoryDefinition` contains:

| Field | Type | Source |
|---|---|---|
| `storagePath` | `String!` | Derived from repository ID and configured data root. |
| `storageClass` | `String!` | `Repository.StorageClass`. |

This feature defines an empty initial Repository condition vocabulary: no
Repository-specific condition types are emitted because no condition-producing
writer exists. Any future writer must define condition types, ownership,
transition rules, reasons, and observed-generation behavior before use.

## Legacy GraphQL deprecations

| Legacy field | Preferred field |
|---|---|
| `name` | `metadata.name` |
| `namespace` | `metadata.namespace` |
| `defaultBranch` | `spec.defaultBranch` |
| `storageClass` | `status.resolved.storageClass` |
| `storagePath` | `status.resolved.storagePath` |
| `createdAt` | `metadata.creationTimestamp` |
| `createdBy` | No declarative equivalent |
| `updatedAt` | No declarative equivalent |
| `updatedBy` | No declarative equivalent |

Relay `id` remains required by `Node` and is not deprecated.

## Relationships

- A Repository belongs to exactly one Namespace through `NamespaceID`.
- Namespace transfer changes `NamespaceID` and `metadata.namespace` without changing `ID`.
- NamespaceMapping remains the path lookup join and is not part of the declarative envelope.

## State Transitions

| Transition | UID | Generation | ResourceVersion | Status |
|---|---|---|---|---|
| Create | New | `1` | `"1"` | Empty conditions |
| Rename (`metadata.name`) | Preserved | `+1` | `+1` | Preserved |
| Transfer | Preserved | Unchanged | `+1` | Preserved |
| Future default-branch/visibility/push-policy write | Preserved | `+1` via shared spec helper | `+1` | Preserved |
| System/status update (future) | Preserved | Unchanged | `+1` | Updated |
| Delete | Removed | N/A | N/A | N/A |

Legacy rows normalize to the create state before being returned or transitioned.

## Validation and Error Handling

- GraphQL `apiVersion`, `kind`, `metadata`, `spec`, `status`,
  `status.conditions`, and `status.resolved` are never null.
- Stored malformed status JSON must be logged with field/resource context and returned as the safe empty-condition status.
- Counter normalization accepts empty, zero, negative, or non-numeric legacy resourceVersion as canonical `"1"`.
- Repository policy override values must not weaken operator-required limits or checks; enforcement is deferred to the admission/policy feature.
- Existing repository operation validation, authorization, error messages, and rollback behavior are unchanged.

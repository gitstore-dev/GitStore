# Data Model: Namespace Types — Remove Enterprise

**Branch**: `030-remove-enterprise-namespace` | **Date**: 2026-06-19

## Namespace Entity (after change)

The `Namespace` entity retains all existing fields except `ParentEnterpriseID`, which is removed from both the Go struct and the Scylla schema.

| Field         | Type            | Constraints                                                                                                |
|---------------|-----------------|------------------------------------------------------------------------------------------------------------|
| `ID`          | `string` (UUID) | System-generated; immutable                                                                                |
| `Identifier`  | `string`        | Globally unique; DNS label format (1–63 chars, lowercase alphanumeric + hyphens); immutable after creation |
| `DisplayName` | `string`        | Optional; mutable                                                                                          |
| `Tier`        | `NamespaceTier` | Enum: `user` \| `organization`; required; immutable after creation                                         |
| `CreatedAt`   | `time.Time`     | System-set on create                                                                                       |
| `CreatedBy`   | `string`        | Username of creator; system-set                                                                            |
| `UpdatedAt`   | `time.Time`     | System-set on any mutation                                                                                 |
| `UpdatedBy`   | `string`        | Username of last updater; system-set                                                                       |

### Removed fields

| Field                        | Reason                                                                                                |
|------------------------------|-------------------------------------------------------------------------------------------------------|
| `ParentEnterpriseID *string` | Only relevant for the `enterprise` tier. Removed from the Go struct, all generated/hand-written code. |

## NamespaceTier Enum (after change)

| Go constant                 | Datastore string | GraphQL enum value | Description                                            |
|-----------------------------|------------------|--------------------|--------------------------------------------------------|
| `NamespaceTierUser`         | `"user"`         | `USER`             | Owned by an individual; directly owns repositories     |
| `NamespaceTierOrganization` | `"organisation"` | `ORGANIZATION`     | Owned by a team or company; directly owns repositories |

> **Spelling note**: The Go constant and GraphQL enum use the American spelling `organization`. The datastore string value remains `"organisation"` (unchanged for existing rows). The converter maps `"organisation"` → `ORGANIZATION` at runtime; no data migration is required.

### Removed value

| Value                                                     | Reason                                                                                                                                           |
|-----------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| `NamespaceTierEnterprise` / `"enterprise"` / `ENTERPRISE` | Does not map to the `/{namespace}/{repo}` path model. Enterprise-level modeling, if required in future, belongs outside the namespace type enum. |

## Validation Rules (after change)

| Rule                                     | Behaviour                                                                                                     |
|------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| `tier` must be `USER` or `ORGANIZATION`  | Any other value (including `ENTERPRISE`) is rejected at the GraphQL schema layer before reaching the resolver |
| `parentEnterpriseIdentifier` input field | Removed from `CreateNamespaceInput`; sending it is a schema validation error                                  |
| Reserved identifier `"enterprise"`       | Retained — the string `enterprise` remains a reserved identifier and cannot be used as a namespace slug       |

## Datastore Notes

- Drop the `parent_enterprise_id` column from the `namespaces`.
- The `memdb` in-memory datastore does not persist the `ParentEnterpriseID` field after the struct change; no separate migration needed there.

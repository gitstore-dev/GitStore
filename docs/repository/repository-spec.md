# Repository Resource Contract

Repository is a namespace-scoped GitStore resource represented by the
`gitstore.dev/v1beta1` declarative read contract. Existing synchronous
repository mutations remain unchanged; this contract separates declarative
configuration from system-managed metadata and status.

## Hydrated API representation

```yaml
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme
  labels: {}
  annotations: {}
  uid: UmVwb3NpdG9yeTowMTk2MDAwMC0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwNDU
  resourceVersion: "1"
  generation: 1
  creationTimestamp: 2026-08-16T12:00:00Z
  revision: null
  ownerReferences: []
spec:
  defaultBranch: main
  visibility: PRIVATE
  pushPolicy:
    maxPackSizeBytes: 0
    maxFileSizeBytes: 0
    receivePackHooks: null
    schemaValidation: null
    admissionControl: null
status:
  observedGeneration: 0
  lastAppliedRevision: null
  conditions: []
  resolved:
    storagePath: /data/repos/01/96/01960000-0000-7000-8000-000000000045.git
    storageClass: default
```

## Ownership and mutability

| Field group                                       | Source                             | Mutability in this feature                                    |
|---------------------------------------------------|------------------------------------|---------------------------------------------------------------|
| `apiVersion`, `kind`                              | Contract                           | Immutable constants                                           |
| `metadata.name`                                   | Existing repository name           | Mutable through `renameRepository`                            |
| `metadata.namespace`                              | Owning Namespace identifier        | Mutable through `transferRepository`                          |
| `metadata.uid`, `metadata.creationTimestamp`      | System                             | Immutable                                                     |
| `metadata.resourceVersion`, `metadata.generation` | System                             | Maintained by existing lifecycle operations                   |
| `spec.defaultBranch`                              | Persisted repository configuration | Read-only projection; existing create input remains unchanged |
| `spec.pushPolicy.max*Bytes`                       | Persisted repository limits        | Read-only projection                                          |
| `spec.visibility`                                 | Reserved contract field            | Always `PRIVATE`                                              |
| Extended push-policy groups                       | Reserved contract fields           | Always null                                                   |
| `status`                                          | System                             | No Repository status-write API exists in this feature         |
| `status.resolved`                                 | System                             | Derived from repository identity and storage configuration    |

Zero maximum pack/file sizes retain the existing unlimited sentinel.
Visibility and extended policy groups are deterministic placeholders until a
future feature defines their write, persistence, validation, and inheritance
semantics.

## Version transitions

| Transition                 | UID       | Generation | ResourceVersion | Status         |
|----------------------------|-----------|------------|-----------------|----------------|
| Create                     | New       | `1`        | `"1"`           | Initial status |
| Rename                     | Preserved | `+1`       | `+1`            | Preserved      |
| Transfer                   | Preserved | Unchanged  | `+1`            | Preserved      |
| Future spec write          | Preserved | `+1`       | `+1`            | Preserved      |
| Future system/status write | Preserved | Unchanged  | `+1`            | Updated        |

Rows created before this contract normalize to generation `1`,
resourceVersion `"1"`, and
`{"observedGeneration":0,"conditions":[]}` before reads or transitions.

## Condition vocabulary

This feature has no Repository controller, reconciler, status mutation, or
condition-producing writer. The valid initial Repository condition vocabulary
is therefore empty and `status.conditions` is an empty list.

A future writer must define its condition types, ownership, transition rules,
reasons, and observed-generation behavior before emitting conditions. It must
continue to use the shared `Condition` GraphQL type.

## Legacy GraphQL fields

The legacy `name`, `namespace`, `defaultBranch`, `storageClass`, `storagePath`,
`createdAt`, `createdBy`, `updatedAt`, and `updatedBy` fields remain selectable
with their existing values and explicit deprecation reasons. Relay `id` remains
non-deprecated. Removal requires a future major GraphQL API release.

## Valid and invalid expectations

Valid:

- An existing row with no contract fields returns non-null metadata, spec,
  status, conditions, and resolved storage state.
- Explicit zero policy limits remain visible as zero.
- Rename preserves UID while advancing both counters.
- Transfer preserves UID and generation while advancing resourceVersion.

Invalid:

- Treating `visibility` or extended policy groups as currently writable.
- Emitting a Repository-specific condition type without a separately defined
  writer contract.
- Resetting identity or counters during transfer.
- Returning null status or resolved storage for a legacy row.

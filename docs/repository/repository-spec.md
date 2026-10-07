# Repository Resource Contract

Repository is a namespace-scoped GitStore resource represented by the
`gitstore.dev/v1beta1` declarative contract. Non-bootstrap repositories are
authored as manifests in their Namespace's `gitstore-system` repository;
GraphQL create/update mutations commit the same manifest shape.

After the Git commit succeeds, both GraphQL convergence and post-receive use
the shared committed-manifest admission service. It validates the committed
content/ref/operation, rejects superseded commits, applies the same catalog
admission rules, and then GraphQL hydrates the admitted record from the
datastore. Mutations do not maintain a second direct-write admission path.

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
  ownerReferences:
    - apiVersion: gitstore.dev/v1beta1
      kind: Namespace
      name: acme-store
      uid: <Namespace persistent UID>
      blockOwnerDeletion: true
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
| `metadata.name`, `metadata.namespace`             | Manifest identity                  | Immutable after admission                                     |
| `metadata.uid`, `metadata.creationTimestamp`      | System                             | Immutable                                                     |
| `metadata.resourceVersion`, `metadata.generation` | System                             | Maintained by existing lifecycle operations                   |
| `spec.defaultBranch`                              | Repository manifest                | Mutable through manifest or `updateRepository`                |
| `spec.pushPolicy.max*Bytes`                       | Persisted repository limits        | Read-only projection                                          |
| `spec.visibility`                                 | Reserved contract field            | Always `PRIVATE`                                              |
| Extended push-policy groups                       | Reserved contract fields           | Always null                                                   |
| `status`                                          | Controller                         | `updateRepositoryStatus`, never author writable               |
| `status.resolved`                                 | System                             | Derived from repository identity and storage configuration    |

Zero maximum pack/file sizes retain the existing unlimited sentinel.
Visibility and extended policy groups are deterministic placeholders until a
future feature defines their write, persistence, validation, and inheritance
semantics.

## Version transitions

| Transition              | UID       | Generation | ResourceVersion | Status            |
|-------------------------|-----------|------------|-----------------|-------------------|
| Create                  | New       | `1`        | `"1"`           | Initial status    |
| Manifest spec write     | Preserved | `+1`       | `+1`            | Admission updated |
| Controller status write | Preserved | Unchanged  | `+1`            | Updated           |

Rows created before this contract normalize to generation `1`,
resourceVersion `"1"`, and
`{"observedGeneration":0,"conditions":[]}` before reads or transitions.

## Condition vocabulary

`AdmissionAccepted` is written by catalog admission. `StorageProvisioned` and
`Ready` are controller-owned; `Terminating` is derived from the deletion marker
and foreground-deletion finalizer. Authors cannot submit `status` or
`metadata.ownerReferences`; admission rejects either attempt.

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
- `renameRepository` and `transferRepository` are deprecated and return
  `Unimplemented` until ADR-0003 Phase 2.

Invalid:

- Setting status or owner references in an authored manifest.
- Treating a raw datastore UID as a public identity; `metadata.uid == id`.
- Returning null status or resolved storage for a legacy row.

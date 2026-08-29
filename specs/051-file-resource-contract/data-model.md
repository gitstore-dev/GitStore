# Data Model: File Resource Contract

## File datastore entity

The File row is the durable aggregate for a Git-backed manifest and its
system-owned read state.

| Field                                | Type              | Authority             | Rules                                                                                    |
|--------------------------------------|-------------------|-----------------------|------------------------------------------------------------------------------------------|
| `UID`                                | string            | System                | Stable identity assigned at first admission.                                             |
| `Namespace`                          | string            | Push context/identity | Required after namespace inheritance; immutable.                                         |
| `Name`                               | string            | Author/identity       | Required, unique within namespace, immutable.                                            |
| `APIVersion`                         | string            | Contract              | `storage.gitstore.dev/v1beta1`.                                                          |
| `Kind`                               | string            | Contract              | `File`.                                                                                  |
| `Generation`                         | int64             | System                | Advances for author spec changes.                                                        |
| `ResourceVersion`                    | string            | System                | Advances for every persisted resource/status change.                                     |
| `CreationTimestamp`                  | timestamp         | System                | Read-only.                                                                               |
| `Revision`                           | string            | Git/admission         | Last admitted source revision.                                                           |
| `Labels`/`Annotations`               | map[string]string | Author                | Writable metadata.                                                                       |
| `OwnerReferences`                    | JSON              | System                | Repository-scoped, never author-writable.                                                |
| `Finalizers`                         | list[string]      | System                | Compatible with future deletion/finalizer flow; no File payload finalizer in this phase. |
| `DeletionTimestamp`                  | timestamp         | System                | Null until future deletion flow.                                                         |
| `RepositoryID`                       | string            | System                | Repository scope for identity/ownership.                                                 |
| `SourcePath`/`GitCommitSHA`/`GitRef` | string            | System                | Git provenance.                                                                          |
| `Spec`                               | JSON              | Author                | FileSpec; stored separately from status.                                                 |
| `Body`                               | string            | Author                | Markdown alt text; empty is valid.                                                       |
| `Status`                             | JSON              | System                | FileStatus; never read from Git.                                                         |

## FileSpec

| Field                       | Type                     | Rules                                                 |
|-----------------------------|--------------------------|-------------------------------------------------------|
| `contentType`               | string                   | Required, non-empty, immutable after first admission. |
| `type`                      | string                   | Optional free-form classification.                    |
| `source`                    | FileSourceDefinition     | Required.                                             |
| `processing.image.variants` | list[FileVariantRequest] | Optional; every entry requires `name`.                |

`FileSourceDefinition.type` is one of `git`, `lfs`, `s3`, or `gcs`; `uri` is
required and non-empty. Optional checksum requires algorithm/value. Optional
`SecretRef` has kind/name/key/namespace and is same-namespace only.

## FileStatus

| Field                 | Type                   | Rules                               |
|-----------------------|------------------------|-------------------------------------|
| `observedGeneration`  | int64                  | System/controller-owned.            |
| `lastAppliedRevision` | string                 | System/controller-owned.            |
| `conditions`          | list[Condition]        | Fixed File condition types only.    |
| `resolved`            | ResolvedFileDefinition | Non-null generic status projection. |

Allowed condition types: `AdmissionAccepted`, `SourceResolved`,
`ProcessingComplete`, `Ready`, `Terminating`. Allowed statuses: `True`,
`False`, `Unknown`. Initial admission sets `AdmissionAccepted=True` and
`Ready=True`; source/processing conditions are absent.

`ResolvedFileDefinition` contains a `resolvedVariants` placeholder. It does not
cause source reads or processing in this phase.

## Relationships and transitions

- Identity is `(apiVersion, kind, namespace, metadata.name)`, with namespace
  inherited from repository context when omitted.
- A File manifest conventionally lives at `files/<metadata.name>.md`.
- Admission creates or updates the durable row and initializes status.
- Name, namespace, and content type are immutable according to the spec.
- Status/watch updates use generic resource-version guarded contracts.
- Payload resolution, processing, deletion, finalizer draining, and dependent
  `fileRef` validation are deferred.

## Validation errors

Reject missing/wrong kind or API version, missing name/content type/source
fields, invalid source type, empty URI, unnamed variants, cross-namespace
credentials, duplicate identity, immutable content-type changes, author
status, read-only metadata, and unsupported File condition types with
field-specific errors.

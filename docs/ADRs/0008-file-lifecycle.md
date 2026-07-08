# ADR 0008: File Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`File` is the technical media primitive. It owns source metadata, content type,
checksum, processing hints, and resolved binary variants (renditions). It is not a
binary payload stored in git — it is a manifest that points at binary content stored
in Git LFS or object storage. Catalog resources (`Product`, `ProductVariant`,
`CategoryTaxonomy`, `Collection`) reference `File` records via `fileRef`.

`File` frontmatter (GH#79) is still an open issue in Phase 1. This ADR closes the
lifecycle decisions for Phase 1, with Phase 2 items explicitly deferred.

`File` is a technical resource. `MediaAsset` is the catalog-facing semantic layer
that points at a `File` and adds presentation metadata (`role`, `altTextRef`,
`focalPoint`). These are separate resources with different lifecycles. This ADR covers
`File` only.

## Decision

`File` is **Git-backed** for the manifest. The binary payload is stored in Git LFS or
object storage; the `File` manifest contains only the pointer, checksum, and
processing hints.

### Storage classification

| Layer             | Owner                                                             |
|-------------------|-------------------------------------------------------------------|
| Desired state     | Git frontmatter (Markdown manifest in repo)                       |
| Binary payload    | Git LFS or object storage (not in git tree directly)              |
| Hydrated record   | Datastore (ScyllaDB/memDB)                                        |
| Processing output | Object storage; manifest ref in datastore (controller-managed)    |
| Status            | Datastore; controller-managed                                     |
| Finalizers        | Datastore; controller-managed                                     |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.contentType`,
`spec.type`, `spec.source`, `spec.processing`, Markdown body (alt text).

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.ownerReferences`,
`status.*`, `status.resolvedVariants`.

### File as manifest vs object-storage pointer

The `File` manifest is both a manifest and an object-storage pointer in the same
resource:

- **Manifest role**: declares `contentType`, `type`, source URI, checksum, and
  processing hints in git. This is the desired state.
- **Object-storage pointer role**: `spec.source.uri` is a pointer to the actual binary
  payload (a Git LFS URL, an `s3://` URI, or a `git:///` relative pointer to an
  LFS-tracked file in the same repository). The binary is not stored inline.

The controller resolves the source URI, verifies the checksum, and updates
`status.resolvedVariants` with processing output URLs. These derived fields are
controller-managed and never authored in git.

### Lifecycle rules

#### Create

**Git push path (canonical):**

1. Author creates `files/<name>.md` in a repository and pushes.
2. Pre-receive validates: envelope, `kind: File`, `spec.contentType` non-empty,
   `spec.source.type` is a known value (`git`, `lfs`, `s3`, `gcs`),
   `spec.source.uri` non-empty.
3. Post-receive admission:
   - Namespace and repository `Active`.
   - `ownerReferences` written pointing at repository.
   - `AdmissionAccepted=True`.
   - Controller is enqueued to resolve the source URI and verify the checksum.
4. Controller resolves source (Phase 2: verifies checksum against `spec.source.checksum`,
   triggers processing pipeline if `spec.processing` is set), and sets
   `SourceResolved=True` and `Ready=True` when the payload is accessible.

**GraphQL mutation path:**

`createFile` commits `files/<name>.md` to the named repository (or `gitstore-system`)
and delegates to git admission. The binary payload upload is a **separate operation**:
the caller must upload the binary to the configured object storage (or push via Git LFS)
before or after committing the manifest. The mutation does not handle binary payload
upload in Phase 1.

Phase 2 will add a `requestFileUpload` mutation that returns a signed upload URL and
auto-commits the manifest after a successful upload completes. For Phase 1, binary
upload is an out-of-band operation.

#### Update

1. Author edits the file manifest and pushes, or issues `updateFile`.
2. `updateFile` commits an updated manifest; waits for admission.
3. Updating `spec.source.uri` or `spec.source.checksum` triggers controller
   re-verification.
4. Updating `spec.processing` triggers controller re-processing (Phase 2).
5. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.
6. `spec.contentType` is immutable after the first successful admission (a content-type
   change implies a new file, not an in-place edit).

#### Delete

1. Author deletes the file manifest and pushes, or issues `deleteFile`.
2. Before any record is removed, admission checks whether any `Product`,
   `ProductVariant`, `CategoryTaxonomy`, or `Collection` records have a `fileRef`
   pointing at this file.
   - If referencing records exist, the delete is **rejected** with
     `FailedPrecondition: file references present`.
3. If no referencing records exist, the API adds the `gitstore.dev/foreground-deletion`
   finalizer and sets `metadata.deletionTimestamp`.
4. Controller removes any controller-managed derived artefacts (processing output
   pointers) from the datastore and then removes the finalizer.
5. Datastore record is hard-deleted.
6. **Binary payload is not deleted by this operation.** Object-storage cleanup is a
   separate lifecycle concern managed by the operator or a background retention policy.
   This avoids accidental deletion of shared assets. Phase 2 will add a
   `purgeFilePayload` operation.

### Reference checking model

`File` references from catalog resources (`spec.media[*].fileRef`, `spec.source.fileRef`)
are validated in two phases:

| Phase      | Check                                                                                         |
|------------|-----------------------------------------------------------------------------------------------|
| Push-time  | Structural: `fileRef.name` and `fileRef.kind` fields are present and non-empty.               |
| Controller | Semantic: the referenced `File` record exists in the same namespace (async, Phase 2, GH#244). |

Cross-namespace `fileRef` is **rejected at admission time** in Phase 1. All referenced
`File` records must be in the same namespace as the referencing resource.

### File location convention

When a repository name is not specified in a GraphQL mutation:

```
files/<metadata.name>.md
```

### Git write path

```markdown
---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: macbook-pro-hero
  namespace: acme-store
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
  source:
    type: s3
    uri: s3://acme-assets/products/macbook-pro-hero.jpg
    checksum:
      algorithm: sha256
      value: abc123...
    credentialsRef:
      kind: SecretRef
      name: s3-catalog-assets
  processing:
    image:
      variants:
      - width: 800
        format: webp
---

Hero image for MacBook Pro.
```

### GraphQL mutation delegation

| Mutation            | Phase 1 behaviour                                                                                                                            |
|---------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
| `createFile`        | Commits `files/<name>.md` to the named repository (or `gitstore-system`); waits for admission. Binary upload is a separate out-of-band step. |
| `updateFile`        | Commits updated manifest; waits for admission.                                                                                               |
| `deleteFile`        | Validates no `fileRef` references exist; adds `foregroundDeletion` finalizer.                                                                |
| `getFile`           | Read-only datastore query; includes controller-computed `resolvedVariants` (Phase 2).                                                        |
| `listFiles`         | Read-only datastore query, namespace-scoped.                                                                                                 |
| `requestFileUpload` | **Phase 2 only.** Returns a signed upload URL; auto-commits manifest on upload completion.                                                   |

There is no direct datastore write path for `File` in Phase 1.

### Validation and admission rules

| Phase       | Rule                                                                                                                                                                         |
|-------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive | Envelope valid; `kind: File`; `spec.contentType` non-empty; `spec.source.type` is a known value; `spec.source.uri` non-empty.                                                |
| Admission   | Namespace and repository `Active`; `spec.contentType` immutability check if updating; `credentialsRef` same-namespace rule (ADR-0001).                                       |
| Controller  | Checksum verification against source URI (Phase 2); processing pipeline trigger (Phase 2); `fileRef` back-reference validation from referencing resources (Phase 2, GH#244). |

### Status and reconciliation behaviour

| Condition            | Meaning                                                                             |
|----------------------|-------------------------------------------------------------------------------------|
| `AdmissionAccepted`  | File manifest stored in datastore.                                                  |
| `SourceResolved`     | Source URI is accessible and checksum verified (Phase 2).                           |
| `ProcessingComplete` | All requested variants/renditions have been generated (Phase 2).                    |
| `Ready`              | Manifest admitted; source accessible (Phase 2).                                     |
| `Terminating`        | `foregroundDeletion` finalizer present; `fileRef` references must be removed first. |

In Phase 1, `SourceResolved` and `ProcessingComplete` are not set (controller work is
deferred). `Ready` is set after `AdmissionAccepted=True` until Phase 2 controller work
is implemented.

### Relationship to MediaAsset

`File` is the technical storage primitive. `MediaAsset` is a separate git-backed
resource that points at a `File` and adds catalog-facing metadata:

```
MediaAsset.spec.fileRef → File
```

`MediaAsset` has its own lifecycle and owns the presentation-layer metadata (`role`,
`altTextRef`, `focalPoint`). Deleting a `File` that has `MediaAsset` records pointing
at it is rejected (same `fileRef` reference-checking rule applies to `MediaAsset`).
`MediaAsset` lifecycle is not covered in this Phase 1 ADR; it is a Phase 2 resource.

## Consequences

Positive:
- File manifests are reviewable and auditable through git history.
- Binary payloads stay outside git (LFS or object storage); git history remains clean.
- Deletion is safe: all `fileRef` references must be removed before the manifest can
  be deleted.
- Controller-based checksum verification and processing are cleanly separated from
  push admission.

Negative:
- Binary payload upload is an out-of-band step in Phase 1; callers must coordinate the
  upload and the manifest push separately.
- `SourceResolved` and `ProcessingComplete` conditions are not populated in Phase 1;
  `Ready=True` is set optimistically after admission.
- Object-storage cleanup after file deletion is deferred to Phase 2.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`.
- [ADR-0004](0004-product-lifecycle.md) — Products reference files via
  `spec.media[*].fileRef`; product deletion does not affect the file, but file deletion
  is blocked while products reference it.
- [ADR-0005](0005-product-variant-lifecycle.md) — Variants reference files via
  `spec.media[*].fileRef` (same rule).
- [ADR-0006](0006-category-taxonomy-lifecycle.md) — Categories reference files via
  `spec.media[*].fileRef` (same rule, Phase 2 resolution per GH#244).
- [ADR-0007](0007-collection-lifecycle.md) — Collections reference files via
  `spec.media[*].fileRef` (same rule, Phase 2 resolution).
- [ADR-0001](0001-secretref-reference-contract.md) — `spec.source.credentialsRef` uses
  the `SecretRef` contract for object-storage credentials.

## Dependency graph position

```
Namespace (ADR-0002)
  └─► Repository (ADR-0003)
        └─► File (this ADR)
              ├── spec.source.credentialsRef → SecretRef (ADR-0001)
              └── ◄── fileRef from Product / ProductVariant / CategoryTaxonomy / Collection
                       (reverse refs; File does not reference back)
```

**No circular dependency risk:** File is referenced by catalog resources but does not
reference any catalog resource. The dependency is one-way.

## Alternatives considered

### Store binary payload in git directly (no LFS)

Rejected. Git is not suited for large binary payloads. Storing image files directly in
the tree would make repository history noisy and clone times unacceptable for large
catalogs. Git LFS and object storage are the standard patterns for binary assets.

### Merge File and MediaAsset into a single resource

Rejected per `docs/resources/git-backed.md`: "Do not model `MediaAsset` as a wrapper
around `File`; the two resources have different lifecycles and ownership." `File` owns
the technical storage and checksum contract; `MediaAsset` owns the catalog-facing
semantic layer. Merging them would couple byte-level storage decisions to
presentation-layer edits and create a single large resource with mixed concerns.

### Allow file deletion to cascade to binary payload

Rejected for Phase 1. Silent deletion of object-storage assets is a destructive
out-of-band side effect that requires careful operator confirmation. A `purgeFilePayload`
operation that is explicit and auditable is the safer Phase 2 design.

# ADR 0008: File Lifecycle

**Status**: Proposed

**Date**: 2026-06-26

**Updated**: 2026-10-14

**Audience**: GitStore API, controller, admission, and catalog authors.

## Context

`File` is the technical media primitive. It owns source metadata, content type,
checksum, processing hints, and resolved binary variants (renditions). It is a
Git-backed manifest whose source may be a repository blob, Git LFS object, or
external provider; the first payload-materialization phase copies the declared
source bytes to configured object storage. Catalog resources (`Product`, `ProductVariant`,
`CategoryTaxonomy`, `Collection`) reference `File` records via `fileRef`.

The `File` manifest contract was delivered by spec 051, closing GH#79, #192,
#194 and #195. This ADR remains the lifecycle design; follow-on work is now
split across spec 056 (reference safety and renewable source access), spec 063
(secret-material boundary), and the still-open storage/media-delivery issues.
Those implementation documents take precedence over phase assumptions in this
older ADR.

`File` is a technical resource. `MediaAsset` is the catalog-facing semantic layer
that points at a `File` and adds presentation metadata (`role`, `altTextRef`,
`focalPoint`). These are separate resources with different lifecycles. This ADR covers
`File` only.

## Decision

`File` is **Git-backed** for the manifest. The manifest declares a source
locator; v1beta1 permits repository blobs, Git LFS, and external providers.
The first materialization phase copies the declared bytes to configured object
storage. The File contract can describe processing intent, but processing is
not part of that initial phase.

### Storage classification

| Layer             | Owner                                                             |
|-------------------|-------------------------------------------------------------------|
| Desired state     | Git frontmatter (Markdown manifest in repo)                       |
| Declared source   | Repository blob, Git LFS, or external provider as allowed by the source contract |
| Managed payload   | Configured object storage; this is the materialization destination |
| Hydrated record   | Datastore (ScyllaDB/memDB)                                        |
| Processing output | Object storage; manifest ref in datastore (controller-managed)    |
| Status            | Datastore; controller-managed                                     |
| Finalizers        | Datastore; controller-managed                                     |

Git-authored fields: `apiVersion`, `kind`, `metadata.*` (non-system), `spec.contentType`,
`spec.type`, `spec.source`, `spec.processing`, Markdown body (alt text).

Controller-managed fields (not author-writable): `metadata.uid`,
`metadata.resourceVersion`, `metadata.generation`, `metadata.ownerReferences`,
`status.*`, including `status.resolved`. `status` uses conditions only; there
is no `status.phase` field (spec 051 / ADR 0008 lifecycle clarification).

### File as manifest, source pointer, and managed payload

The `File` manifest is both a manifest and an object-storage pointer in the same
resource:

- **Manifest role**: declares `contentType`, `type`, source URI, checksum, and
  processing hints in git. This is the desired state.
- **Source pointer role**: `spec.source.type` and `spec.source.uri` identify the
  source bytes (currently `git`, `lfs`, `s3`, or `gcs` in the v1beta1 schema).
  For `git`, the URI is repository-relative; external source access is authorized
  separately from naming the URI. These source locations are not themselves the
  managed payload destination or a storefront URL.

The source-materialization controller retrieves the declared source blob,
writes it to the configured object store, and records non-secret resolved
object metadata in status. Status must not contain bearer URLs. The first phase ignores `spec.processing`: it does
not run transformations or create image, video, or 3D renditions. Later source
verification, renewable access, reference propagation and transformation work
are defined by their own specifications. Resolved data is controller-managed
and never authored in Git.

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
   - Durable File observation is recorded for controller reconciliation.
4. The initial payload phase reads the source blob and writes it to configured
   object storage. It ignores `spec.processing`; no transformation worker or
   rendition is required. Source verification and renewable private-source
   access are specified separately by spec 056 and are not implied by manifest
   admission.

#### Update

1. Author edits the file manifest in its repository and pushes the commit.
2. Admission validates the changed manifest and updates the hydrated File record.
3. Updating source identity/checksum triggers a new source reconciliation.
4. Changes to `spec.processing` are retained as desired state but ignored by
   the initial payload-materialization phase. A later processing feature must
   define validation, cost limits and output lifecycle before these fields
   trigger work.
5. Immutable fields in Phase 1: `metadata.name`, `metadata.namespace`.
6. `spec.contentType` is immutable after the first successful admission (a content-type
   change implies a new file, not an in-place edit).

#### Delete

1. Author deletes the file manifest from its repository and pushes the commit.
2. File deletion is not blocked by catalog `fileRef` consumers. It begins the
   foreground deletion lifecycle and durably schedules bounded decoupling by
   the File UID, per spec 056. Previously resolved required references become
   unavailable with a distinct deleted-File reason; optional references are
   omitted. Authored references are preserved, and a same-name File does not
   silently inherit the old identity.
3. File-owned source work is cancelled or fenced, pending fan-out remains
   recoverable after the File row is removed, and the finalizer is removed when
   File-owned cleanup and durable handoff are complete.
4. **External source/object payloads are not automatically deleted.** Payload
   retention and explicit purge semantics require a separate, auditable
   contract; consumer references do not grant ownership of shared bytes.

### Reference checking model

`File` references from catalog resources (`spec.media[*].fileRef`) follow the
non-blocking reference contract in spec 056:

| Phase      | Check                                                                                         |
|------------|-----------------------------------------------------------------------------------------------|
| Admission | Structural shape and same-namespace isolation are validated. Missing File does not reject the manifest. |
| Controller | A resolved reference records a system-owned File UID owner reference with `blockOwnerDeletion: false`; reverse lookup and bounded reconciliation refresh dependents. |

Unresolved references remain asynchronous. Required/optional behavior controls
readiness and presentation, not File deletion. References that have never
resolved do not receive a deleted-File condition.

### File location convention

The conventional repository path for a File manifest is:

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
      kind: CredentialsRef
      type: aws-access-key/v1
      secretRef:
        kind: SecretRef
        name: s3-catalog-assets
  processing:
    image:
      variants:
      - name: thumbnail
---

Hero image for MacBook Pro.
```

### GraphQL read and watch surface

| Surface | Current state |
|---|---|
| `file(by)` | Implemented read-only query. File is namespaced and looked up by its id or namespace/name pair. |
| `watchFiles` | Implemented durable, resumable File watch through the shared resource watch journal. |
| `files` connection | Not currently exposed by the File GraphQL schema. |
| File create/update/delete mutations | Not exposed; Git manifest commits and admission are the authoring path. |
| Upload/download/purge mutations | Not implemented; require separate API, authorization, and payload-ownership contracts. |

### Validation and admission rules

| Phase       | Rule                                                                                                                                                                         |
|-------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pre-receive / admission | File envelope and schema validation; author-supplied status and system metadata rejected; immutable content type enforced; typed `CredentialsRef`/`SecretRef` shape and namespace constraints follow ADRs 0001/0009 and spec 063. |
| Admission diagnostics | API error surfaces use ADR-0015/spec 057's standard codes (`ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND`, `CONFLICT`, `FAILED_PRECONDITION`, `BAD_USER_INPUT`, `FORBIDDEN`) and structured diagnostics. Spec 057 partially implements this for Namespace and CategoryTaxonomy. Git receive-pack currently returns its pre-receive rejection as `ng <ref> <reason>`, not that GraphQL envelope. |
| Controller | Initial payload phase copies the source blob to configured object storage and ignores `spec.processing`. Spec 056 defines later source authorization, renewable access, verification, File reference propagation, and deletion safety; these are not implicit in admission. |

### Status and reconciliation behaviour

| Condition            | Meaning                                                                             |
|----------------------|-------------------------------------------------------------------------------------|
| `AdmissionAccepted`  | File manifest has been admitted and stored. |
| `SourceResolved`     | Future controller signal that declared source access and applicable verification succeeded under the currently authorized binding. |
| `ProcessingComplete` | Future signal for a separately specified transformation phase; the initial payload phase ignores `spec.processing`. |
| `Ready`              | Spec 051 currently initializes this to `True` on admission. That baseline is not proof of source access or object-store materialization; spec 056 must make readiness truthful when runtime reconciliation is implemented. |
| `Terminating`        | Reserved for File-owned deletion/cleanup. Spec 051 does not implement this lifecycle; spec 056 defines non-blocking dependent decoupling. |

The manifest-contract implementation currently sets `AdmissionAccepted=True`
and `Ready=True`; `SourceResolved` and `ProcessingComplete` are absent. The
source-materialization controller and renewable source access remain separate
work. Do not present the optimistic admission-time `Ready` value as durable
proof that the source is accessible or its payload has been copied. The first
payload phase ignores processing requests.

### Relationship to MediaAsset

`File` is the technical storage primitive. `MediaAsset` is a separate git-backed
resource that points at a `File` and adds catalog-facing metadata:

```
MediaAsset.spec.fileRef → File
```

`MediaAsset` has its own lifecycle and owns the presentation-layer metadata (`role`,
`altTextRef`, `focalPoint`). It is tracked separately by open GH#316 and is not
implemented by this ADR. If it references a File, it must follow the same
non-blocking, UID-based decoupling semantics as other consumers in spec 056.

## Consequences

Positive:
- File manifests are reviewable and auditable through git history.
- GitStore-managed payloads live in object storage. A repository-blob source is
  still permitted by the File source contract and necessarily remains in Git history.
- Deletion does not wait on catalog references; previously resolved dependents are
  durably decoupled by File UID under spec 056.
- Source materialization and later verification are asynchronous controller work,
  separate from push admission.

Negative:
- The manifest contract exists, but the File source-materialization controller is
  separate work. Its first payload phase reads the source blob and writes it to
  configured object storage; `spec.processing` is ignored.
- Source access, verification, renewable credentials, and reference safety are
  specified by draft spec 056; admission alone is not proof of usable payload.
- External source/object payloads are not automatically deleted with the File record.

## Cross-references

- [ADR-0002](0002-namespace-lifecycle.md) — Namespace must be `Active`.
- [ADR-0003](0003-repository-lifecycle.md) — Repository must be `Active`.
- [ADR-0004](0004-product-lifecycle.md) — Products reference files via
  `spec.media[*].fileRef`; product deletion does not affect the file. File deletion
  follows the non-blocking decoupling contract in spec 056.
- [ADR-0005](0005-product-variant-lifecycle.md) — Variants reference files via
  `spec.media[*].fileRef` (same non-blocking rule).
- [ADR-0006](0006-category-taxonomy-lifecycle.md) — Categories reference files via
  `spec.media[*].fileRef`; reference resolution/deletion semantics follow spec 056.
- [ADR-0007](0007-collection-lifecycle.md) — Collections reference files via
  `spec.media[*].fileRef` (same non-blocking rule).
- [ADR-0001](0001-secretref-reference-contract.md) and [ADR-0009](0009-credential-secret-boundary.md) —
  `spec.source.credentialsRef` is a typed `CredentialsRef` wrapping a `SecretRef`; it is not a grant of runtime authority.
- [ADR-0015](0015-resource-lifecycle-hooks.md) — admission diagnostics, asynchronous
  event subscriptions, and lifecycle gates; payload facts such as malware results
  gate File readiness rather than blocking Git push admission.
- [spec 051](../../specs/051-file-resource-contract/spec.md), [spec 056](../../specs/056-file-reference-safety/spec.md),
  and [spec 063](../../specs/063-implement-secret-adrs/spec.md) — current File
  contract, reference/source-runtime work, and secret-material boundary.

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

### Use the Git tree as the managed payload destination (rather than object storage)

Rejected as the managed payload destination: Git history and clone costs make it
unsuitable for serving/materializing large binary assets. This does not prohibit
`spec.source.type: git`; a repository blob may be an ingress source that the
controller copies to configured object storage. Git LFS and external sources are
also represented by source locators, not confused with the managed destination.

### Merge File and MediaAsset into a single resource

Rejected per `../resource-storage/git-backed.md`: "Do not model `MediaAsset` as a wrapper
around `File`; the two resources have different lifecycles and ownership." `File` owns
the technical storage and checksum contract; `MediaAsset` owns the catalog-facing
semantic layer. Merging them would couple byte-level storage decisions to
presentation-layer edits and create a single large resource with mixed concerns.

### Allow file deletion to cascade to binary payload

Rejected for Phase 1. Silent deletion of object-storage assets is a destructive
out-of-band side effect that requires careful operator confirmation. A `purgeFilePayload`
operation that is explicit and auditable is the safer Phase 2 design.

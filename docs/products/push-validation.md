# Push Validation and Admission Pipeline

This document describes how GitStore validates and stores Git-backed catalog resources when a catalog author pushes to a repository.

## Overview

When a `git push` arrives at the GitStore git service, two hook phases handle the incoming data:

1. **Pre-receive (blocking)**: `gitstore-git-service` extracts resource blobs from the incoming commit and calls `CatalogService.ValidateResources` on `gitstore-api`. Invalid pushes are rejected before any refs are updated.
2. **Post-receive (fire-and-forget)**: `gitstore-git-service` notifies `gitstore-api` via `CatalogService.AdmitResources`. The API compares the old and new ref tips, parses changed resources, and stores catalog records asynchronously. The git author is not blocked.

## Frontmatter Opt-in Model

Only files whose raw bytes begin with a YAML frontmatter delimiter (`---`) are sent for validation. Files without a `---` prefix are silently ignored by the git service. This allows a repository to contain documentation, images, and other non-catalog files alongside product specs.

## Pre-receive Validation

The git service walks the incoming commit tree (from the quarantine area) and collects candidate files. For each file:

- If the file does not start with `---`, it is skipped.
- If the file starts with `---`, it is sent to `CatalogService.ValidateResources` as a `ResourceBlob`.

The validation call is **blocking** and has a configurable timeout (`GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS`, default 10 seconds). If the validation service is unreachable or times out, the push is **rejected** (fail-closed).

All blobs are validated in a single RPC call. If any blob has violations, the push is rejected and all field-level errors are returned to the author via `git push` stderr output. No fail-fast — all blobs are checked before returning.

### Example rejection output

```
remote: error: push rejected by pre-receive: validate: spec.title exceeds maximum length of 200 characters
```

## Post-receive Admission

After refs are committed, the git service calls `CatalogService.AdmitResources` with `repository_id`, `ref_name`, the old commit SHA, and the new commit SHA. `commit_sha` is still sent as a compatibility alias for the new commit. The call is fire-and-forget — the git client receives the push acknowledgment immediately without waiting for storage to complete.

The API then:
1. Confirms the ref still points at the admitted new commit. If a newer push has already advanced the ref, the stale admission is skipped.
2. Compares resource files at the old and new commits.
3. Derives operations by resource identity: create, update, delete, or path move.
4. Calls `GetFile` for changed resource files, parses frontmatter, dispatches on `kind`, and stores, updates, or deletes the catalog record.
5. Sets `AdmissionAccepted: True` status on stored or updated resources.

Supported `kind` values: `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`.

Failures for individual resources are logged with operation, namespace, name, and conflict details where available. They do not block remaining resources and cannot reject a Git push that has already been accepted.

If `old_commit_sha` is absent because an older git service called the API, admission falls back to the legacy full-tree snapshot path and logs a warning.

## Resource Identity and Lifecycle

Catalog resource identity is independent of file path. GitStore identifies a stored resource by:

| Field | Source |
|---|---|
| `apiVersion` | Frontmatter |
| `kind` | Frontmatter |
| `namespace` | `metadata.namespace` or the repository's owning namespace |
| `metadata.name` | Frontmatter |

The file path is stored as source provenance, not identity. Moving `products/widget.md` to `catalog/products/widget.md` with the same identity keeps the same `metadata.uid`.

Lifecycle rules:

| Git change | Stored result |
|---|---|
| New identity appears | Create resource with a new `metadata.uid`, `generation=1`, `resourceVersion=1` |
| Existing identity changes `spec` or Markdown body | Preserve `metadata.uid`, increment `generation`, increment `resourceVersion` |
| Existing identity moves to a new path with the same spec/body | Preserve `metadata.uid` and `generation`, increment `resourceVersion` |
| Existing identity changes only labels/annotations | Preserve `metadata.uid` and `generation`, increment `resourceVersion` |
| Identity disappears from the admitted ref | Delete the stored resource and remove lookup indexes |
| Identity is deleted in one commit and added again later | Allocate a new `metadata.uid`, reset `generation=1` and `resourceVersion=1` |
| `metadata.name` or `kind` changes in a file | Delete the old identity and create the new identity |

Duplicate SKU conflicts for `ProductVariant` are not inserted after detection. The existing variant remains unchanged, the conflicting incoming variant is skipped, and the conflict is visible in structured API logs. Structural validation errors still reject the push during pre-receive.

### Branch filtering

Only pushes to refs matching `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` (default: `refs/heads/main`) trigger catalog storage. Pushes to feature branches pass validation but are not stored.

### Branch deletion

When a branch matching `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` is deleted via `git push origin --delete <branch>`, the git service forwards the deletion as an `AdmitResources` call with `new_commit_sha` set to the zero OID (`0000000000000000000000000000000000000000`). The API interprets this as a branch-delete and removes all catalog resources that were admitted on the deleted ref.

Branch-delete admission uses the same pattern filter as push admission — deleting a branch that does not match the pattern produces no admission activity. The call is fire-and-forget (non-blocking), consistent with all other post-receive admission calls.

The API's staleness guard also applies to branch deletions: if the branch was recreated before the admission call is processed, the delete is skipped and the re-created resources are preserved.

## Revision format

The `revision` field on a stored catalog resource encodes both the branch context and the exact commit:

```
main@sha1:a1b2c3d4e5f6...
```

This provides a human-readable audit trail: branch name + commit SHA without requiring an extra lookup.

## Configuration reference

| Environment variable | Default | Description |
|---|---|---|
| `GITSTORE_SCHEMA_VALIDATION__PHASE` | `pre-receive` | Hook phase for validation callout |
| `GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS` | `10` | Validation gRPC timeout in seconds |
| `GITSTORE_ADMISSION_CONTROL__PHASE` | `post-receive` | Hook phase for admission callout |
| `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` | `refs/heads/main` | Refs that trigger catalog storage |
| `GITSTORE_CATALOG_SERVICE__URI` | `http://localhost:6000` | gitstore-api gRPC endpoint |
| `GITSTORE_API__GRPC_PORT` | `6000` | Port where gitstore-api listens for gRPC (CatalogService) |

> **Phase conflict**: `GITSTORE_SCHEMA_VALIDATION__PHASE` and `GITSTORE_ADMISSION_CONTROL__PHASE` must not be equal. The service refuses to start if they are the same (FR-019).

## Metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `gitstore_schema_validation_total` | Counter | `result={accepted,rejected,timeout,service_unavailable}` | Pre-receive validation callout outcomes |

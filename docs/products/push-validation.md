# Push Validation and Admission Pipeline

This document describes how GitStore validates and stores product resources when a catalog author pushes to a repository.

## Overview

When a `git push` arrives at the GitStore git service, two hook phases handle the incoming data:

1. **Pre-receive (blocking)**: `gitstore-git-service` extracts resource blobs from the incoming commit and calls `CatalogService.ValidateResources` on `gitstore-api`. Invalid pushes are rejected before any refs are updated.
2. **Post-receive (fire-and-forget)**: `gitstore-git-service` notifies `gitstore-api` via `CatalogService.AdmitResources`. The API fetches the accepted files, parses them, and stores catalog records asynchronously. The git author is not blocked.

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

After refs are committed, the git service calls `CatalogService.AdmitResources` with `repository_id`, `commit_sha`, and `ref_name`. The call is fire-and-forget — the git client receives the push acknowledgment immediately without waiting for storage to complete.

The API then:
1. Calls `ListFiles` on the git service to enumerate all files at the commit SHA.
2. For each file with a `---` prefix: calls `GetFile`, parses it with `validate.Parse`, and stores or updates the catalog record.
3. Sets the `AdmissionAccepted: True` status condition on the stored product.

Failures for individual products are logged at ERROR but do not block remaining products.

### Branch filtering

Only pushes to refs matching `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` (default: `refs/heads/main`) trigger catalog storage. Pushes to feature branches pass validation but are not stored.

## Revision format

The `revision` field on a stored product encodes both the branch context and the exact commit:

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

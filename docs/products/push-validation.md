# Push Validation and Admission Pipeline

This document describes how GitStore validates and stores Git-backed catalog resources when a catalog author pushes to a repository.

## Overview

When a `git push` arrives at the GitStore git service, two hook phases handle the incoming data:

1. **Pre-receive (blocking)**: `gitstore-git-service` extracts resource blobs from the incoming commit and calls `CatalogService.ValidateResources` on `gitstore-api`. Invalid pushes are rejected before any refs are updated.
2. **Post-receive (fire-and-forget)**: `gitstore-git-service` notifies `gitstore-api` via `CatalogService.AdmitResources`. The API compares the old and new ref tips, parses changed resources, and stores catalog records asynchronously. The git author is not blocked.

## Frontmatter Opt-in Model

Only files whose raw bytes begin with a YAML frontmatter delimiter (`---`) are sent for validation. Files without a `---` prefix are silently ignored by the git service. This allows a repository to contain documentation, images, and other non-catalog files alongside product specs.

## Pre-receive Validation

The git service walks the changed portions of the old and proposed commit trees
(from the quarantine area) and collects candidate files. For each file:

- If the file does not start with `---`, it is skipped.
- If the file starts with `---`, it is sent to `CatalogService.ValidateResources` as a `ResourceBlob`.

For a changed resource, the call also carries its old and proposed Git blobs.
This enables stateless immutable-field checks before the ref advances: File
`spec.contentType`, ProductVariant `spec.sku` and `spec.productRef`, and
Namespace tier demotions are rejected without a datastore read. `metadata.name`
and `metadata.namespace` remain resource identity, so changing either is a
delete/create operation rather than an in-place update. Product,
CategoryTaxonomy, and Collection have no additional immutable authored fields;
Repository rename is intentionally supported by its API contract.

The validation call is **blocking** and has a configurable timeout (`git_service.validation.timeout`, env `GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT`, default `10s`). If the validation service is unreachable or times out, the push is **rejected** (fail-closed).

All blobs are validated in a single RPC call. If any blob has violations, the push is rejected and all field-level errors are returned to the author via `git push` stderr output. No fail-fast — all blobs are checked before returning.

### Example rejection output

```
remote: error: push rejected by pre-receive: validate: spec.title exceeds maximum length of 200 characters
```

## Post-receive Admission

After refs are committed, the git service calls `CatalogService.AdmitResources` with `repository_id`, `ref_name`, the old commit SHA, and the new commit SHA. The call is fire-and-forget — the git client receives the push acknowledgment immediately without waiting for storage to complete.

The API then:
1. Checks the current ref. If it has advanced, Namespace and Product upserts may converge only when their accepted identity and content still match and Git history proves the accepted source remains current. Product removals may converge through the reserved-deletion protocol below. Convergence retains the original callback actor and accepted revision; it does not admit a later writer's changed content under the earlier actor. Other kinds retain their stale-callback skip behavior.
2. Compares resource files at the old and new commits.
3. Derives operations by resource identity: create, update, delete, or path move.
4. Calls `GetFile` for changed resource files, parses frontmatter, dispatches on `kind`, and stores, updates, or deletes the catalog record.
5. Sets `AdmissionAccepted: True` status on stored or updated resources.

Supported `kind` values: `Repository`, `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `File`, and `Namespace`.

Failures are logged with operation, namespace, name, and conflict details where available. A failed Product is reported at the callback boundary while other Products in that batch are still attempted. Authorization is checked again before retrying a Product write after a controller status conflict. Failed upserts can prevent the later deletion phase; no post-receive failure can reject a Git push that has already been accepted.

New Products are first stored provisionally with a durable UID and
`AdmissionAccepted=False`, reason `SourceVerificationPending`. Their
`lastAppliedRevision` is unset. After the accepted Git source is verified again,
a conditional write sets `AdmissionAccepted=True` and records the accepted
revision. The controller cannot mark a provisional Product `Ready=True`.
This creates an `ADDED` watch event followed by a `MODIFIED` event, and consumes
an additional resource version without changing generation. In the absence of
concurrent status writes, a newly accepted Product therefore has
`generation=1` and `resourceVersion=2`.

A matching retry by the original creator can finish the provisional create
through another API replica using create permission; it does not require
update permission. Current authorization is checked again before acceptance.
A concurrent deletion, ownership change, or replacement UID cannot be
overwritten by that acceptance. If the source is found absent after reserving
the UID, the still-matching provisional Product enters normal foreground
deletion. Uncertain Git reads or failed cleanup return an error and leave the
Product unaccepted rather than claiming success.

Post-receive callbacks have no persistent retry queue. A native-service restart
or failed callback can leave accepted Git changes without completed admission.
Missing or unreadable diff objects are errors, not requests for a full scan;
an unchanged tree produces no call.

Git source checks and datastore conditional writes are not one atomic
transaction, and publication and admission can still race with direct writers.
A successful Git acknowledgment, or a successful callback response, is not
proof that every catalog record exists or is ready. Before counting
bulk-authored Products as complete, read them through the API and verify their
identity, content, accepted revision, and current-generation
`AdmissionAccepted=True` and `Ready=True` conditions on each API replica.

For a Product removal, admission first proves the stored source remained
unchanged through the original pre-removal commit. It then conditionally
reserves that Product UID before proving the path has remained absent from
the removal commit through the current ref. This applies even when that
removal is still the current tip, not only after an unrelated publication.
The reservation binds the source, ref, removal commits, original actor and a
unique acquisition token. An identical newer upsert cannot bypass a held
reservation as a successful no-op. A released and reacquired reservation
cannot reuse an older proof.

Successful verification rechecks current delete authorization and marks the
matching Product terminating while retaining its reservation. Normal
foreground finalization and blocking ProductVariant dependencies still apply.
Controller status writes preserve the reservation. Failed verification
releases only the same acquisition; uncertain writes or cancellation may
leave it held for an explicit retry through another API replica. These
transitions consume additional resource versions, not generations.

The proof is bounded and fails closed on recreated paths, directories,
unrelated history, merges, unavailable history or exhausted history limits.
A file that remains present with a different identity is not path absence.
Branch deletion uses the same reservation but requires an explicit native
proof that the named ref is absent in an accessible repository. A missing
repository, corrupt ref or ordinary `NotFound` response is not that proof.
Upgrade the Git service before the API uses absence proofs; unsupported peers
are errors, not evidence of removal.

Provisional creation and deletion reservations do not schedule callback retries
or repair older lost work.
Other kinds' superseded deletions still retain their current-ref guard. Do not
assume every pending or obsolete record will automatically converge.

If `old_commit_sha` is absent because an older git service called the API, admission falls back to the legacy full-tree snapshot path and logs a warning.

## Resource Identity and Lifecycle

Catalog resource identity is independent of file path. GitStore identifies a stored resource by:

| Field           | Source                                                    |
|-----------------|-----------------------------------------------------------|
| `apiVersion`    | Frontmatter                                               |
| `kind`          | Frontmatter                                               |
| `namespace`     | `metadata.namespace` or the repository's owning namespace |
| `metadata.name` | Frontmatter                                               |

The file path is stored as source provenance, not identity. Moving `products/widget.md` to `catalog/products/widget.md` with the same identity keeps the same `metadata.uid`.

Lifecycle rules:

| Git change                                                    | Stored result                                                                  |
|---------------------------------------------------------------|--------------------------------------------------------------------------------|
| New identity appears                                          | Allocate a new `metadata.uid`, `generation=1`, initial `resourceVersion=1`; Products advance to version 2 on acceptance |
| Existing identity changes `spec` or Markdown body             | Preserve `metadata.uid`, increment `generation`, increment `resourceVersion`   |
| Existing identity moves to a new path with the same spec/body | Preserve `metadata.uid` and `generation`, increment `resourceVersion`          |
| Existing identity changes only labels/annotations             | Preserve `metadata.uid` and `generation`, increment `resourceVersion`          |
| Identity disappears from the admitted ref                     | Remove the identity; Products first enter guarded foreground termination, and finalization removes their lookup indexes |
| Identity is deleted in one commit and added again later       | Allocate a new `metadata.uid`, reset `generation=1` and initial `resourceVersion=1`; Products advance again on acceptance |
| `metadata.name` or `kind` changes in a file                   | Delete the old identity and create the new identity                            |

Duplicate SKU conflicts for `ProductVariant` are not inserted after detection. The existing variant remains unchanged, the conflicting incoming variant is skipped, and the conflict is visible in structured API logs. Structural validation errors still reject the push during pre-receive.

### Branch filtering

Only pushes to refs matching `git_service.admission.branch_pattern` (env `GITSTORE_GIT_SERVICE__ADMISSION__BRANCH_PATTERN`, default: `^refs/heads/main$`) trigger catalog storage. Pushes to feature branches pass validation but are not stored.

### Branch deletion

When a branch matching `git_service.admission.branch_pattern` is deleted via `git push origin --delete <branch>`, the git service forwards the deletion as an `AdmitResources` call with `new_commit_sha` set to the zero OID (`0000000000000000000000000000000000000000`). The API interprets this as a branch-delete and removes all catalog resources that were admitted on the deleted ref.

Branch-delete admission uses the same pattern filter as push admission — deleting a branch that does not match the pattern produces no admission activity. The call is fire-and-forget (non-blocking), consistent with all other post-receive admission calls.

The API's staleness guard also applies to branch deletions: if the branch was recreated before the admission call is processed, the delete is skipped and the re-created resources are preserved.

## Revision format

The `revision` field on a stored catalog resource encodes both the branch context and the exact commit:

```
main@sha1:a1b2c3d4e5f6...
```

This provides a human-readable audit trail: branch name + commit SHA without requiring an extra lookup.

## Push size limits

Every push is bounded by a shared platform ceiling, configured as
`push_limits.max_pack_size` (default `512MiB`) and `push_limits.max_file_size`
(default `100MiB`) on both `gitstore-api` and `gitstore-git-service`. This
ceiling is enforced at two points:

1. **Namespace admission (`gitstore-api`)**: a Namespace's
   `spec.pushPolicyDefaults.maxPackSizeBytes`/`maxFileSizeBytes` is rejected at
   create/update time if it exceeds the configured ceiling. A Repository
   created under that namespace inherits the namespace's defaults, or the
   ceiling itself when the namespace sets no default.
2. **Receive-pack clamp (`gitstore-git-service`)**: for every push, the git
   service resolves the effective limit as
   `effective = min(repository policy, ceiling)`, treating a `0`/absent
   repository policy as the ceiling itself. There is no "0 = unlimited"
   sentinel — every repository always has a positive, enforceable limit. A
   pack or blob that exceeds the effective limit is rejected with
   `RESOURCE_EXHAUSTED` before the push is accepted.

Because namespace defaults are already validated against the ceiling at
admission time, the git-service clamp is a defense-in-depth bound: it only
takes effect if the ceiling itself is lowered after a repository's policy was
set, or for repositories created before the ceiling existed.

See [`push_limits`](../configuration.md#shared-push-size-ceiling) in the
configuration reference for the full key/env-var table.

## Configuration reference

| Key                                     | Env Var                                           | Default                 | Description                                               |
|--------------------------------------------|------------------------------------------------------|--------------------------|-------------------------------------------------------------|
| `git_service.validation.timeout`        | `GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT`       | `10s`                    | Validation gRPC timeout (duration string, `ms\|s\|m\|h`)   |
| `git_service.admission.branch_pattern`  | `GITSTORE_GIT_SERVICE__ADMISSION__BRANCH_PATTERN` | `^refs/heads/main$`      | Refs that trigger catalog storage (regex)                 |
| `git_service.catalog.uri`               | `GITSTORE_GIT_SERVICE__CATALOG__URI`              | `dns:///localhost:6000`  | gitstore-api gRPC endpoint                                |
| `push_limits.max_pack_size`              | `GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE`             | `512MiB`                 | Platform ceiling for a push's total pack size (IEC size string) |
| `push_limits.max_file_size`              | `GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE`             | `100MiB`                 | Platform ceiling for a single file/blob in a push (IEC size string) |
| `api.grpc_port`                         | `GITSTORE_API__GRPC_PORT`                         | `6000`                   | Port where gitstore-api listens for gRPC (CatalogService) |

Pre-receive schema validation and post-receive admission control run
unconditionally on every push — both phases are always on, with no toggle and
no way to reorder or disable them. See the full
[gitstore-git-service configuration reference](../configuration.md#gitstore-git-service)
for every key above plus the shared `grpc_auth` and `log` settings.

## Metrics

| Metric                             | Type    | Labels                                                   | Description                             |
|------------------------------------|---------|----------------------------------------------------------|-----------------------------------------|
| `gitstore_schema_validation_total` | Counter | `result={accepted,rejected,timeout,service_unavailable}` | Pre-receive validation callout outcomes |

# Contract: Mutation error envelope and admission diagnostics

This contract is kind-neutral and shared by every Git-backed mutation. CategoryTaxonomy and Namespace adopt it in this feature. Product and Repository adopt it in their follow-ups.

## Envelope: four keys, fixed presence rules

| Key           | Present when                                                                              | Value                                                                                                                                                       |
|---------------|-------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `code`        | always                                                                                    | `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `NOT_FOUND`, `CONFLICT`, `FAILED_PRECONDITION`, `BAD_USER_INPUT`, `FORBIDDEN`                                       |
| `diagnostics` | the error has detail (always for `ADMISSION_REJECTED`, `FAILED_PRECONDITION`, `CONFLICT`) | ≥ 1 entry, see below                                                                                                                                        |
| `phase`       | only `code = ADMISSION_REJECTED`                                                          | `PRE_RECEIVE` (no commit, no record change) or `POST_RECEIVE` (ref moved; last accepted generation kept, `AdmissionAccepted=False`/`AdmissionReportFailed`) |
| `commit`      | only `phase = POST_RECEIVE`                                                               | commit SHA                                                                                                                                                  |

No other keys are allowed, with no exceptions. `RESOURCE_VERSION_CONFLICT` (status writes, deletion completion) and its `resourceVersion` key fold into `CONFLICT` with `diagnostics[{reason: RESOURCE_VERSION_CONFLICT}]`, and the current version moves into `message`. Controllers already treat any conflict as "re-read and retry"; they must accept `CONFLICT` before the API switches.

**Diagnostic entry**: `{ reason, message, level, file?, field? }`
- `reason`: required machine-readable SCREAMING_SNAKE string. Clients branch on it.
- `message`: human-readable text.
- `level`: `FAILURE | WARNING | NOTICE`. Errors contain at least one `FAILURE`.
- `file`, `field`: present for manifest-level problems.
- Reserved for later: `title`, `startLine`, `startColumn`, `endLine`, `endColumn`. Clients must ignore unknown keys.

**Code selection by cause**:

| Cause                                                                                                   | `code`                                                                          |
|---------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------|
| Schema, structural, immutable-field or policy check failed                                              | `ADMISSION_REJECTED`                                                            |
| Name already taken                                                                                      | `ALREADY_EXISTS`                                                                |
| Target or referenced namespace missing                                                                  | `NOT_FOUND`                                                                     |
| Superseded by a concurrent commit or resource-version change                                            | `CONFLICT`                                                                      |
| Lifecycle/state precondition (terminating, blocking dependents, missing provenance, bootstrap resource) | `FAILED_PRECONDITION`                                                           |
| Malformed arguments (cursor mode, `maxDepth` range)                                                     | `BAD_USER_INPUT`                                                                |
| Authorization denied                                                                                    | `FORBIDDEN` (`diagnostics` omitted, so nothing about the resource is disclosed) |

## Examples

Pre-receive rejection:

```json
{
  "message": "categories/laptops.md: spec.title is required; categories/laptops.md: spec.parentRef.name must not equal metadata.name",
  "path": ["createCategory"],
  "extensions": {
    "code": "ADMISSION_REJECTED",
    "phase": "PRE_RECEIVE",
    "diagnostics": [
      { "reason": "REQUIRED_FIELD", "message": "spec.title is required", "level": "FAILURE",
        "file": "categories/laptops.md", "field": "spec.title" },
      { "reason": "SELF_PARENT", "message": "spec.parentRef.name must not equal metadata.name", "level": "FAILURE",
        "file": "categories/laptops.md", "field": "spec.parentRef.name" }
    ]
  }
}
```

Post-receive rejection: same shape, with `"phase": "POST_RECEIVE"` and `"commit": "9f2c…"`.

Precondition:

```json
{ "message": "category electronics has child categories",
  "extensions": { "code": "FAILED_PRECONDITION",
    "diagnostics": [{ "reason": "CHILD_CATEGORIES_PRESENT", "message": "category electronics has child categories", "level": "FAILURE" }] } }
```

For `ADMISSION_REJECTED`, `message` is the `file: message` entries joined by `"; "` (just `message` when there is no file). It is byte-identical to the `ng <ref> <reason>` text a push carries for the same manifest, asserted against one golden fixture from both Go and Rust tests. For other codes, `message` is free text.

## CategoryTaxonomy reasons

| `reason`                                                                                                                                                                                                          | `code`                                                                             |
|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------|
| `INVALID_ENVELOPE`, `REQUIRED_FIELD`, `INVALID_FIELD`, `SELF_PARENT`, `PARENT_TERMINATING`, `CROSS_NAMESPACE_REFERENCE`, `IMMUTABLE_NAME`, `IMMUTABLE_NAMESPACE`, `POLICY_DENIED`, `VALIDATION_FAILED` (fallback) | `ADMISSION_REJECTED`. These are manifest checks; a push rejects the same manifest. |
| `CATEGORY_TERMINATING`, `CHILD_CATEGORIES_PRESENT`, `PROVENANCE_UNAVAILABLE`                                                                                                                                      | `FAILED_PRECONDITION`. These are about the target's state, not the manifest.       |
| `CATEGORY_ALREADY_EXISTS`                                                                                                                                                                                         | `ALREADY_EXISTS`                                                                   |
| `CATEGORY_NOT_FOUND`                                                                                                                                                                                              | `NOT_FOUND`                                                                        |
| `SUPERSEDED`                                                                                                                                                                                                      | `CONFLICT`                                                                         |

Admission reasons are derived from `catalogv1.ValidationError.Constraint`. Unmapped constraints fall back to `VALIDATION_FAILED`.

## Namespace and status-write migration

| Today `code`                                                                   | Today extensions              | New `code`                                                                                                                                                                                                    | New `diagnostics[].reason`                                                                                                                           |
|--------------------------------------------------------------------------------|-------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| `NAMESPACE_STRUCTURAL_VALIDATION_FAILED`                                       | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED` (+ `phase`)                                                                                                                                                                              | unchanged reason (`INVALID_ENVELOPE`, `INVALID_IDENTIFIER`, `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`, `DUPLICATE_IDENTITY`) |
| `NAMESPACE_IMMUTABLE_FIELD`                                                    | `phase: STRUCTURAL`, `reason` | `ADMISSION_REJECTED` (+ `phase`)                                                                                                                                                                              | `IMMUTABLE_NAME`                                                                                                                                     |
| `NAMESPACE_POLICY_REJECTED`                                                    | `phase: POLICY`, `reason`     | by reason: `TIER_DEMOTION` → `ADMISSION_REJECTED`; `BOOTSTRAP_NAMESPACE`, `NAMESPACE_TERMINATING` → `FAILED_PRECONDITION`; `NAMESPACE_ALREADY_EXISTS` → `ALREADY_EXISTS`; `NAMESPACE_NOT_FOUND` → `NOT_FOUND` | unchanged reason                                                                                                                                     |
| `NAMESPACE_CONFLICT`                                                           | `phase: POLICY`, `reason`     | `CONFLICT`                                                                                                                                                                                                    | `RESOURCE_VERSION_CONFLICT`, or `SUPERSEDED`                                                                                                         |
| `NOT_FOUND` (`NewNamespaceNotFoundError`)                                      | `phase: POLICY`, `reason`     | `NOT_FOUND`                                                                                                                                                                                                   | `NAMESPACE_NOT_FOUND`                                                                                                                                |
| `NAMESPACE_DELETION_BLOCKED`                                                   | `reasons[]`                   | `FAILED_PRECONDITION`                                                                                                                                                                                         | one entry per blocker, in the existing order: `BOOTSTRAP_NAMESPACE`, then `NAMESPACE_NOT_EMPTY`                                                      |
| `RESOURCE_VERSION_CONFLICT` (all kinds' status writes and `complete*Deletion`) | `resourceVersion`             | `CONFLICT`                                                                                                                                                                                                    | `RESOURCE_VERSION_CONFLICT`                                                                                                                          |
| `NAMESPACE_REPOSITORY_FENCE_DISABLED`                                          | `reason`, `operation`         | removed in #457 (the fence is always on)                                                                                                                                                       | n/a                                                                                                                                                  |

`namespace.Phase` (`decision.go:11-15`), the `Code*` constants, and the `phase` label on `gitstore_namespace_validation_*` metrics are replaced. The metric label becomes `code`, keeping the `reason` label. `docs/runbooks/namespace-admission.md` "Stable response codes" is rewritten.

## Warnings on success (top-level `extensions.admission`)

```json
{
  "data": { "a": { "category": { "id": "…" } }, "b": { "category": { "id": "…" } } },
  "extensions": {
    "admission": [
      { "path": ["b"], "commit": "9f2c…",
        "diagnostics": [{ "reason": "MEDIA_UNRESOLVED", "message": "…", "level": "WARNING",
                          "file": "categories/tv.md", "field": "spec.media" }] }
    ]
  }
}
```

- `path` is the response path of the mutation field, so it reflects aliases.
- The key is omitted when there is nothing to report.
- It is merged with other top-level keys and never overwrites them.
- No diagnostics appear in `data`.

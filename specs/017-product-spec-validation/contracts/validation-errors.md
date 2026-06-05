# Contract: Validation Error Messages

**Feature**: `017-product-spec-validation` — #186  
**Date**: 2026-06-05  
**Stability**: `catalog.gitstore.dev/v1beta1`

This contract defines the exact error message patterns the push pipeline returns when a product file is rejected. These messages appear in the git-receive-pack response and are the surface tested by FR-001 through FR-009.

---

## Error Format

All validation errors are returned as a single string. When multiple violations exist they are joined with `\n` (via `errors.Join`). Each individual violation message follows the pattern:

```
validate: <field-path> <violation-description>
```

Where `<field-path>` uses dot notation, lowercased, with bracket-indexing for slice fields:
- `spec.title`
- `spec.media[0].fileRef.name`
- `spec.options[2].name`
- `metadata.uid`

---

## Error Catalogue

### Pre-Parse Errors (YAML map phase)

| Trigger | Error Message |
|---------|--------------|
| `apiVersion` absent | `validate: document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha` |
| `spec` key absent | `validate: spec is required` |
| `status` key present (any value) | `validate: status is system-managed and must not be set by authors` |
| `metadata.uid` present | `validate: metadata.uid is read-only and must not be set by authors` |
| `metadata.resourceVersion` present | `validate: metadata.resourceVersion is read-only and must not be set by authors` |
| `metadata.generation` present | `validate: metadata.generation is read-only and must not be set by authors` |
| `metadata.creationTimestamp` present | `validate: metadata.creationTimestamp is read-only and must not be set by authors` |
| `metadata.revision` present | `validate: metadata.revision is read-only and must not be set by authors` |
| `metadata.ownerReferences` present | `validate: metadata.ownerReferences is read-only and must not be set by authors` |

**Multiple forbidden metadata fields**: All are reported in the same response (FR-009). Messages are joined with `\n`.

---

### Struct-Tag Validation Errors (post-parse)

| Trigger | Error Message |
|---------|--------------|
| `apiVersion` wrong value | `validate: apiversion must be "catalog.gitstore.dev/v1beta1", got "<value>"` |
| `kind` wrong value | `validate: kind must be "Product", got "<value>"` |
| `metadata.name` absent or empty | `validate: name is required` |
| `spec.title` > 200 characters | `validate: title failed max` *(see note)* |
| `spec.media[N].fileRef.name` absent/empty | `validate: spec.media[N].fileRef.name is required` |
| `spec.media[N].fileRef.kind` absent/empty | `validate: spec.media[N].fileRef.kind is required` |
| `spec.categoryRef.name` absent (when ref present) | `validate: name is required` |

> **Note on `title` max**: The current `toFriendlyError` uses `fe.Field()` (leaf name). The updated implementation must use `fe.StructNamespace()` to produce qualified paths for nested fields. For top-level spec fields, `spec.title` is the expected output.

---

### Spec-Level Errors (custom `validateSpec`)

| Trigger | Error Message |
|---------|--------------|
| `spec.options[N].name` absent | `validate: options[N].name is required` (N is the zero-based index) |
| Duplicate option name `"color"` | `validate: spec.options contains duplicate name "color"` |

---

### Label Validation Errors

| Trigger | Error Message |
|---------|--------------|
| Label key (no prefix) > 63 chars | `validate: label key "<key>" exceeds 63-character maximum` |
| Label key prefix > 253 chars | `validate: label key prefix "<prefix>" exceeds 253-character maximum` |
| Label value > 63 chars | `validate: label value for key "<key>" exceeds 63-character maximum` |

---

## Multi-Error Behaviour (FR-009)

When a product file contains both a struct-tag violation and a spec-level violation, both are reported:

```
validate: kind must be "Product", got "Widget"
validate: spec.options contains duplicate name "color"
```

The order is deterministic: pre-parse errors first, then struct-tag errors, then spec-level errors, then label errors.

# Data Model: Product Parser and Kind Validation Enforcement

**Branch**: `015-product-parser` | **Date**: 2026-06-04

## Overview

This feature introduces no new persistent entities. All types are in-memory representations used within the `validate.Parse` call boundary. The structs below are defined in `internal/catalog/product.go` (delivered by spec#014) and are reproduced here for completeness; **no struct changes are needed for this feature**.

---

## Entities (existing — no changes)

### `ProductResource`

Top-level envelope for a parsed product file. Only author-writable fields.

| Field        | Type          | Constraints                                                            |
|--------------|---------------|------------------------------------------------------------------------|
| `APIVersion` | `string`      | required; must equal `catalog.gitstore.dev/v1beta1`                    |
| `Kind`       | `string`      | required; must equal `Product` (case-sensitive)                        |
| `Metadata`   | `ObjectMeta`  | required                                                               |
| `Spec`       | `ProductSpec` | required (enforced by pre-parse check — see Decision 5 in research.md) |

### `ObjectMeta`

Author-supplied metadata. Read-only fields (`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`) are rejected in pre-parse and never appear here.

| Field          | Type                | Constraints                                                              |
|----------------|---------------------|--------------------------------------------------------------------------|
| `Name`         | `string`            | required; non-empty                                                      |
| `Namespace`    | `string`            | optional; empty = resolved from push context by caller                   |
| `GenerateName` | `string`            | optional                                                                 |
| `Labels`       | `map[string]string` | optional; key name segment ≤63 chars, prefix ≤253 chars, value ≤63 chars |
| `Annotations`  | `map[string]string` | optional; no length constraint in this version                           |

### `ProductSpec`

Author-controlled product description.

| Field         | Type                        | Constraints                                                      |
|---------------|-----------------------------|------------------------------------------------------------------|
| `Title`       | `string`                    | optional; max 200 chars                                          |
| `CategoryRef` | `*ObjectReference`          | optional; `Name` required if present                             |
| `Tags`        | `[]string`                  | optional                                                         |
| `Media`       | `[]MediaDefinition`         | optional; each entry requires `FileRef`                          |
| `Options`     | `[]ProductOptionDefinition` | optional; no duplicate `Name` values; each entry requires `Name` |

### `ProductOptionDefinition`

| Field    | Type       | Constraints                                                                     |
|----------|------------|---------------------------------------------------------------------------------|
| `Name`   | `string`   | required (validated by `validateSpec`, not struct tag — index appears in error) |
| `Title`  | `string`   | optional                                                                        |
| `Values` | `[]string` | optional                                                                        |

---

## Parse Result States

The `validate.Parse` function returns one of three states:

| State       | Return values                   | Meaning                                                                  |
|-------------|---------------------------------|--------------------------------------------------------------------------|
| **Skip**    | `(nil, rawBytes, nil)`          | File has no `---` frontmatter; not a product file; caller ignores        |
| **Valid**   | `(*ProductResource, body, nil)` | Fully parsed and validated product                                       |
| **Invalid** | `(nil, nil, err)`               | Validation failed; `err` contains all collected field-path errors joined |

---

## Validation Error Format

Errors use dotted field paths matching the YAML key hierarchy:

```
validate: <field-path> <constraint-description>
```

Examples:
- `validate: kind must be "Product", got "Category"`
- `validate: metadata.name is required`
- `validate: spec is required`
- `validate: status is system-managed and must not be set by authors`
- `validate: metadata.uid is read-only and must not be set by authors`
- `validate: options[1].name is required`
- `validate: spec.options contains duplicate name "color"`
- `validate: label key prefix "example.com/..." exceeds 253-character maximum`

When multiple errors exist, they are joined with `\n` via `errors.Join`.

---

## Forbidden Fields

The following fields are rejected in the pre-parse phase (before struct binding). Their presence in an author-supplied file is always an error regardless of other content.

**Top-level forbidden keys**: `status`

**Forbidden `metadata` sub-keys** (read-only, system-managed):
`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

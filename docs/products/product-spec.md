# Product Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `Product`

A Product resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. The frontmatter declares the product's identity and specification; the body is free-form Markdown content.

---

## Envelope Fields

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `apiVersion` | string | yes | Must be `catalog.gitstore.dev/v1beta1` |
| `kind` | string | yes | Must be `Product` (case-sensitive) |
| `metadata` | object | yes | See Metadata Fields |
| `spec` | object | yes | May be empty (`spec: {}`); see Spec Fields |
| `status` | — | **forbidden** | System-managed; presence causes rejection |

---

## Metadata Fields

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `metadata.name` | string | yes | DNS subdomain format |
| `metadata.namespace` | string | no | |
| `metadata.labels` | map[string]string | no | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars |
| `metadata.annotations` | map[string]string | no | |

**Forbidden metadata fields** (read-only, system-assigned):  
`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

---

## Spec Fields

All spec fields are individually optional. Constraints apply when the field is present.

| Field | Type | Constraint |
|-------|------|-----------|
| `spec.title` | string | Max 200 characters |
| `spec.categoryRef` | object | If present, `categoryRef.name` is required |
| `spec.tags` | []string | No per-tag length constraint |
| `spec.media` | []MediaDefinition | Each entry: `fileRef.name` and `fileRef.kind` required |
| `spec.options` | []ProductOptionDefinition | Each entry: `name` required and unique within the list |

### MediaDefinition

| Field | Type | Required |
|-------|------|----------|
| `fileRef.name` | string | yes — even when `fileRef.optional: true` |
| `fileRef.kind` | string | yes |
| `fileRef.optional` | bool | no |

### ProductOptionDefinition

| Field | Type | Required |
|-------|------|----------|
| `name` | string | yes — must be unique within `spec.options` |
| `title` | string | no |
| `values` | []string | no |

---

## Examples

| File | Outcome |
|------|---------|
| [examples/valid-product.md](examples/valid-product.md) | Accepted — complete valid product |
| [examples/invalid-status.md](examples/invalid-status.md) | Rejected — `status` key is system-managed |
| [examples/invalid-title.md](examples/invalid-title.md) | Rejected — `spec.title` exceeds 200 characters |
| [examples/invalid-media.md](examples/invalid-media.md) | Rejected — `spec.media[0].fileRef.name` is required |

---

## Validation Error Format

Errors follow the pattern `validate: <field-path> <violation>`. Multiple violations are reported together in a single response separated by newlines. See [contracts/validation-errors.md](../../specs/017-product-spec-validation/contracts/validation-errors.md) for the full error catalogue.

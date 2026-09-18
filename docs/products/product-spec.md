# Product Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `Product`

A Product resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. The frontmatter declares the product's identity and specification; the body is free-form Markdown content.

---

## Envelope Fields

| Field        | Type   | Required      | Constraint                                 |
|--------------|--------|---------------|--------------------------------------------|
| `apiVersion` | string | yes           | Must be `catalog.gitstore.dev/v1beta1`     |
| `kind`       | string | yes           | Must be `Product` (case-sensitive)         |
| `metadata`   | object | yes           | See Metadata Fields                        |
| `spec`       | object | yes           | May be empty (`spec: {}`); see Spec Fields |
| `status`     | —      | **forbidden** | System-managed; presence causes rejection  |

---

## Metadata Fields

| Field                  | Type              | Required | Constraint                                                                                                  |
|------------------------|-------------------|----------|-------------------------------------------------------------------------------------------------------------|
| `metadata.name`        | string            | yes      | DNS subdomain format                                                                                        |
| `metadata.namespace`   | string            | no       | Optional. Inferred from the repository's owning namespace at push time; raw repository UUID is never stored |
| `metadata.labels`      | map[string]string | no       | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars                                               |
| `metadata.annotations` | map[string]string | no       |                                                                                                             |

**Forbidden metadata fields** (read-only, system-assigned):  
`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

## Identity, deletion, and lifecycle

GitStore identifies a product by `apiVersion`, `kind`, resolved namespace, and `metadata.name`; the file path is provenance only. Moving a product file preserves `metadata.uid`. Changing `spec` or the Markdown body increments `metadata.generation` and `metadata.resourceVersion`. Path-only moves and label/annotation-only edits preserve `generation` and increment `resourceVersion`.

Deleting a manifest starts foreground termination rather than immediately
removing the Product. A Product with a blocking `ProductVariant` owner
reference cannot begin deletion. Once termination starts, the API exposes
`metadata.deletionTimestamp` and the foreground finalizer; a controller makes
a fresh indexed blocker check and emits the final removal only when it is safe.
GraphQL deletion uses the opaque Product node ID: `DeleteProductInput { id }`.
The same Product can be updated through GraphQL after Git push: its stored
repository and original source path are retained as provenance and determine
where the commit is written.

---

## Spec Fields

All spec fields are individually optional. Constraints apply when the field is present.

| Field              | Type                      | Constraint                                             |
|--------------------|---------------------------|--------------------------------------------------------|
| `spec.title`       | string                    | Max 200 characters                                     |
| `spec.categoryRef` | object                    | If present, `categoryRef.name` is required             |
| `spec.tags`        | []string                  | No per-tag length constraint                           |
| `spec.media`       | []MediaDefinition         | Each entry: `fileRef.name` and `fileRef.kind` required |
| `spec.options`     | []ProductOptionDefinition | Each entry: `name` required and unique within the list |
| `spec.lifecycle.state` | `ACTIVE` or `RETIRED` | Defaults to `ACTIVE`; `RETIRED` remains private desired state and is excluded from future release candidates |

### MediaDefinition

| Field              | Type   | Required                                 |
|--------------------|--------|------------------------------------------|
| `fileRef.name`     | string | yes — even when `fileRef.optional: true` |
| `fileRef.kind`     | string | yes                                      |
| `fileRef.optional` | bool   | no                                       |

### ProductOptionDefinition

| Field    | Type     | Required                                   |
|----------|----------|--------------------------------------------|
| `name`   | string   | yes — must be unique within `spec.options` |
| `title`  | string   | no                                         |
| `values` | []string | no                                         |

---

## Examples

| File                                                     | Outcome                                             |
|----------------------------------------------------------|-----------------------------------------------------|
| [examples/valid-product.md](examples/valid-product.md)   | Accepted — complete valid product                   |
| [examples/invalid-status.md](examples/invalid-status.md) | Rejected — `status` key is system-managed           |
| [examples/invalid-title.md](examples/invalid-title.md)   | Rejected — `spec.title` exceeds 200 characters      |
| [examples/invalid-media.md](examples/invalid-media.md)   | Rejected — `spec.media[0].fileRef.name` is required |

---

## Validation Error Format

Errors follow the pattern `validate: <field-path> <violation>`. Multiple violations are reported together in a single response separated by newlines. See [contracts/validation-errors.md](../../specs/017-product-spec-validation/contracts/validation-errors.md) for the full error catalogue.

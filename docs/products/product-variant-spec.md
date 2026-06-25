# ProductVariant Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `ProductVariant`

A ProductVariant resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. It represents a purchasable configuration of a parent `Product` with its own SKU, pricing rules, inventory policy, and optional media references.

---

## Envelope Fields

| Field        | Type   | Required      | Constraint                                |
|--------------|--------|---------------|-------------------------------------------|
| `apiVersion` | string | yes           | Must be `catalog.gitstore.dev/v1beta1`    |
| `kind`       | string | yes           | Must be `ProductVariant` (case-sensitive) |
| `metadata`   | object | yes           | See Metadata Fields                       |
| `spec`       | object | yes           | See Spec Fields                           |
| `status`     | —      | **forbidden** | System-managed; presence causes rejection |

---

## Metadata Fields

| Field                  | Type              | Required | Constraint                                                    |
|------------------------|-------------------|----------|---------------------------------------------------------------|
| `metadata.name`        | string            | yes      | DNS subdomain format                                          |
| `metadata.namespace`   | string            | no       | Inferred from the repository's owning namespace at push time  |
| `metadata.labels`      | map[string]string | no       | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars |
| `metadata.annotations` | map[string]string | no       |                                                               |

**Forbidden metadata fields** (read-only, system-assigned):
`uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

## Lifecycle

GitStore identifies a variant by `apiVersion`, `kind`, resolved namespace, and `metadata.name`; the file path is provenance only. Moving a variant file preserves `metadata.uid`. Changing `spec` or the Markdown body increments `metadata.generation` and `metadata.resourceVersion`. Path-only moves and label/annotation-only edits preserve `generation` and increment `resourceVersion`. Deleting the file removes the variant from GraphQL reads after post-receive admission; adding the same identity again later creates a new UID.

---

## Spec Fields

All spec fields are individually optional unless noted otherwise. Constraints apply when the field is present.

| Field                  | Type   | Constraint                                                                                          |
|------------------------|--------|-----------------------------------------------------------------------------------------------------|
| `spec.title`           | string | Max 200 characters                                                                                  |
| `spec.sku`             | string | Unique per namespace; duplicate SKU admissions are skipped and leave the existing variant unchanged |
| `spec.productRef.name` | string | Required; name of the parent `Product` resource                                                     |
| `spec.selectedOptions` | list   | Optional; each entry must match a parent product option                                             |
| `spec.pricing`         | object | Optional; see Pricing Fields                                                                        |
| `spec.inventory`       | object | Optional; see Inventory Fields                                                                      |
| `spec.media`           | list   | Optional; file references for catalog presentation                                                  |

### Product Reference

| Field                 | Type   | Required | Constraint                      |
|----------------------|--------|----------|---------------------------------|
| `productRef.name`    | string | yes      | Name of the parent `Product`    |
| `productRef.kind`    | string | no       | Defaults to `Product`           |
| `productRef.optional`| bool   | no       | Present for parity only; ignored |

### Selected Options

Each selected option entry must contain:

| Field   | Type   | Required | Constraint                               |
|---------|--------|----------|------------------------------------------|
| `name`  | string | yes      | Must exist on the parent product options |
| `value` | string | yes      | One of the parent option values          |

### Media Fields

| Field                       | Type   | Required | Constraint                                     |
|-----------------------------|--------|----------|------------------------------------------------|
| `media[*].fileRef.name`     | string | yes      | Name of the `File` resource                    |
| `media[*].fileRef.kind`     | string | no       | Defaults to `"File"`                           |
| `media[*].fileRef.optional` | bool   | no       | When `true`, admission succeeds if absent      |

### Pricing Fields (`spec.pricing.priceSet`)

| Field             | Type   | Required | Constraint                             |
|-------------------|--------|----------|----------------------------------------|
| `priceSet.name`   | string | no       | Human-readable name for this price set |
| `priceSet.prices` | list   | no       | List of `PriceTemplate` entries        |

Each `PriceTemplate` supports:

| Field                                   | Type     | Constraint                                                |
|-----------------------------------------|----------|-----------------------------------------------------------|
| `name`                                  | string   | Required within prices list                               |
| `currencyCode`                          | string   | ISO 4217 currency code                                    |
| `amount`                                | string   | Decimal string (e.g. `"9.99"`)                            |
| `priority`                              | integer  | Lower = higher priority                                   |
| `strategy.type`                         | string   | One of: `fixed`, `fixedUnitPrice`, `percentage`, `tiered` |
| `validFromTime`                         | RFC 3339 | Must be before `validUntilTime` if both set               |
| `validUntilTime`                        | RFC 3339 | Must be after `validFromTime` if both set                 |
| `quantity.min`                          | integer  | Must be ≤ `quantity.max`                                  |
| `quantity.max`                          | integer  | Must be ≥ `quantity.min`                                  |
| `eligibility.operator`                  | string   | `All` or `Any`                                            |
| `eligibility.constraints[*].expression` | string   | CEL expression — syntax validated at admission            |

### Inventory Fields (`spec.inventory`)

| Field               | Type    | Constraint                                  |
|---------------------|---------|---------------------------------------------|
| `managed`           | boolean | Whether stock is tracked                    |
| `policy`            | string  | One of: `""` (default), `deny`, `backorder` |
| `stockLocationRefs` | list    | References to stock location resources      |

---

## Status (system-managed)

The `status` block is written by the admission controller and must not be authored.

| Field                       | Meaning                                                                                                      |
|-----------------------------|--------------------------------------------------------------------------------------------------------------|
| `status.observedGeneration` | Tracks the latest admitted `metadata.generation`; generation increments when `spec` or Markdown body changes |
| `status.conditions`         | List of named conditions (see below)                                                                         |
| `status.resolved`           | Computed summaries populated at admission                                                                    |

### Conditions

| Type                | `True` means                                     | `False` means                           |
|---------------------|--------------------------------------------------|-----------------------------------------|
| `AdmissionAccepted` | Variant stored successfully                      | Storage failure                         |
| `ProductResolved`   | Parent product found in datastore                | Parent not yet stored (deferred)        |
| `OptionsAccepted`   | All `selectedOptions` names match parent product | Unknown option name encountered         |
| `PricingAccepted`   | All CEL expressions are syntactically valid      | At least one expression failed to parse |
| `MediaAccepted`     | All referenced files passed admission checks     | A media `fileRef` is invalid or missing |

`ProductResolved=False` is expected when a product and its variants are pushed in the same commit. The admission controller stores both; a reconciler loop resolves the product reference asynchronously.

### Resolved Fields

`status.resolved` holds admission-time summaries for efficient storefront queries:

```yaml
status:
  resolved:
    product:
      name: my-t-shirt
      uid: 01932f4a-...
    priceSet:
      name: multi
      hash: sha256:...
      priceCount: 2
      compiledExpressions: 1
      currencies: [USD, EUR]
      strategies: [fixed]
    inventory:
      managed: false
      availableQuantity: 0
      policy: ""
    media:
    - fileRef:
        name: product-hero
        kind: File
      url: https://cdn.example.com/products/my-t-shirt/hero.jpg
```

---

## Minimal Example

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: my-t-shirt-red-m
  namespace: my-store
spec:
  title: My T-Shirt - Red, Medium
  sku: TSHIRT-RED-M
  productRef:
    name: my-t-shirt
  selectedOptions:
  - name: colour
    value: Red
  - name: size
    value: M
  pricing:
    priceSet:
      name: default
      prices:
      - name: usd
        currencyCode: USD
        amount: "19.99"
        priority: 0
        strategy:
          type: fixed
  inventory:
    managed: true
    policy: deny
  media:
  - fileRef:
      name: product-hero
      kind: File
      optional: true
---

Red Medium variant of the My T-Shirt product.
```

---

## Single-pass catalog authoring

GitStore allows an entire catalog to be pushed in one commit. You can push a `Product` and all its `ProductVariant` files together:

```
catalog/
  products/
    my-t-shirt.md       # kind: Product
  variants/
    my-t-shirt-red-m.md # kind: ProductVariant
    my-t-shirt-blue-l.md
```

The admission controller derives create, update, delete, and move operations by comparing the old and new commit. When it encounters a variant whose `productRef` points to a product that is not yet in the datastore (because it is also being admitted from the same push), it stores the variant with `ProductResolved=False` and resolves the reference asynchronously. No multi-step workflow is required.

The same single-pass model applies to co-pushed `CategoryTaxonomy` and `Collection` resources.

File paths are not resource identity. Moving a variant file while keeping the same `apiVersion`, `kind`, namespace, and `metadata.name` preserves `metadata.uid`. Deleting a variant removes it from GraphQL reads after admission; adding the same identity again in a later commit creates a new UID.

---

## Validation Errors

Errors follow the pattern `validate: <field-path> <violation>`.

| Condition                             | Error message pattern                               |
|---------------------------------------|------------------------------------------------------|
| Wrong `apiVersion`                    | `validate: apiVersion must be "catalog.gitstore.dev/v1beta1"` |
| Wrong `kind`                          | `validate: kind must be "ProductVariant"`           |
| Missing `metadata.name`               | `validate: metadata.name is required`               |
| Missing `spec.title`                  | `validate: spec.title is required`                  |
| Missing `spec.productRef.name`        | `validate: spec.productRef.name is required`        |
| Media `fileRef.name` missing or empty | `validate: spec.media[N].fileRef.name is required`  |
| Media `fileRef.kind` missing or empty | `validate: spec.media[N].fileRef.kind is required`  |
| `status` key present                  | `validate: "status" is a system-managed field`      |

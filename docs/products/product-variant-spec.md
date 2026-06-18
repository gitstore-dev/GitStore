# ProductVariant Spec Reference

**API Version**: `catalog.gitstore.dev/v1beta1`  
**Kind**: `ProductVariant`

A ProductVariant resource is a Markdown file with YAML frontmatter pushed to a GitStore repository. It represents a purchasable configuration of a parent `Product` — a specific combination of options (e.g. colour=Red, size=M) with its own SKU, pricing rules, and inventory policy.

---

## Envelope Fields

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `apiVersion` | string | yes | Must be `catalog.gitstore.dev/v1beta1` |
| `kind` | string | yes | Must be `ProductVariant` (case-sensitive) |
| `metadata` | object | yes | See Metadata Fields |
| `spec` | object | yes | See Spec Fields |
| `status` | — | **forbidden** | System-managed; presence causes rejection |

---

## Metadata Fields

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `metadata.name` | string | yes | DNS subdomain format |
| `metadata.namespace` | string | no | Inferred from the repository's owning namespace at push time |
| `metadata.labels` | map[string]string | no | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars |
| `metadata.annotations` | map[string]string | no | |

---

## Spec Fields

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `spec.title` | string | yes | Max 200 characters |
| `spec.sku` | string | yes | Unique per namespace; duplicate SKU admissions are skipped and leave the existing variant unchanged |
| `spec.productRef.name` | string | yes | Name of the parent `Product` resource |
| `spec.selectedOptions` | list | no | Each entry: `name` + `value`; names must match parent product options |
| `spec.pricing` | object | no | See Pricing Fields |
| `spec.inventory` | object | no | See Inventory Fields |

### Pricing Fields (`spec.pricing.priceSet`)

| Field | Type | Required | Constraint |
|-------|------|----------|-----------|
| `priceSet.name` | string | no | Human-readable name for this price set |
| `priceSet.prices` | list | no | List of `PriceTemplate` entries |

Each `PriceTemplate` supports:

| Field | Type | Constraint |
|-------|------|-----------|
| `name` | string | Required within prices list |
| `currencyCode` | string | ISO 4217 currency code |
| `amount` | string | Decimal string (e.g. `"9.99"`) |
| `priority` | integer | Lower = higher priority |
| `strategy.type` | string | One of: `fixed`, `fixedUnitPrice`, `percentage`, `tiered` |
| `validFromTime` | RFC 3339 | Must be before `validUntilTime` if both set |
| `validUntilTime` | RFC 3339 | Must be after `validFromTime` if both set |
| `quantity.min` | integer | Must be ≤ `quantity.max` |
| `quantity.max` | integer | Must be ≥ `quantity.min` |
| `eligibility.operator` | string | `All` or `Any` |
| `eligibility.constraints[*].expression` | string | CEL expression — syntax validated at admission |

### Inventory Fields (`spec.inventory`)

| Field | Type | Constraint |
|-------|------|-----------|
| `managed` | boolean | Whether stock is tracked |
| `policy` | string | One of: `""` (default), `deny`, `backorder` |
| `stockLocationRefs` | list | References to stock location resources |

---

## Status (system-managed)

The `status` block is written by the admission controller and must not be authored. Key fields:

| Field | Meaning |
|-------|---------|
| `status.observedGeneration` | Tracks the latest admitted `metadata.generation`; generation increments when `spec` or Markdown body changes |
| `status.conditions` | List of named conditions (see below) |
| `status.resolved` | Computed summaries populated at admission |

### Conditions

| Type | `True` means | `False` means |
|------|-------------|--------------|
| `AdmissionAccepted` | Variant stored successfully | Storage failure |
| `ProductResolved` | Parent product found in datastore | Parent not yet stored (deferred) |
| `OptionsAccepted` | All `selectedOptions` names match parent product | Unknown option name encountered |
| `PricingAccepted` | All CEL expressions are syntactically valid | At least one expression failed to parse |

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
  title: My T-Shirt — Red, Medium
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

This is a key differentiator from traditional e-commerce platforms where catalog setup requires:

1. Create product via admin UI → save
2. Navigate to variants tab → add variant one at a time

With GitStore:

1. Write all `.md` files locally
2. `git push` — everything is admitted in one operation

The same single-pass model applies to co-pushed `CategoryTaxonomy` and `Collection` resources.

File paths are not resource identity. Moving a variant file while keeping the same `apiVersion`, `kind`, namespace, and `metadata.name` preserves `metadata.uid`. Deleting a variant removes it from GraphQL reads after admission; adding the same identity again in a later commit creates a new UID.

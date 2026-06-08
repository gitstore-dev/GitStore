# Quickstart: ProductVariant Catalog Item

**Branch**: `024-product-variant` | **Date**: 2026-06-08

## What is a ProductVariant?

A `ProductVariant` is the purchasable unit in the GitStore catalog. A `Product` is a non-sellable parent descriptor (title, category, options matrix); each `ProductVariant` represents a specific sellable SKU — a concrete combination of options (e.g. color + size) with its own pricing rules, inventory controls, and media.

## Single-pass catalog authoring

The key advantage of git-backed catalog management: push an entire catalog — products, variants, categories, collections — in a single commit. No sequential API calls, no multiple round trips through the admin UI.

```bash
# Author product + variants in one commit
git add catalog/products/macbook-pro.md
git add catalog/variants/macbook-pro-silver-16.md
git add catalog/variants/macbook-pro-silver-14.md
git commit -m "feat: add MacBook Pro M4 with two variants"
git push origin main
```

All three resources are admitted in the same push. The variants' `productRef` is resolved on the next reconciliation pass if the product wasn't already in the datastore.

## Authoring a ProductVariant document

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  name: macbook-pro-silver-16-64gb-1tb
  namespace: my-store
  labels:
    gitstore.dev/brand: apple
    processor: m4-max
    ram: 64gb
spec:
  title: "MacBook Pro Silver 16\" 64GB 1TB"
  sku: MBP-SILVER-16-64-1TB
  productRef:
    name: macbook-pro
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
  inventory:
    managed: true
    policy: deny
  pricing:
    priceSet:
      name: ps_mbp_silver_16_64_1tb
      prices:
      - name: base-eur
        currencyCode: EUR
        amount: "2499.00"
        strategy:
          type: fixedUnitPrice
        priority: 0
      - name: eu-business
        currencyCode: EUR
        amount: "2399.00"
        strategy:
          type: fixedUnitPrice
        priority: 50
        eligibility:
          operator: All
          constraints:
          - expression: "region.code == 'EU'"
          - expression: "customer.group == 'business'"
  selectedOptions:
  - name: color
    value: silver
  - name: size
    value: "16"
  - name: ram
    value: 64GB
  - name: storage
    value: 1TB SSD
  media:
  - fileRef:
      name: macbook-hero
      optional: true
---
Variant-specific copy describing this configuration.
```

## Validation phases

| Phase | What's checked | DB access |
|---|---|---|
| pre-receive | Required fields, valid enums (`inventory.policy`, `strategy.type`), `validFromTime ≤ validUntilTime`, `quantity.min ≤ quantity.max` | None |
| admission | SKU uniqueness, `productRef` resolution, `selectedOptions` compatibility, option-set uniqueness per product, CEL expression syntax | Yes |

## Querying via GraphQL

```graphql
query GetVariant {
  productVariant(by: { namespacePath: { namespace: "my-store", name: "macbook-pro-silver-16-64gb-1tb" } }) {
    id
    spec {
      title
      sku
      selectedOptions { name value }
      pricing {
        priceSet {
          name
          prices { name currencyCode amount priority }
        }
      }
      inventory { managed policy }
    }
    status {
      conditions { type status reason message }
      resolved {
        product { name uid }
        selectedOptionsHash
        priceSet { priceCount currencies strategies }
        inventory { managed availableQuantity policy }
      }
    }
  }
}

# List all variants in a namespace
query ListVariants {
  productVariants(namespace: "my-store", first: 20) {
    edges { node { spec { sku title } } }
    pageInfo { hasNextPage endCursor }
    totalCount
  }
}

# Traverse variants from a product
query ProductWithVariants {
  product(by: { namespacePath: { namespace: "my-store", name: "macbook-pro" } }) {
    spec { title options { name values } }
    productVariants(first: 10) {
      edges { node { spec { sku selectedOptions { name value } } } }
    }
  }
}
```

## Common rejection errors

| Scenario | Phase | Error |
|---|---|---|
| Missing `spec.sku` | pre-receive | `spec.sku is required` |
| Invalid `inventory.policy: "hold"` | pre-receive | `spec.inventory.policy must be one of: deny, backorder` |
| `validFromTime` after `validUntilTime` | pre-receive | `prices[0].validFromTime must not be after validUntilTime` |
| `quantity.min > quantity.max` | pre-receive | `prices[1].quantity.min must not exceed quantity.max` |
| Duplicate SKU | admission | `spec.sku "MBP-SILVER-16-64-1TB" already exists in namespace "my-store"` |
| Unknown parent product | admission | admitted with `ProductResolved: False`; reconciler retries |
| Option name not on product | admission | `selectedOptions[0].name "color" not found in product "macbook-pro" options` |
| Duplicate option combination | admission | `selectedOptions combination already exists for product "macbook-pro"` |
| Invalid CEL expression | admission | `prices[1].eligibility.constraints[0].expression: syntax error at 1:10` |

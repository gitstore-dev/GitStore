# Data Model: ProductVariant Catalog Item

**Branch**: `024-product-variant` | **Phase**: 1 | **Date**: 2026-06-08

## Overview

`ProductVariant` follows the same Kubernetes-style resource envelope as `Product`, `CategoryTaxonomy`, and `Collection`. It is a namespace-scoped, git-backed resource stored in the datastore as a single row with a `Spec json.RawMessage` and `Status json.RawMessage`.

---

## Go Catalog Resource Structs

### `gitstore-api/internal/catalog/product_variant.go` (new file)

```
ProductVariantResource
  APIVersion   string
  Kind         string              // "ProductVariant"
  Metadata     ObjectMeta          // reuse existing type from product.go
  Spec         ProductVariantSpec
  Body         string              // markdown body content

ProductVariantSpec
  Title           string
  SKU             string
  ProductRef       CatalogObjectReference   // reuse from product.go
  Inventory        InventoryDefinition
  Pricing          PricingDefinition
  SelectedOptions  []SelectedOptionDefinition
  Media            []MediaDefinition        // reuse from product.go

InventoryDefinition
  Managed           bool
  Policy            string   // enum: "deny" | "backorder"
  StockLocationRefs []CatalogObjectReference

PricingDefinition
  PriceSet  PriceSet

PriceSet
  Name    string
  Prices  []PriceTemplate

PriceTemplate
  Name           string
  ValidFromTime   *time.Time
  ValidUntilTime  *time.Time
  Quantity        *QuantityDefinition
  CurrencyCode    string
  Amount          string   // Decimal as string; parsed to shopspring/decimal
  Strategy        StrategyDefinition
  Priority        int8
  Eligibility     *EligibilityDefinition

QuantityDefinition
  Min  int16
  Max  *int16   // nil = unbounded

StrategyDefinition
  Type  string   // e.g. "fixedUnitPrice"

EligibilityDefinition
  Operator     string   // "All" | "Any"
  Constraints  []PriceRuleConstraint

PriceRuleConstraint
  Name        string
  Expression  string   // CEL expression (syntax validated at admission)

SelectedOptionDefinition
  Name   string
  Value  string
```

---

## Datastore Entity

### `gitstore-api/internal/datastore/entities.go` — add `ProductVariant` struct

Same envelope as `Product`:

```
ProductVariant
  UID               string   (UUID, system-assigned)
  Namespace         string
  Name              string
  APIVersion        string
  Kind              string
  Generation        int64
  ResourceVersion   string
  CreationTimestamp time.Time
  Revision          string
  Labels            map[string]string
  Annotations       map[string]string
  OwnerRefs         json.RawMessage
  GitCommitSHA      string
  GitRef            string
  Spec              json.RawMessage   // ProductVariantSpec serialised
  Body              string
  Status            json.RawMessage   // ProductVariantStatus serialised
```

---

## Datastore Interface

### `gitstore-api/internal/datastore/datastore.go` — add to `Datastore` interface

```
CreateProductVariant(ctx, variant *ProductVariant) error
UpdateProductVariant(ctx, variant *ProductVariant) error
GetProductVariantByUID(ctx, uid string) (*ProductVariant, error)
GetProductVariantByName(ctx, namespace, name string) (*ProductVariant, error)
GetProductVariantBySKU(ctx, namespace, sku string) (*ProductVariant, error)
ListProductVariants(ctx, namespace string, opts PaginationOpts) ([]*ProductVariant, error)
ListProductVariantsByProductRef(ctx, namespace, productName string) ([]*ProductVariant, error)
```

---

## Memdb Table Schema

### `gitstore-api/internal/datastore/memdb/schema.go` — add `"product_variant"` table

```
Table: "product_variant"

Indexes:
  "id"              — UUIDFieldIndex on UID; unique, allowMissing: false
  "name_namespace"  — CompoundIndex [Namespace, Name]; unique
  "namespace"       — StringFieldIndex on Namespace; non-unique
  "sku_namespace"   — CompoundIndex [Namespace, SKU-from-spec]; unique
                      (SKU is denormalised onto the entity for indexing)
  "product_ref"     — CompoundIndex [Namespace, ProductRefName]; non-unique
                      (ProductRefName denormalised from spec.productRef.name)
```

Note: `SKU` and `ProductRefName` are denormalised string fields added to the `ProductVariant` entity struct to support memdb field-based indexing. They are always kept in sync with `Spec` during writes.

---

## Status Types

### `gitstore-api/internal/catalog/product_variant.go` (continued)

```
ProductVariantStatus
  ObservedGeneration   int64
  LastAppliedRevision  string
  Conditions           []Condition    // reuse existing type
  Resolved             *ResolvedProductVariantDefinition

ResolvedProductVariantDefinition
  Product                  ResolvedProductRef
  SelectedOptionsHash       string   // sha256 of sorted name:value pairs
  PriceSet                  ResolvedPriceSetDefinition
  Inventory                 ResolvedInventoryDefinition
  Media                     []ResolvedFileDefinition  // reuse from product.go

ResolvedProductRef
  Name  string
  UID   string

ResolvedPriceSetDefinition
  Name                 string
  Hash                 string    // sha256 of compiled price entries
  CompiledExpressions  int16     // total CEL constraint expressions parsed
  PriceCount           int64
  Currencies           []string
  Strategies           []string

ResolvedInventoryDefinition
  Managed            bool
  AvailableQuantity  int64   // populated by external inventory reconciler
  Policy             string
```

---

## Condition Types

Add to `gitstore-api/internal/catalog/status.go`:

```
ConditionAdmissionAccepted  — structural + schema checks passed (set at admission)
ConditionProductResolved    — productRef resolved to an existing Product
ConditionOptionsAccepted    — selectedOptions valid against parent product options
ConditionPricingAccepted    — CEL expressions parsed, priceSet valid
ConditionReady              — all above True; variant ready for checkout pricing
```

Existing `ConditionVariantsResolved` and `ConditionOptionsAccepted` constants in `status.go` may already be defined — check before adding.

---

## Validation Rules

### Pre-receive (stateless, `validate/validator.go` → `validateProductVariantSpec`)

| Field | Rule |
|---|---|
| `apiVersion` | must equal `catalog.gitstore.dev/v1beta1` |
| `kind` | must equal `ProductVariant` |
| `metadata.name` | required, DNS-label format |
| `metadata.namespace` | required |
| `spec.title` | required, non-empty |
| `spec.sku` | required, non-empty |
| `spec.productRef.name` | required |
| `spec.inventory.policy` | if present: `deny` or `backorder` |
| `spec.pricing.priceSet.prices[*].strategy.type` | recognised value (e.g. `fixedUnitPrice`) |
| `spec.pricing.priceSet.prices[*].validFromTime` vs `validUntilTime` | `validFromTime` MUST NOT be after `validUntilTime` |
| `spec.pricing.priceSet.prices[*].quantity.min` vs `max` | if both set: `min` MUST NOT exceed `max` |

### Admission control (DB-backed, `cataloggrpc/server.go` → `admitProductVariant`)

| Check | Rule |
|---|---|
| SKU uniqueness | `GetProductVariantBySKU(namespace, sku)` must return not-found |
| `productRef` resolution | `GetProductByName(namespace, productRef.name)`: if found → `ProductResolved: True`; if not found → admit with `ProductResolved: False`, defer to reconciler |
| `selectedOptions` name validity | each `selectedOptions.name` must appear in `product.spec.options[*].name` (skip if product not yet resolved) |
| `selectedOptions` value validity | each `selectedOptions.value` must appear in matching option's `values` list (skip if product not yet resolved) |
| `selectedOptions` combination uniqueness | fingerprint of sorted `name:value` pairs must not match any existing variant for same parent product (skip if product not yet resolved) |
| CEL expression syntax | `cel.NewEnv().Parse(expr)` must succeed for each `eligibility.constraints[*].expression` |

---

## Entity Relationships

```
Namespace
  └── Product  (1)
        └── ProductVariant  (0..N)   — linked via spec.productRef.name
              └── PriceSet  (0..1)
                    └── PriceTemplate  (0..N)
                          └── EligibilityDefinition  (0..1)
                                └── PriceRuleConstraint  (0..N, CEL)
```

- `ProductVariant` → `Product`: many-to-one (namespace-scoped)
- `ProductVariant` → `StockLocation` (via `stockLocationRefs`): stored as reference only, not resolved in this feature
- `Product.productVariants` connection: resolved at query time by `ListProductVariantsByProductRef`

---

## State Transitions

```
Pushed
  → pre-receive validation (structural)
      FAIL → push rejected, no resource created
      PASS → admission

Admission
  → SKU uniqueness check
      FAIL → push rejected
  → productRef resolution
      FOUND → OptionsAccepted check → CEL check → admit (ProductResolved: True)
      NOT FOUND → admit with ProductResolved: False, OptionsAccepted: False (reconciler will retry)
  → selectedOptions fingerprint uniqueness
      DUPLICATE → push rejected
  → CEL syntax check
      FAIL → push rejected

Admitted (ProductResolved: False)
  → control-loop reconciler retries productRef lookup
      RESOLVED → re-checks options + re-computes status → ProductResolved: True, OptionsAccepted: True/False
      STILL NOT FOUND → condition remains False

Admitted (all conditions True)
  → Ready: True
```

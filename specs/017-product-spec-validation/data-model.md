# Data Model: Product Spec/Status Validation Semantics

**Feature**: `017-product-spec-validation`  
**Date**: 2026-06-05

---

## Entities

These entities are already defined in the codebase. This document records the authoritative field-level contracts for the validation and hydration rules introduced by this feature.

---

### ProductResource *(author-writable envelope)*

**Source**: `gitstore-api/internal/catalog/product.go`  
**Stored in**: Git (YAML frontmatter in `.md` files)

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `apiVersion` | `string` | required; must equal `catalog.gitstore.dev/v1beta1` | FR-014: reported as `apiVersion` in errors |
| `kind` | `string` | required; must equal `Product` | Case-sensitive |
| `metadata` | `ObjectMeta` | required | See below |
| `spec` | `ProductSpec` | required key (may be empty object) | FR-006: `spec: {}` is valid |
| `status` | — | **FORBIDDEN** — key must not appear | FR-007; triggers pre-parse rejection |

**Forbidden top-level keys**: `status`  
**Forbidden metadata sub-keys** (FR-008): `uid`, `resourceVersion`, `generation`, `creationTimestamp`, `revision`, `ownerReferences`

---

### ObjectMeta *(author-supplied metadata)*

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `name` | `string` | required | DNS subdomain format (validated by struct tag) |
| `namespace` | `string` | optional | |
| `generateName` | `string` | optional | |
| `labels` | `map[string]string` | optional | Key prefix ≤ 253 chars; key name ≤ 63 chars; value ≤ 63 chars |
| `annotations` | `map[string]string` | optional | No length validation currently |

**Validation rules for labels** (FR-008-adjacent, enforced by `validateLabels`):  
- Label key without `/`: total length ≤ 63  
- Label key with prefix/name: prefix ≤ 253, name ≤ 63  
- Label value: length ≤ 63

---

### ProductSpec *(author-controlled declaration)*

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `title` | `string` | optional; max 200 characters | FR-001: reports field name and limit |
| `categoryRef` | `*ObjectReference` | optional; if present, `name` is required | FR-005 |
| `tags` | `[]string` | optional | No per-tag length constraint in scope |
| `media` | `[]MediaDefinition` | optional; each entry validated | FR-002 |
| `options` | `[]ProductOptionDefinition` | optional; each entry validated | FR-003, FR-004 |

**Spec-level rules**:  
- All fields are individually optional (FR-006: `spec: {}` accepted)  
- When `options` is present: each entry's `name` is required and must be unique across the list

---

### MediaDefinition

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `fileRef` | `FileReference` | required | |

### FileReference

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `name` | `string` | required | FR-002: reported as `spec.media[N].fileRef.name` |
| `kind` | `string` | required | FR-002: reported as `spec.media[N].fileRef.kind` |
| `optional` | `bool` | optional | Does NOT waive `name`/`kind` requirements |

---

### ProductOptionDefinition

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `name` | `string` | required; unique within `options` | FR-003, FR-004: reported as `options[N].name` |
| `title` | `string` | optional | |
| `values` | `[]string` | optional | |

---

### ProductStatus *(system-written, never from authors)*

**Source**: `gitstore-api/internal/catalog/status.go`  
**Stored in**: Datastore only (JSON blob in `products.status` column)  
**Written by**: Controller (sole writer per spec assumption)  
**Read by**: Catalog API (`statusFromJSON` in `converters.go`)

| Field | Type | Notes |
|-------|------|-------|
| `observedGeneration` | `int64` | FR-010: must be returned as-is |
| `lastAppliedRevision` | `string` | FR-010: must be returned; absent for unprocessed products (FR-011) |
| `conditions` | `[]Condition` | FR-010: all conditions returned; none silently dropped |
| `resolved` | `*ResolvedProductDefinition` | FR-010: returned when present |

**Absent-status rule**: When `status` column is NULL/empty, API returns `null` for `status` (not an empty object). Implemented by `statusFromJSON` returning `nil` on empty input.

---

### Condition

| Field | Type | Constraint | Notes |
|-------|------|-----------|-------|
| `type` | `ConditionType` | Known values: `Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready` | FR-012: Kubernetes TitleCase normalised to GraphQL SCREAMING_SNAKE |
| `status` | `ConditionStatus` | `True` / `False` / `Unknown` | FR-012: normalised |
| `observedGeneration` | `int64` | | |
| `lastTransitionTime` | `time.Time` | | |
| `reason` | `string` | optional | |
| `message` | `string` | optional | |

**Normalisation map** (Kubernetes TitleCase → GraphQL enum):  
`Published` → `PUBLISHED`, `AdmissionAccepted` → `ADMISSION_ACCEPTED`, `CategoryResolved` → `CATEGORY_RESOLVED`, `OptionsAccepted` → `OPTIONS_ACCEPTED`, `VariantsResolved` → `VARIANTS_RESOLVED`, `Ready` → `READY`  
`True` → `TRUE`, `False` → `FALSE`, `Unknown` → `UNKNOWN`

**Unknown condition type**: passed through uppercased (not dropped); edge case from spec.

---

### ResolvedProductDefinition

| Field | Type | Notes |
|-------|------|-------|
| `category` | `*ResolvedCategoryDefinition` | optional |
| `priceRange` | `[]PriceRangeDefinition` | FR-013: monetary values use `shopspring/decimal`; survive round-trip |
| `totalInventory` | `int64` | |
| `variantSummary` | `*VariantSummaryDefinition` | optional |
| `defaultVariantRef` | `*ObjectReference` | optional |
| `media` | `[]ResolvedFileDefinition` | optional |

---

### PriceRangeDefinition

| Field | Type | Notes |
|-------|------|-------|
| `currencyCode` | `string` | ISO 4217 (e.g. `USD`, `JPY`) |
| `min` | `decimal.Decimal` | FR-013, SC-005: no precision loss |
| `max` | `decimal.Decimal` | FR-013, SC-005: no precision loss |

---

## State Transitions

```
Product file (git push)
  │
  ├─► preParseChecks (YAML map)
  │     ├── apiVersion present?       → missing: REJECT (legacy format error)
  │     ├── spec key present?         → missing: REJECT
  │     ├── status key present?       → present: REJECT (FR-007)
  │     └── read-only metadata keys?  → present: REJECT (FR-008) [collect all, report together]
  │
  ├─► frontmatter.Parse (struct binding)
  │
  ├─► validate.Struct (struct-tag rules)
  │     ├── apiVersion == v1beta1?    → wrong: REJECT (FR field error with path)
  │     ├── kind == Product?          → wrong: REJECT
  │     ├── metadata.name present?    → missing: REJECT
  │     ├── spec.title ≤ 200?         → exceeds: REJECT (FR-001)
  │     └── media[N].fileRef name/kind required? → missing: REJECT (FR-002)
  │
  └─► validateSpec (custom rules)
        ├── options[N].name present?  → missing: REJECT with index (FR-004)
        └── options names unique?     → duplicate: REJECT (FR-003)

All violations from post-parse stages collected via errors.Join → single rejection response (FR-009)

ProductStatus (controller write)
  │
  └─► datastore.UpdateProduct(status JSON blob)
        │
        └─► statusFromJSON (read-time conversion)
              ├── nil/empty blob        → return nil (FR-011)
              ├── Kubernetes TitleCase  → normalise to GraphQL enum (FR-012)
              └── unknown condition type → pass through uppercased (edge case)
```

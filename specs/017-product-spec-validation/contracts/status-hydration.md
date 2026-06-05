# Contract: Status Hydration

**Feature**: `017-product-spec-validation` — #186  
**Date**: 2026-06-05  
**Stability**: `catalog.gitstore.dev/v1beta1`

This contract defines how the catalog API translates a controller-written `ProductStatus` JSON blob into a GraphQL `ProductStatus` response.

---

## Rules

### FR-010 — Full status returned when present

When the datastore `products.status` column contains a non-empty JSON blob:
- All `conditions` are returned (none silently dropped)
- `observedGeneration` is returned
- `lastAppliedRevision` is returned as a non-null string when non-empty
- `resolved` is returned when present in the blob

### FR-011 — Absent status for unprocessed products

When the datastore `products.status` column is NULL or empty:
- The GraphQL `status` field MUST be `null` in the response
- An empty object `{}` MUST NOT be returned

### FR-012 — Kubernetes TitleCase normalisation

The controller writes condition `type` and `status` in Kubernetes TitleCase. The API normalises these to GraphQL SCREAMING_SNAKE_CASE enums at read time.

| Controller writes | API returns |
|-------------------|-------------|
| `"Published"` | `PUBLISHED` |
| `"AdmissionAccepted"` | `ADMISSION_ACCEPTED` |
| `"CategoryResolved"` | `CATEGORY_RESOLVED` |
| `"OptionsAccepted"` | `OPTIONS_ACCEPTED` |
| `"VariantsResolved"` | `VARIANTS_RESOLVED` |
| `"Ready"` | `READY` |
| `"True"` | `TRUE` |
| `"False"` | `FALSE` |
| `"Unknown"` | `UNKNOWN` |

**Unknown condition type**: If a condition type is not in the normalisation table, it is passed through as `strings.ToUpper(type)`. The system MUST NOT crash or drop the condition.

### FR-013 — Monetary precision

`priceRange[N].min` and `priceRange[N].max` are `shopspring/decimal` values serialised as JSON strings. The API MUST return these without any truncation or rounding. This applies to all currencies including zero-decimal currencies (e.g. JPY).

---

## GraphQL Schema Reference

```graphql
type ProductStatus {
  observedGeneration: Int!
  lastAppliedRevision: String
  conditions: [ProductCondition!]!
  resolved: ResolvedProductDefinition
}

type ProductCondition {
  type: ProductConditionType!
  status: ConditionStatus!
  observedGeneration: Int
  lastTransitionTime: Time!
  reason: String
  message: String
}

enum ProductConditionType {
  PUBLISHED
  ADMISSION_ACCEPTED
  CATEGORY_RESOLVED
  OPTIONS_ACCEPTED
  VARIANTS_RESOLVED
  READY
}

enum ConditionStatus {
  TRUE
  FALSE
  UNKNOWN
}
```

---

## Example: Full Status Blob (controller-written)

```json
{
  "observedGeneration": 3,
  "lastAppliedRevision": "main@sha1:abc123def456",
  "conditions": [
    {
      "type": "Ready",
      "status": "True",
      "observedGeneration": 3,
      "lastTransitionTime": "2026-06-01T12:00:00Z",
      "reason": "AllChecksPass",
      "message": "product is ready for sale"
    },
    {
      "type": "Published",
      "status": "True",
      "observedGeneration": 3,
      "lastTransitionTime": "2026-06-01T12:00:00Z"
    }
  ],
  "resolved": {
    "priceRange": [
      { "currencyCode": "USD", "min": "999.00", "max": "1999.00" },
      { "currencyCode": "JPY", "min": "149000", "max": "299000" }
    ],
    "totalInventory": 42
  }
}
```

**Expected GraphQL response** (partial):

```json
{
  "status": {
    "observedGeneration": 3,
    "lastAppliedRevision": "main@sha1:abc123def456",
    "conditions": [
      {
        "type": "READY",
        "status": "TRUE",
        "observedGeneration": 3,
        "reason": "AllChecksPass",
        "message": "product is ready for sale"
      },
      {
        "type": "PUBLISHED",
        "status": "TRUE",
        "observedGeneration": 3
      }
    ],
    "resolved": {
      "priceRange": [
        { "currencyCode": "USD", "min": "999.00", "max": "1999.00" },
        { "currencyCode": "JPY", "min": "149000", "max": "299000" }
      ],
      "totalInventory": 42
    }
  }
}
```

# Research: ProductVariant Catalog Item

**Branch**: `024-product-variant` | **Phase**: 0 | **Date**: 2026-06-08

## Summary

All NEEDS CLARIFICATION items from Technical Context are resolved. Research covers: validation pipeline phases, CEL dependency addition, datastore table design, selectedOptions uniqueness enforcement, and co-push reconciliation semantics.

---

## Decision 1: Validation pipeline phase split

**Decision**: Two-phase validation — pre-receive (stateless, no DB) and admission control (DB-backed, includes CEL syntax check).

**Rationale**: The existing `ValidateResources` gRPC method (called from `SchemaValidationHandler` in the Rust git service) handles the pre-receive phase. The `AdmitResources` method handles admission. The same pattern used for Product and Collection applies to ProductVariant. Pre-receive must remain fast and stateless (no DB) per the architecture constraint from `HookPipeline` timeout configuration.

**Alternatives considered**:
- All validation in pre-receive: rejected — DB lookups (SKU uniqueness, productRef resolution, option compatibility) are too expensive and stateful for the pre-receive critical path.
- All validation in admission: rejected — syntactic errors (missing required fields, invalid enums, invalid time ranges) are cheapest to catch pre-receive.

---

## Decision 2: CEL dependency for expression syntax validation

**Decision**: Add `github.com/google/cel-go` to `gitstore-api/go.mod` for CEL expression parsing at admission time. Use the CEL `NewEnv()` + `Parse()` API for syntax-only checking (no type-checking environment needed).

**Rationale**: No CEL library exists in `gitstore-api/go.mod` today. `cel-go` is the official Go implementation of the CEL spec and is used by Kubernetes itself for admission webhooks. Syntax-only validation requires only `cel.NewEnv()` and `env.Parse(expr)` — no variable declarations, program construction, or evaluation are needed.

**Alternatives considered**:
- Regex-based expression validation: rejected — too fragile, doesn't catch all CEL syntax errors correctly.
- Defer CEL validation to a future feature: rejected — pricing expressions with silent syntax errors would reach the storefront undetected.
- Full CEL type-checking at admission: deferred — requires defining the full evaluation context (cart, region, customer variables) which is out of scope; syntax check is sufficient.

---

## Decision 3: ProductVariant datastore table

**Decision**: Add a `"product_variant"` table to the memdb schema (`schema.go`) with the same envelope structure as the `"product"` table, adding a compound `"sku_namespace"` unique index and a `"product_ref"` non-unique index.

**Rationale**: All existing catalog tables (`"product"`, `"category_taxonomy"`, `"collection"`) share the same envelope — `UID`, `Namespace`, `Name`, `Spec json.RawMessage`, `Status json.RawMessage`, `Labels`, `Annotations`, etc. The same pattern is the lowest-friction path. A dedicated `"sku_namespace"` compound index enables O(1) SKU uniqueness checks at admission. A `"product_ref"` index enables efficient listing of all variants for a given parent product (used by `Product.productVariants` GraphQL connection and by the control-loop reconciler).

**ScyllaDB (production)**: Three-table pattern as used by Collection is **not** needed here. ProductVariant is a single resource; the variant→product relationship is captured in `spec.productRef` (already in `Spec json.RawMessage`). A single `product_variant` table with a secondary index on `(namespace, product_ref_name)` is sufficient.

**Alternatives considered**:
- Inline variants in the product row: rejected — variants are independent resources with their own lifecycle, SKU, and status. Embedding would break the Kubernetes-style resource model and complicate individual variant updates.

---

## Decision 4: selectedOptions uniqueness enforcement

**Decision**: Enforce at admission via a datastore query: list all existing variants for the parent product in the namespace, compute the option-set fingerprint (sorted `name:value` pairs), and compare against the incoming variant's fingerprint. Reject if duplicate.

**Rationale**: This is a semantic uniqueness constraint that requires a DB lookup (same as SKU uniqueness), so it belongs in the admission phase. A fingerprint approach (sorted key-value pairs) is deterministic and order-independent — `[{color:silver, size:16}]` == `[{size:16, color:silver}]`.

**Alternatives considered**:
- Add a dedicated compound index for option combinations: complex to implement in memdb for variable-length arrays; fingerprint in a string index is simpler.
- Allow duplicates, warn via condition: rejected per user clarification — ambiguous option-matrix lookups are a data integrity issue.

---

## Decision 5: Co-push (product + variant in same commit) reconciliation

**Decision**: Admission admits the variant with `ProductResolved: False` when the parent product is co-pushed and not yet in the datastore. The control-loop reconciler (Go) re-attempts `productRef` resolution and option compatibility checks, transitioning the condition to `True` once the product is available.

**Rationale**: The core value proposition of GitStore's git-push model is single-pass catalog authoring — operators can push an entire catalog (products, variants, categories, collections) in one commit without multiple sequential pushes. This matches how the existing `CategoryTaxonomy` topo-sort works in `AdmitResources`: the admission pass handles what it can; the control loop handles cross-resource dependencies. The `OptionsAccepted` condition also starts `False` for co-pushed variants (can't validate options without the product) and is resolved by the reconciler.

**Alternatives considered**:
- Process products before variants within a push: would require ordering guarantees across arbitrary file paths, coupling the push pipeline to resource type ordering. Rejected.
- Reject variant if product not in datastore at admission: breaks the single-pass model. Rejected.

---

## Decision 6: `quantity.min > quantity.max` handling

**Decision**: Reject at pre-receive. This is a stateless structural constraint (two integer fields on the same `PriceTemplate` object), identical in nature to the `validFromTime > validUntilTime` check.

**Rationale**: Consistent with the phase-split rule: if it requires no DB lookup, it's pre-receive's job. Catching this early gives operators immediate feedback without consuming admission resources.

---

## Resolved NEEDS CLARIFICATION Summary

| Item | Resolution |
|---|---|
| CEL dependency | Add `cel-go`; syntax-only at admission |
| `manageInventory` vs `managed` | `spec.inventory.managed` nested in `InventoryDefinition` |
| Co-push ordering | Admit with `ProductResolved: False`; reconciler resolves |
| `validFromTime > validUntilTime` | Reject at pre-receive |
| `stockLocationRefs` when `managed: false` | Store as-is; silently inactive |
| `selectedOptions` duplicate combinations | Reject at admission via fingerprint check |
| `quantity.min > quantity.max` | Reject at pre-receive |

# Feature Specification: ProductVariant Catalog Item

**Feature Branch**: `024-product-variant`
**Created**: 2026-06-08
**Status**: Closed
**Input**: User description: "let's continue with the productvariant catalog item"

## Clarifications

### Session 2026-06-08

- Q: At which pipeline phase does `selectedOptions` / `productRef` validation occur — pre-receive or admission? → A: Admission control phase. DB lookups (e.g. fetching parent product options) are permitted at admission but not at pre-receive. Pre-receive handles structural/schema correctness only (required fields, valid enum values, kind/apiVersion matching) — stateless, no DB access.
- Q: At which pipeline phase is CEL expression syntax validated? → A: Admission phase. Invoking the CEL engine at pre-receive is too heavyweight; pre-receive only checks structural correctness. CEL syntax validation happens in the admission pipeline alongside other DB-backed checks.
- Q: Does the inventory `managed` flag live at `spec.manageInventory` (top-level) or `spec.inventory.managed` (nested)? → A: `spec.inventory.managed`, nested inside `InventoryDefinition` alongside `policy` and `stockLocationRefs`. The top-level `manageInventory` field mentioned in GH#83's scope list is leftover brainstorming text, not authoritative.
- Q: When a price entry has `validFromTime` after `validUntilTime`, how should it be handled? → A: Reject at pre-receive with a validation error identifying the offending price entry. This is a stateless structural check (comparing two fields on the same object) — squarely in pre-receive's remit.
- Q: When `spec.inventory.managed: false`, what happens to `stockLocationRefs` if provided? → A: Stored as-is, silently inactive at runtime. Rejecting non-empty refs would complicate operators toggling `managed` on/off; refs are kept to avoid forcing repeated edits.
- Q: Should duplicate `selectedOptions` combinations (same parent product + same option set) be allowed? → A: Rejected at admission. Two variants with identical option combinations for the same product create an ambiguous lookup; the combination MUST be unique per parent product within the namespace.
- Q: When a `ProductVariant` and its parent `Product` are pushed in the same commit, how does `productRef` resolution behave? → A: The push is accepted. Variants whose `productRef` cannot be resolved at admission time (because the product is co-pushed and not yet admitted) are deferred to a control-loop reconciliation pass. This preserves the git-push productivity model — an entire catalog, including new products and their variants, can be committed and pushed in a single pass without requiring multiple sequential pushes.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Author and push a ProductVariant document (Priority: P1)

A store operator authors a `ProductVariant` Markdown file with a `kind: ProductVariant` frontmatter envelope, commits it to the catalog repository, and pushes. The system validates the document and admits it as a `ProductVariant` resource, making it queryable by name.

**Why this priority**: This is the foundational capability — no other variant behaviour is meaningful unless a variant can be authored, validated, and persisted. It unblocks all downstream query and pricing flows.

**Independent Test**: Push a single valid `ProductVariant` document (with `metadata.name`, `spec.title`, `spec.sku`, `spec.productRef`, `spec.selectedOptions`) and verify it appears in the `productVariant` GraphQL query with all authored fields preserved.

**Acceptance Scenarios**:

1. **Given** a valid `ProductVariant` document with `kind: ProductVariant`, `metadata.name`, `spec.title`, `spec.sku`, and a `spec.productRef` pointing to an existing product, **When** it is pushed to the catalog repository, **Then** the system admits the resource and it is retrievable by name with all authored fields preserved.
2. **Given** a `ProductVariant` document missing `spec.sku`, **When** it is pushed, **Then** the push is rejected with a descriptive validation error referencing the missing field.
3. **Given** a `ProductVariant` document whose `kind` field is misspelled or set to an unrecognised value, **When** pushed, **Then** the document is rejected at admission time.
4. **Given** two `ProductVariant` documents in the same namespace with identical `spec.sku` values, **When** the second is pushed, **Then** it is rejected with an error stating the SKU is already in use.

---

### User Story 2 — Query a ProductVariant and its resolved status (Priority: P1)

A storefront developer queries the GraphQL API for a `ProductVariant` by name. The response includes metadata, spec (title, SKU, productRef, pricing, inventory, selectedOptions, media), and a resolved status that surfaces the compiled price set, resolved product reference, available inventory, and selected-options hash.

**Why this priority**: Without queryability the authored resource has no consumer-facing value; storefront rendering and checkout pricing both depend on the resolved status.

**Independent Test**: Query `productVariant(by: {namespacePath: {namespace: "...", name: "..."}})` and assert `spec.title`, `spec.sku`, `status.resolved.product.name`, `status.resolved.priceSet.priceCount`, and `status.resolved.inventory.availableQuantity` are present and correct.

**Acceptance Scenarios**:

1. **Given** a successfully admitted `ProductVariant`, **When** queried via GraphQL, **Then** `spec`, `metadata`, and `status.resolved` are returned with correct values.
2. **Given** a `ProductVariant` whose parent product has been removed, **When** queried, **Then** `status.conditions` contains a `ProductResolved: False` condition with a descriptive reason.
3. **Given** a `ProductVariant` with a `spec.pricing.priceSet` containing multiple price rules, **When** queried, **Then** `status.resolved.priceSet.priceCount` equals the number of authored price entries and `status.resolved.priceSet.currencies` lists all authored currency codes.

---

### User Story 3 — Parent product link and option compatibility validation (Priority: P1)

When a `ProductVariant` is pushed, the admission control phase verifies (via datastore lookup) that `spec.productRef` resolves to an existing `Product` in the same namespace, and that every entry in `spec.selectedOptions` corresponds to an option name and value declared on that parent product. Pre-receive performs structural checks only (schema shape, required fields, valid enum values) and does not perform DB queries.

**Why this priority**: A variant that references a non-existent product or declares incompatible options would produce misleading catalog data and invalid storefront behaviour.

**Independent Test**: Push a `ProductVariant` with `spec.selectedOptions` that includes an option name not present on the referenced parent product, and verify the push is rejected at admission time with a message identifying the incompatible option.

**Acceptance Scenarios**:

1. **Given** a `ProductVariant` with a `spec.productRef.name` that does not match any product in the namespace, **When** pushed, **Then** the push is rejected with an error indicating the parent product cannot be found.
2. **Given** a `ProductVariant` with `spec.selectedOptions` containing an option name not declared in the parent product's `spec.options`, **When** pushed, **Then** the push is rejected with an error identifying the unrecognised option name.
3. **Given** a `ProductVariant` with `spec.selectedOptions` where a value does not appear in the parent product's declared values for that option, **When** pushed, **Then** the push is rejected with an error identifying the invalid option value.
4. **Given** a `ProductVariant` whose `spec.selectedOptions` exactly match option names and values from the parent product, **When** pushed, **Then** it is admitted without errors.
5. **Given** a `ProductVariant` and its parent `Product` are committed and pushed together in the same commit, **When** the push is processed, **Then** both resources are admitted; the variant's `ProductResolved` condition is initially `False` and transitions to `True` after the control-loop reconciliation pass resolves the `productRef`.

---

### User Story 4 — Pricing and inventory schema validation (Priority: P2)

An operator configures pricing rules with CEL-based eligibility expressions and inventory controls on a variant. CEL expression syntax is validated during the admission control phase (not pre-receive); the system records compiled pricing metadata in the resolved status.

**Why this priority**: Incorrect pricing data reaching the storefront has direct revenue and legal implications; catching errors at admission prevents silent mis-pricing.

**Independent Test**: Push a `ProductVariant` with a `priceSet` containing an invalid CEL expression and verify the push is rejected at admission time with a descriptive error. Then push a valid variant and verify `status.resolved.priceSet.compiledExpressions` reflects the total number of compiled constraint expressions.

**Acceptance Scenarios**:

1. **Given** a `ProductVariant` with a `spec.pricing.priceSet.prices` entry whose `eligibility.constraints` contains a syntactically invalid CEL expression, **When** pushed, **Then** the push is rejected at admission time with an error indicating which expression failed to compile.
2. **Given** a `ProductVariant` with `spec.inventory.managed: true` and no `stockLocationRefs`, **When** pushed, **Then** the push is admitted (empty `stockLocationRefs` is allowed; location assignment may happen later).
3. **Given** a `ProductVariant` with `spec.inventory.policy` set to a value other than `deny` or `backorder`, **When** pushed, **Then** the push is rejected with a validation error listing the allowed values.
4. **Given** a `ProductVariant` with a valid `priceSet` containing mixed priorities and eligibility rules, **When** admitted, **Then** `status.resolved.priceSet.strategies` lists all unique strategy types and `status.resolved.priceSet.priceCount` matches the authored count.

---

### User Story 5 — Update a ProductVariant (Priority: P2)

An operator modifies an existing `ProductVariant` document (changes pricing, inventory settings, or title), pushes the update, and the system re-validates and updates the resolved status accordingly.

**Why this priority**: Variants change over time (seasonal pricing, stock policy changes); the system must handle lifecycle updates without requiring a delete-and-recreate.

**Independent Test**: Push a `ProductVariant`, then push an update changing `spec.pricing.priceSet.prices` to add a new rule, and verify `status.resolved.priceSet.priceCount` increases by one.

**Acceptance Scenarios**:

1. **Given** an existing `ProductVariant`, **When** the operator changes `spec.pricing` and pushes, **Then** `status.resolved.priceSet` is updated to reflect the new pricing rules.
2. **Given** an existing `ProductVariant`, **When** `spec.inventory.policy` is changed to a valid value and pushed, **Then** `status.resolved.inventory.policy` reflects the new value.
3. **Given** an existing `ProductVariant`, **When** `spec.selectedOptions` is updated to include an option not on the parent product and pushed, **Then** the push is rejected and the stored variant remains unchanged.

---

### Edge Cases

- When a `ProductVariant` and its parent `Product` are co-pushed in the same commit, the push is accepted; the variant's `productRef` resolution is deferred to the control-loop reconciliation pass. The `ProductResolved` condition will be `False` until reconciliation completes. This supports the single-pass catalog authoring model.
- A `spec.pricing.priceSet.prices` entry with `validFromTime` after `validUntilTime` is rejected at pre-receive; this is a stateless structural check requiring no DB access.
- What is returned for `status.resolved` before any reconciliation has occurred (first push in-flight)?
- Two variants sharing identical `spec.selectedOptions` for the same parent product are rejected at admission; the combination MUST be unique per parent product to prevent ambiguous option-matrix lookups.
- What happens when a `spec.pricing.priceSet.prices` entry has `quantity.min > quantity.max`?
- When `spec.inventory.managed: false`, `stockLocationRefs` are stored as-is and silently inactive at runtime; they are not rejected, allowing operators to toggle `managed` on/off without removing refs each time.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST accept a `ProductVariant` Markdown document with valid `apiVersion: catalog.gitstore.dev/v1beta1`, `kind: ProductVariant`, `metadata.name`, `metadata.namespace`, `spec.title`, `spec.sku`, and `spec.productRef` via git push.
- **FR-002**: The pre-receive hook MUST reject a `ProductVariant` document missing any of `spec.title`, `spec.sku`, or `spec.productRef` with a descriptive error referencing the missing field. Pre-receive performs structural/schema checks only — no datastore access.
- **FR-003**: The pre-receive hook MUST validate `spec.inventory.policy`, when present, is one of `deny` or `backorder`; other values MUST be rejected at this stage.
- **FR-004**: The pre-receive hook MUST validate `spec.pricing.priceSet.prices[*].strategy.type` is a recognised value; unrecognised strategy types MUST be rejected at this stage.
- **FR-004a**: The pre-receive hook MUST reject any price entry where `validFromTime` is set to a value after `validUntilTime`, with an error identifying the offending entry.
- **FR-005**: The admission control phase MUST enforce uniqueness of `spec.sku` across all `ProductVariant` resources in the same namespace via a datastore lookup; a push introducing a duplicate SKU MUST be rejected at admission.
- **FR-006**: The admission control phase MUST attempt to validate `spec.productRef.name` against the datastore. If the referenced `Product` exists, the variant is admitted with `ProductResolved: True`. If the product does not yet exist (e.g. co-pushed in the same commit and not yet admitted), the variant MUST still be admitted and the `ProductResolved` condition set to `False`; a control-loop reconciliation pass resolves the reference once the product is available.
- **FR-007**: The admission control phase MUST validate that every entry in `spec.selectedOptions` has a `name` matching an option declared in the parent product's `spec.options` and a `value` appearing in that option's declared values list, using a datastore lookup of the parent product; violations MUST cause rejection at admission.
- **FR-007a**: The admission control phase MUST enforce that the `spec.selectedOptions` combination is unique per parent product within the namespace; a variant whose option set duplicates an existing variant's for the same product MUST be rejected at admission.
- **FR-008**: The admission control phase MUST validate CEL expression syntax in `spec.pricing.priceSet.prices[*].eligibility.constraints[*].expression` by invoking the CEL parser; syntactically invalid expressions MUST cause rejection at admission with the offending expression identified.
- **FR-009**: System MUST persist `spec.pricing`, `spec.inventory`, `spec.selectedOptions`, and `spec.media` without modification.
- **FR-010**: System MUST expose a `productVariant(by: ...)` GraphQL query supporting lookup by namespace + name and by globally unique ID.
- **FR-011**: System MUST expose a paginated `productVariants` GraphQL listing scoped to a namespace, using cursor-based pagination.
- **FR-012**: System MUST write `status.conditions` entries for `AdmissionAccepted`, `ProductResolved`, `OptionsAccepted`, `PricingAccepted`, and `Ready` on each reconciliation cycle.
- **FR-013**: System MUST populate `status.resolved` with: resolved parent product reference (`product.name`, `product.uid`), `selectedOptionsHash`, and `priceSet` summary (`name`, `hash`, `compiledExpressions`, `priceCount`, `currencies`, `strategies`), `inventory` summary, and `media` with resolved URLs after each successful reconciliation.
- **FR-014**: System MUST support markdown body content in a `ProductVariant` document as variant-specific descriptive copy; body content MUST be stored and returned via the GraphQL API.
- **FR-015**: System MUST expose `productVariants` as a paginated connection on the parent `Product` GraphQL type, allowing traversal of all variants belonging to a product.

### Key Entities

- **ProductVariant**: A named, namespace-scoped catalog resource representing a purchasable SKU. Carries `metadata`, `spec` (title, sku, productRef, inventory, pricing, selectedOptions, media), and `status` (conditions, resolved summary). The atomic sellable unit; `Product` is the non-sellable parent descriptor.
- **ProductVariantSpec**: Author-defined attributes of the variant: display title, unique SKU, parent product reference, inventory controls, pricing price-set, selected option combinations, and media attachments.
- **PricingDefinition / PriceSet**: A named set of `PriceTemplate` entries, each defining a currency amount, optional quantity range, validity time window, priority, pricing strategy, and CEL-based eligibility constraints.
- **InventoryDefinition**: Controls whether inventory is managed (`managed: bool`), the out-of-stock policy (`deny` or `backorder`), and optional references to named stock locations.
- **SelectedOptionDefinition**: A single option choice (name + value) that identifies this variant within the parent product's option matrix (e.g. `color: silver`, `size: 16`).
- **ProductVariantStatus**: The full status envelope including `conditions`, `observedGeneration`, `lastAppliedRevision`, and `resolved` (computed summary of parent product, options hash, compiled pricing, and inventory state).
- **ResolvedProductVariantDefinition**: Computed at reconciliation time — parent product identity, `selectedOptionsHash`, price-set compilation summary, resolved inventory, and resolved media with URLs.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A valid `ProductVariant` document pushed to the catalog repository is admitted and queryable within the same request-response cycle as the push acknowledgement.
- **SC-002**: Invalid `ProductVariant` documents (missing required fields, duplicate SKU, bad inventory policy, invalid CEL expression) are rejected at push time in 100% of cases with a human-readable error message identifying the violation.
- **SC-003**: `spec.selectedOptions` compatibility is validated against the parent product in 100% of pushes; mismatched option names or values are rejected before any resource is persisted.
- **SC-004**: The `productVariants` listing query returns consistent, non-duplicated results across all pages for a namespace containing at least 500 variants within 2 seconds.
- **SC-005**: `status.resolved.priceSet` is populated after admission for all variants with a non-empty `spec.pricing.priceSet`, with correct `priceCount`, `currencies`, and `strategies` values verified by automated tests.
- **SC-006**: All condition types (`AdmissionAccepted`, `ProductResolved`, `OptionsAccepted`, `PricingAccepted`, `Ready`) are covered by automated integration tests for both success and failure paths.

## Assumptions

- `Product` is the non-sellable parent descriptor; `ProductVariant` is the purchasable unit. Price, inventory, and SKU live exclusively at the variant level.
- `spec.productRef` always references a `Product` in the same namespace; cross-namespace parent references are out of scope.
- The push pipeline has two validation phases: (1) **pre-receive** — stateless structural checks (required fields, valid enums, kind/apiVersion); (2) **admission control** — DB-backed semantic checks (SKU uniqueness, `productRef` resolution, option compatibility, CEL syntax validation). Pre-receive never performs DB lookups.
- A `ProductVariant` whose `productRef` cannot be resolved at admission time (e.g. the parent `Product` is co-pushed in the same commit) is admitted with `ProductResolved: False`; the control loop reconciles the reference once the product is available. This enables single-pass catalog authoring — an entire catalog of products and variants can be pushed in one commit without requiring sequential pushes.
- CEL expression syntax is validated at admission using the CEL parser (syntax check only); runtime CEL evaluation (e.g. `region.code == 'EU'`) is out of scope for this feature.
- `stockLocationRefs` are stored and returned as-is regardless of `spec.inventory.managed`; they are silently inactive when `managed: false`. Resolution of referenced `StockLocation` resources is out of scope.
- `status.resolved.inventory.availableQuantity` is populated by an external inventory system or reconciler; this feature defines the field but not the reconciliation source.
- Automatic generation of all option-combination variants from a product's option matrix is out of scope.
- Variant pricing engine implementation (runtime price selection) is out of scope.
- Inventory reservation workflows are out of scope.
- `spec.pricing.priceSet.prices[*].quantity.min` defaults to 1 when omitted; `quantity.max` is unbounded when omitted.
- The `spec.selectedOptions` combination MUST be unique per parent product within the namespace; duplicate combinations are rejected at admission to prevent ambiguous option-matrix lookups.

## Dependencies

- GH#83: Parent ProductVariant initiative (schema, scope, and acceptance criteria)
- GH#208: ProductVariant Resource Contract baseline (blocked by this spec)
- GH#209: Parent Link and Option Compatibility Validation (blocked by #208)
- GH#210: Pricing and Inventory Schema Validation (blocked by #208)
- GH#211: Integration Tests and Documentation (blocked by #209, #210)
- GH#40: ObjectMeta, Condition, and ObjectReference contracts (shared envelope types)
- Spec 022 (`022-collection-resource-contract`): Kubernetes-style resource envelope pattern
- Spec 021 (`021-category-taxonomy`): Established `ParseResource` multi-kind dispatcher and intra-push validation patterns

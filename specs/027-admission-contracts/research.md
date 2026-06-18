# Research: Admission Control Contract (spec 027)

## 1. Existing Admission Helper Inventory

### Functions migrating to `admission/catalog/`

| Function | Current location | Signature | Moves to |
|---|---|---|---|
| `validateSelectedOptions` | `cataloggrpc/server.go` (unexported) | `(selected []catalog.SelectedOptionDefinition, parentSpec []byte) (bool, string)` | `admission/catalog/product_variant_policy.go` (exported) |
| `celValidateExpressions` | `cataloggrpc/server.go` (unexported) | `(env *cel.Env, spec catalog.ProductVariantSpec) (bool, string)` | `admission/catalog/product_variant_policy.go` |
| `detectCycles` | `cataloggrpc/server.go` (unexported) | `(parentMap map[string]string) map[string]bool` | `admission/catalog/category_taxonomy_policy.go` |
| `topoSortCategories` | `cataloggrpc/server.go` (unexported) | `(parentMap map[string]string, cycleMembers map[string]bool) []string` | `admission/catalog/category_taxonomy_policy.go` |

### Functions staying in `cataloggrpc/server.go`

| Function | Reason |
|---|---|
| `variantAdmissionStatus` | Status JSON builder — storage handler concern |
| `categoryAdmissionStatusFull` | Status JSON builder — storage handler concern |
| `admissionAcceptedStatus` | Status JSON builder — storage handler concern |
| `computeResolvedPriceSet` | Derives resolved summary fields stored in status — storage handler concern |
| `computeResolvedInventory` | Derives resolved summary fields stored in status — storage handler concern |

### Structs migrating

| Struct | Current location | Decision |
|---|---|---|
| `variantAdmitResult` | `cataloggrpc/server.go` | **Stays in `cataloggrpc`**; the policy returns conditions, the handler maps them to `variantAdmitResult` for status building |

---

## 2. Test Coverage Gap

`validateSelectedOptions` is tested in the internal package test (`server_variant_test.go`). After migration to `admission/catalog/`, it becomes exported and must be tested in the new package's own unit tests. Moving the tests is straightforward.

`celValidateExpressions` has **no existing tests**. New unit tests must be written in `admission/catalog/` alongside the migration.

`admitProductVariant` end-to-end has **no existing integration test coverage** in `server_test.go`. Integration tests for the post-migration path must be added.

---

## 3. Condition Return Design

**Decision**: `Allowed` carries `Conditions []AdmissionCondition`.

**Rationale**: GitStore's post-receive admission is non-blocking — nothing is ever hard-denied by semantic checks. Policies surface check results as named conditions rather than as pass/fail rejections. The storage handler reads `Conditions` from the `Allowed` decision and maps them to the existing status JSON format. This design is backwards-compatible with the current condition names (`ProductResolved`, `OptionsAccepted`, `PricingAccepted`, `ParentResolved`, `Acyclic`).

**Alternatives considered**:
- *Typed return structs per policy*: Would require the chain to know concrete policy types — breaks the sealed-interface design.
- *Side-channel context key*: Fragile, untestable.
- *Mutation patches encoding conditions*: Overloads the mutating phase for a validating concern.

```go
type AdmissionCondition struct {
    Type    string // condition name, e.g. "ProductResolved"
    Status  bool   // true = condition satisfied
    Reason  string // optional machine-readable reason code
    Message string // optional human-readable detail
}
```

---

## 4. CategoryTaxonomy Policy Design

**Decision**: The `CategoryTaxonomyValidatingPolicy` receives the full `PushSet` and independently computes cycle membership and ancestor path for the target resource. The `detectCycles` and `topoSortCategories` helpers move to the new package. The pre-processing loop in `AdmitResources` still computes topo order for admission ordering, but the cycle/parent-resolution logic is delegated to the policy.

**Rationale**: The `inPushAncestorPaths` accumulator that was threaded through the topo-ordered admit loop encoded the sequential nature of the computation. By giving the policy the full `PushSet`, it can reconstruct the full ancestor chain for any resource in a single call. Datastore access for out-of-push parents is injected via the policy constructor.

**Alternatives considered**:
- *Pass topology facts via `AdmissionRequest` extra fields*: Couples the generic `AdmissionRequest` type to taxonomy-specific graph concepts.
- *Pre-compute and annotate each entry before the chain call*: Moves the computation back into `AdmitResources`, defeating the goal of policy encapsulation.

---

## 5. `ValidationContext` Disposition

**Decision**: `ValidationContext` (defined in `cataloggrpc/context.go`, currently unused in production code) is **deprecated** and will be removed. Its concerns are absorbed by `AdmissionRequest` (which carries `Namespace` and `RepositoryID` via `GitAdmissionContext`). No production code depends on it.

---

## 6. `CommitFile`/`DeleteFile` Trigger Point

**Decision**: The `TriggerCommitFile` constant is defined in `admission/admission.go`. The natural hook point is the `CommitFile` and `DeleteFile` methods on the `gitclient.Client` (or on the `GitWriter` interface in the service layer). No code is wired in this spec. The constant exists only to ensure the `AdmissionRequest.Trigger` field is forward-compatible without a breaking change later.

---

## 7. Package Structure

**Decision**: Two packages:
- `gitstore-api/internal/admission/` — core types and chain (no resource-specific code)
- `gitstore-api/internal/admission/catalog/` — concrete `ValidatingAdmissionPolicy` implementations for catalog resource kinds

**Rationale**: Separates the generic framework (reusable for any resource type or trigger) from the catalog-specific business logic. The `cataloggrpc` package imports `admission/catalog/`; the `admission` package itself has no dependency on catalog types.

---

## 8. Execution Phase Order

The Kubernetes admission controller phase order applied to GitStore's post-receive, non-blocking context:

1. **Mutating policies** (built-in, registered order) — may return patches
2. **Mutating webhooks** (external, registered order) — may return patches
3. **Validating policies** (built-in, registered order) — return conditions in `Allowed`
4. **Validating webhooks** (external, registered order) — return conditions or deny

A `Denied` from any step short-circuits the remainder. In the current post-receive context, no policy actually issues a hard `Denied` — all existing checks surface as `False` conditions in an `Allowed` result. The `Denied` path is fully defined for future use and for pre-receive extension.

---

## 9. Constitution Compliance

| Principle | Status |
|---|---|
| I. Test-First | New packages require unit tests before implementation |
| II. API-First | Contracts defined in this spec before any implementation |
| VII. Simplicity | Framework replaces inline code rather than adding layers on top |

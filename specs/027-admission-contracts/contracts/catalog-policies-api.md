# Contract: Catalog Admission Policies API

**Package**: `github.com/gitstore-dev/gitstore/api/internal/admission/catalog`  
**Stability**: Stable

## Overview

Concrete `ValidatingAdmissionPolicy` implementations for catalog resource kinds. These are the migrated forms of the inline semantic checks previously embedded in `cataloggrpc/server.go`.

No mutating policies are implemented in spec 027. The interfaces are defined in the `admission` package.

---

## ProductVariantValidatingPolicy

Validates a `ProductVariant` resource against its parent `Product` and pricing rules.

```go
// ProductVariantValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "ProductVariant".
type ProductVariantValidatingPolicy struct { /* unexported fields */ }

// NewProductVariantValidatingPolicy constructs the policy.
// celEnv may be nil; CEL validation is skipped when nil.
func NewProductVariantValidatingPolicy(
    store datastore.Datastore,
    celEnv *cel.Env,
    log *zap.Logger,
) *ProductVariantValidatingPolicy

func (p *ProductVariantValidatingPolicy) Name() string // returns "ProductVariantValidatingPolicy"

// Validate checks the ProductVariant resource and returns Allowed with conditions.
// Never returns Denied — all check results are surfaced as False conditions.
func (p *ProductVariantValidatingPolicy) Validate(
    ctx context.Context,
    req admission.AdmissionRequest,
) admission.AdmissionDecision
```

### Conditions emitted

| Condition (`Type`) | `Status` true when |
|---|---|
| `ProductResolved` | Parent product found in datastore by `spec.productRef.name` |
| `OptionsAccepted` | All `spec.selectedOptions` entries are compatible with parent product option definitions |
| `PricingAccepted` | All CEL expressions in `spec.pricing.priceSet.prices[*].eligibility.constraints[*].expression` parse without error |

### Exported helper functions

These were previously unexported in `cataloggrpc/server.go`. They are exported here so they can be unit-tested independently.

```go
// ValidateSelectedOptions checks that all selected options are compatible with
// the parent product's declared options.
// parentSpec is the raw JSON of the parent product's spec field from the datastore.
// Returns (true, "") on success or (false, reason) on first incompatibility.
// If parentSpec cannot be unmarshalled, returns (true, "") — skip rather than false-reject.
func ValidateSelectedOptions(
    selected []catalog.SelectedOptionDefinition,
    parentSpec []byte,
) (ok bool, reason string)

// ValidateCELExpressions checks CEL expression syntax in a ProductVariant pricing spec.
// env may be nil (skip validation). Returns (true, "") on success or (false, reason) on
// first parse error. reason identifies the expression field path and the parse error.
func ValidateCELExpressions(
    env *cel.Env,
    spec catalog.ProductVariantSpec,
) (ok bool, reason string)
```

---

## CategoryTaxonomyValidatingPolicy

Validates a `CategoryTaxonomy` resource for parent resolution and intra-push cycle membership.

```go
// CategoryTaxonomyValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "CategoryTaxonomy".
type CategoryTaxonomyValidatingPolicy struct { /* unexported fields */ }

// NewCategoryTaxonomyValidatingPolicy constructs the policy.
func NewCategoryTaxonomyValidatingPolicy(
    store datastore.Datastore,
    log *zap.Logger,
) *CategoryTaxonomyValidatingPolicy

func (p *CategoryTaxonomyValidatingPolicy) Name() string // returns "CategoryTaxonomyValidatingPolicy"

// Validate checks the CategoryTaxonomy resource and returns Allowed with conditions.
// Never returns Denied — all check results are surfaced as False conditions.
// Uses req.PushSet to resolve in-push parents and detect cycles.
func (p *CategoryTaxonomyValidatingPolicy) Validate(
    ctx context.Context,
    req admission.AdmissionRequest,
) admission.AdmissionDecision
```

### Conditions emitted

| Condition (`Type`) | `Status` true when |
|---|---|
| `ParentResolved` | `spec.parentRef.name` found in datastore or in `req.PushSet` (or resource is a root with no `parentRef`) |
| `Acyclic` | Resource is not part of an intra-push reference cycle |

### Exported helper functions

```go
// DetectCycles performs a three-color DFS over a parent-map and returns the set
// of category names that participate in at least one cycle.
// parentMap maps category name → parent name ("" for roots).
func DetectCycles(parentMap map[string]string) map[string]bool

// TopoSortCategories returns a topological ordering of categories in parentMap,
// with roots first and cycle members appended at the end.
// Cycle members are identified by cycleMembers (output of DetectCycles).
func TopoSortCategories(
    parentMap map[string]string,
    cycleMembers map[string]bool,
) []string
```

---

---

## ProductValidatingPolicy

Stub policy for `Kind == "Product"`. No semantic checks in spec 027 — placeholder for future rules (e.g., variant count limits, option set validation).

```go
// ProductValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "Product". Returns Allowed immediately; no checks in spec 027.
type ProductValidatingPolicy struct { /* unexported fields */ }

func NewProductValidatingPolicy(log *zap.Logger) *ProductValidatingPolicy

func (p *ProductValidatingPolicy) Name() string // returns "ProductValidatingPolicy"

func (p *ProductValidatingPolicy) Validate(
    _ context.Context,
    req admission.AdmissionRequest,
) admission.AdmissionDecision // always returns DecisionAllow()
```

---

## CollectionValidatingPolicy

Stub policy for `Kind == "Collection"`. No semantic checks in spec 027 — placeholder for future rules (e.g., targetRef resolution, match-expression validation beyond schema).

```go
// CollectionValidatingPolicy implements admission.ValidatingAdmissionPolicy
// for Kind == "Collection". Returns Allowed immediately; no checks in spec 027.
type CollectionValidatingPolicy struct { /* unexported fields */ }

func NewCollectionValidatingPolicy(log *zap.Logger) *CollectionValidatingPolicy

func (p *CollectionValidatingPolicy) Name() string // returns "CollectionValidatingPolicy"

func (p *CollectionValidatingPolicy) Validate(
    _ context.Context,
    req admission.AdmissionRequest,
) admission.AdmissionDecision // always returns DecisionAllow()
```

---

## Chain registration (wiring)

In `cataloggrpc/server.go`, all four policies are registered in `NewServer`:

```go
chain := admission.NewChain(deps.Logger)
chain.RegisterValidatingPolicy(catalog.NewProductValidatingPolicy(deps.Logger))
chain.RegisterValidatingPolicy(catalog.NewCollectionValidatingPolicy(deps.Logger))
chain.RegisterValidatingPolicy(
    catalog.NewProductVariantValidatingPolicy(deps.Store, celEnv, deps.Logger),
)
chain.RegisterValidatingPolicy(
    catalog.NewCategoryTaxonomyValidatingPolicy(deps.Store, deps.Logger),
)
```

The chain is stored on `Server.chain` and called from each `admit*` method.

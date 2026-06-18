# Quickstart: Admission Control Contract (spec 027)

## What changes and why

This spec introduces a first-class admission framework. The semantic checks that were previously embedded in `cataloggrpc/server.go` (parent product lookup, option compatibility, CEL syntax, cycle detection) are extracted into standalone `ValidatingAdmissionPolicy` implementations.

**From the caller's perspective, nothing changes**: the git push path, the GraphQL read API, and the datastore status conditions are identical after this migration.

---

## Adding a new ValidatingAdmissionPolicy

1. Create a struct implementing `admission.ValidatingAdmissionPolicy` in `gitstore-api/internal/admission/catalog/` (or a new sub-package for non-catalog resources).

```go
package catalog

type MyResourceValidatingPolicy struct {
    store datastore.Datastore
    log   *zap.Logger
}

func NewMyResourceValidatingPolicy(store datastore.Datastore, log *zap.Logger) *MyResourceValidatingPolicy {
    return &MyResourceValidatingPolicy{store: store, log: log}
}

func (p *MyResourceValidatingPolicy) Name() string { return "MyResourceValidatingPolicy" }

func (p *MyResourceValidatingPolicy) Validate(ctx context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
    if req.Kind != "MyResource" {
        return admission.DecisionAllow()
    }
    resource, ok := req.Object.(*catalog.MyResource)
    if !ok {
        return admission.DecisionAllow()
    }
    // ... run checks, build conditions
    return admission.DecisionAllow(
        admission.AdmissionCondition{Type: "MyCheck", Status: checkPassed},
    )
}
```

2. Register the policy in `NewServer` in `gitstore-api/internal/cataloggrpc/server.go`:

```go
chain.RegisterValidatingPolicy(
    admissioncatalog.NewMyResourceValidatingPolicy(deps.Store, deps.Logger),
)
```

3. In the `admitMyResource` handler, call the chain and read back conditions:

```go
req := admission.AdmissionRequest{
    Object:    resource,
    Kind:      resource.Kind,
    Name:      resource.Metadata.Name,
    Namespace: namespace,
    Operation: admission.OperationCreate, // or OperationUpdate
    Trigger:   admission.TriggerGitPush,
    GitContext: &admission.GitAdmissionContext{
        RepositoryID: admCtx.RepositoryID,
        CommitSHA:    admCtx.CommitSHA,
        RefName:      admCtx.RefName,
        Revision:     admCtx.Revision,
    },
}
if allowed, ok := s.chain.Admit(ctx, req).(admission.Allowed); ok {
    for _, c := range allowed.Conditions {
        // map c.Type and c.Status into your status builder
    }
}
```

---

## Running tests

```bash
# All admission package tests
cd gitstore-api && go test ./internal/admission/... -v

# All admission/catalog package tests
cd gitstore-api && go test ./internal/admission/catalog/... -v

# Full cataloggrpc integration tests (verifies migration did not break anything)
cd gitstore-api && go test ./internal/cataloggrpc/... -v

# Full test suite
make test
```

---

## Verifying the end-to-end path

```bash
# Start the full stack
make dev

# In a second terminal, push a ProductVariant with a bad CEL expression
# (adjust NAMESPACE and path to match your bootstrap)
cat > /tmp/bad-variant.md <<'EOF'
---
apiVersion: commerce.gitstore.io/v1
kind: ProductVariant
metadata:
  name: bad-variant
spec:
  sku: BAD-001
  pricing:
    priceSet:
      prices:
        - currencyCode: USD
          strategy:
            type: fixed
          eligibility:
            constraints:
              - name: bad
                expression: "this is not valid CEL {"
---
EOF
# push via git (assumes local repo is set up pointing at dev server)
git add /tmp/bad-variant.md && git commit -m "test: bad CEL" && git push

# Query the variant status via GraphQL
# Expected: PricingAccepted: false with reason "InvalidCELExpression"
```

---

## File map

```text
gitstore-api/internal/admission/
├── admission.go          # AdmissionRequest, AdmissionDecision, Operation, Trigger, GitAdmissionContext, AdmissionCondition
├── interfaces.go         # MutatingAdmissionPolicy, ValidatingAdmissionPolicy, MutatingAdmissionWebhook, ValidatingAdmissionWebhook
├── chain.go              # Chain, NewChain, Register*, Admit
└── catalog/
    ├── product_policy.go               # ProductValidatingPolicy (stub)
    ├── collection_policy.go            # CollectionValidatingPolicy (stub)
    ├── product_variant_policy.go       # ProductVariantValidatingPolicy, ValidateSelectedOptions, ValidateCELExpressions
    └── category_taxonomy_policy.go     # CategoryTaxonomyValidatingPolicy, DetectCycles, TopoSortCategories

gitstore-api/internal/cataloggrpc/
├── server.go             # Modified: chain field on Server, ExtraValidatingPolicies in ServerDeps, admit* methods delegate to chain
└── context.go            # ValidationContext removed; AdmissionContext stays
```

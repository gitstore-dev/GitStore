# Implementation Plan: Admission Control Contract

**Branch**: `027-admission-contracts` | **Date**: 2026-06-18 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/027-admission-contracts/spec.md`

## Summary

Define a first-class admission control framework (Mutating → Validating, Policies → Webhooks) for GitStore's resource lifecycle, then migrate the existing inline semantic checks for `ProductVariant` and `CategoryTaxonomy` out of the monolithic `cataloggrpc/server.go` into independently testable `ValidatingAdmissionPolicy` implementations. The chain is orchestrated through a new `gitstore-api/internal/admission/` package; concrete catalog policies live in `gitstore-api/internal/admission/catalog/`. No proto changes, no Rust changes, no new datastore schema.

## Technical Context

**Language/Version**: Go 1.25 (gitstore-api)
**Primary Dependencies**: `go.uber.org/zap`, `github.com/google/cel-go/cel`, `github.com/go-playground/validator/v10`, `encoding/json`
**Storage**: None (in-process Go types only; no new datastore tables)
**Testing**: `go test ./...` (package-level unit tests + existing `cataloggrpc` integration tests)
**Target Platform**: Linux server (gitstore-api)
**Project Type**: Internal library within the gitstore-api service
**Performance Goals**: Chain overhead must be negligible for post-receive fire-and-forget processing (no latency SLA added)
**Constraints**: Condition names and status JSON format must remain identical to pre-migration output (no consumer breakage)
**Scale/Scope**: 4 registered policies at launch (ProductVariant, CategoryTaxonomy); framework supports unbounded additions

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Test-First | PASS | Unit tests written before policy implementations; existing cataloggrpc integration tests serve as red gate for migration correctness |
| II. API-First | PASS | Contracts defined in `contracts/` before any implementation code is written |
| III. Clear Contracts | PASS | `AdmissionDecision` sealed interface, condition types match existing `catalog.ConditionType` constants |
| IV. Observability | PASS | Policy panics logged with stack trace; all chain decisions logged at appropriate level via zap |
| V. User Story Driven | PASS | All 4 user stories map to FR-001–FR-017 |
| VI. Incremental Delivery | PASS | P1 (core types + condition feedback) deliverable independently of P3/P4 webhook extension points |
| VII. Simplicity | PASS | Framework replaces inline code (net code reduction in `cataloggrpc/server.go`); no speculative abstractions |

## Project Structure

### Documentation (this feature)

```text
specs/027-admission-contracts/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   ├── admission-chain-api.md      # Chain + extension-point interfaces
│   └── catalog-policies-api.md    # ProductVariant + CategoryTaxonomy policies
└── tasks.md             # Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-api/internal/admission/        # NEW package
├── admission.go                        # AdmissionRequest, AdmissionDecision, Operation, Trigger, etc.
├── interfaces.go                       # Extension-point interfaces
├── chain.go                            # Chain orchestrator
└── catalog/                            # NEW sub-package
    ├── product_policy.go               # ProductValidatingPolicy (stub; no checks in spec 027)
    ├── collection_policy.go            # CollectionValidatingPolicy (stub; no checks in spec 027)
    ├── product_variant_policy.go       # ProductVariantValidatingPolicy + exported helpers
    └── category_taxonomy_policy.go     # CategoryTaxonomyValidatingPolicy + exported helpers

gitstore-api/internal/cataloggrpc/
├── server.go                           # MODIFIED: chain field, wired in NewServer, admit* slimmed
└── context.go                          # MODIFIED: ValidationContext removed
```

## Phase 0: Research

Research complete. See [research.md](research.md) for full findings.

**Key decisions:**
- `Allowed` carries `Conditions []AdmissionCondition` so non-blocking semantic checks surface as named status conditions without hard denial
- `validateSelectedOptions`, `celValidateExpressions`, `detectCycles`, `topoSortCategories` move to `admission/catalog/` as exported functions
- Status builder helpers (`variantAdmissionStatus`, `categoryAdmissionStatusFull`, `admissionAcceptedStatus`) stay in `cataloggrpc/server.go`
- `ValidationContext` in `context.go` is unused and will be removed
- `variantAdmitResult` stays in `cataloggrpc` — policy returns conditions, handler maps to result struct for status building

## Phase 1: Design & Contracts

### Foundation (US1 + US2)

**Task F1** — Create `gitstore-api/internal/admission/admission.go`

Core types: `Operation`, `Trigger`, `GitAdmissionContext`, `AdmissionCondition`, `AdmissionRequest`, `AdmissionDecision` (sealed: `Allowed`, `Denied`), constructor helpers `DecisionAllow`, `DecisionDeny`.

*Write unit tests first*: `admission_test.go` verifying the sealed-interface pattern, constructor helpers, and zero values.

**Task F2** — Create `gitstore-api/internal/admission/interfaces.go`

Extension-point interfaces: `MutatingAdmissionPolicy`, `ValidatingAdmissionPolicy`, `MutatingAdmissionWebhook`, `ValidatingAdmissionWebhook`.

*Write unit tests first*: compile-time interface satisfaction assertions via `var _ MutatingAdmissionPolicy = (*noopMutating)(nil)` style stubs.

**Task F3** — Create `gitstore-api/internal/admission/chain.go`

`Chain` struct, `NewChain`, four `Register*` methods, `Admit` method with phase-ordered execution, panic recovery, condition accumulation.

*Write unit tests first*: `chain_test.go` covering empty chain, single policy, denial short-circuit, panic recovery, phase order verification, condition accumulation from multiple validating policies.

### Catalog Policies (US1 + US2)

**Task C1** — Create `gitstore-api/internal/admission/catalog/product_variant_policy.go`

Move `validateSelectedOptions` → `ValidateSelectedOptions`, `celValidateExpressions` → `ValidateCELExpressions`. Implement `ProductVariantValidatingPolicy` with `Name()` and `Validate()`.

*Write unit tests first*: `product_variant_policy_test.go` covering:
- `ValidateSelectedOptions`: known name + allowed value, known name + disallowed value, unknown name, empty values list (accepts any), unparseable parent spec (skip)
- `ValidateCELExpressions`: nil env (skip), nil pricing (skip), valid expression, invalid expression with field path in reason
- `Validate`: full integration across all three conditions

*Test port*: move the three tests from `cataloggrpc/server_variant_test.go` into `admission/catalog/product_variant_policy_test.go` (adjusting for exported names).

**Task C2** — Create `gitstore-api/internal/admission/catalog/category_taxonomy_policy.go`

Move `detectCycles` → `DetectCycles`, `topoSortCategories` → `TopoSortCategories`. Implement `CategoryTaxonomyValidatingPolicy` with `Name()` and `Validate()`. Validate uses `req.PushSet` for in-push parent resolution and cycle membership.

*Write unit tests first*: `category_taxonomy_policy_test.go` covering:
- `DetectCycles`: no cycles, direct cycle A↔B, tail cycle A→B→C→B (A is not in cycle, B and C are)
- `TopoSortCategories`: roots first, cycle members at end
- `Validate`: root (no parentRef), child with in-push parent, child with datastore parent, child with missing parent (ParentResolved: false), cycle member (Acyclic: false)

### cataloggrpc Migration (US1 + US2)

**Task M1** — Add `chain *admission.Chain` to `Server`, wire in `NewServer`

Register all four policies: `ProductValidatingPolicy`, `CollectionValidatingPolicy`, `ProductVariantValidatingPolicy`, `CategoryTaxonomyValidatingPolicy`. Remove `celEnv` usage from `Server` struct (now inside the variant policy). Keep `celEnv` on `Server` only if `computeResolvedPriceSet` still needs it (check: yes, `computeResolvedPriceSet` calls `celEnv.Parse` for resolved summaries — keep field).

**Task M2** — Slim `admitProductVariant`

Replace the inline semantic check block (lines 615–652 of current `server.go`) with a call to `s.chain.Admit`. Map `Allowed.Conditions` back to the existing `variantAdmitResult` struct for status building. The upsert and status-building logic stays unchanged.

**Task M3** — Slim `admitCategoryTaxonomyWithContext`

Replace the inline cycle/parent checks with a call to `s.chain.Admit`. The batch pre-processing in `AdmitResources` (topo-sort loop) still computes admission order but delegates cycle/parent results to the policy via `PushSet`.

**Task M4** — Wire chain calls for `admitProduct` and `admitCollection`

Call `s.chain.Admit` for both kinds. The registered stub policies (`ProductValidatingPolicy`, `CollectionValidatingPolicy`) return `Allowed{}` immediately with no conditions. Ensures all four resource kinds flow through the chain infrastructure uniformly and have a named policy file ready for future checks.

**Task M5** — Remove `validateSelectedOptions` and `celValidateExpressions` from `server.go` and delete `server_variant_test.go` (tests ported to `admission/catalog/`)

**Task M6** — Remove `ValidationContext` from `cataloggrpc/context.go`

### Webhook Extension Points (US3 + US4)

Extension-point interfaces are already defined in Task F2. No implementations are needed. The `Register*` methods on `Chain` accept webhook implementations whenever a future spec wires them. Tasks F2 and F3 cover this entirely.

### Observability

All `chain.Admit` calls are logged by the chain itself on policy error or panic. The `admit*` handlers continue their existing `Info`/`Warn`/`Error` logging for datastore operations. No additional instrumentation is required.

## Complexity Tracking

No constitution violations. The framework reduces complexity in `cataloggrpc/server.go` by extracting inline logic into independently testable units.

## Verification Checklist

- [ ] `go test ./internal/admission/...` passes (new package unit tests)
- [ ] `go test ./internal/admission/catalog/...` passes (policy unit tests)
- [ ] `go test ./internal/cataloggrpc/...` passes unchanged (migration correctness gate)
- [ ] `make test` passes (full suite)
- [ ] `make lint` passes
- [ ] `make pr-ready` green
- [ ] End-to-end: push `ProductVariant` with bad CEL → status has `PricingAccepted: false`
- [ ] End-to-end: push cyclic `CategoryTaxonomy` pair → status has `Acyclic: false`
- [ ] End-to-end: push clean `ProductVariant` with existing parent → status has `AdmissionAccepted: true`, `ProductResolved: true`, `OptionsAccepted: true`, `PricingAccepted: true`

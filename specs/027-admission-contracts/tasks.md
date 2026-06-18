# Tasks: Admission Control Contract

**Input**: Design documents from `/specs/027-admission-contracts/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation code; every test must FAIL before the corresponding implementation task begins.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)
- Exact file paths are included in every task description

---

## Phase 1: Setup

**Purpose**: Create the new package directories that will house the admission framework.

- [x] T001 Create package directories `gitstore-api/internal/admission/` and `gitstore-api/internal/admission/catalog/`

---

## Phase 2: Foundational — Core Admission Types & Chain

**Purpose**: Core types, extension-point interfaces, and the chain orchestrator. This phase is a hard prerequisite for every user-story phase.

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete and all tests are green.

> **Write tests FIRST — verify they FAIL before writing implementation**

- [x] T002 Write unit tests for core admission types and constructor helpers in `gitstore-api/internal/admission/admission_test.go` — cover `Operation`, `Trigger`, `AdmissionCondition`, `AdmissionRequest` zero values, `Allowed`/`Denied` variants, `DecisionAllow`, `DecisionDeny`, sealed-interface assertion
- [x] T003 Create `gitstore-api/internal/admission/admission.go` with `Operation`, `Trigger`, `GitAdmissionContext`, `AdmissionCondition`, `AdmissionRequest`, `AdmissionDecision` (sealed), `Allowed`, `Denied`, `DecisionAllow`, `DecisionDeny` — make T002 pass
- [x] T004 Create `gitstore-api/internal/admission/interfaces.go` with `MutatingAdmissionPolicy`, `ValidatingAdmissionPolicy`, `MutatingAdmissionWebhook`, `ValidatingAdmissionWebhook` — compile-time satisfaction asserted via blank-identifier stubs in `admission_test.go`
- [x] T005 Write all chain behaviour tests in `gitstore-api/internal/admission/chain_test.go` — cover: empty chain returns `Allowed`; single validating policy; denial short-circuits remaining policies; panic in policy recovered and returns `Denied{Reason:"InternalError"}`; conditions from multiple validating policies accumulated; **mutating policy (phase 1) invoked before validating policy (phase 3)** [US3 acceptance scenario]; **patched object passed to downstream validators** [US3 acceptance scenario]; **MutatingAdmissionWebhook (phase 2) invoked between mutating policies and validating policies** [US4 acceptance scenario]; **ValidatingAdmissionWebhook (phase 4) invoked after built-in validating policies** [US4 acceptance scenario]; nil-policy treated as `Allowed`
- [x] T006 Create `gitstore-api/internal/admission/chain.go` with `Chain`, `NewChain`, `RegisterMutatingPolicy`, `RegisterMutatingWebhook`, `RegisterValidatingPolicy`, `RegisterValidatingWebhook`, `Admit` — make T005 pass

**Checkpoint**: `go test ./internal/admission/...` passes — Foundation is ready

---

## Phase 3: User Story 1 — Clear, Attributable Validation Feedback (Priority: P1) 🎯 MVP

**Goal**: All four resource kinds flow through the admission chain; `ProductVariant` and `CategoryTaxonomy` semantic checks are in named policies; authors see accurate `ProductResolved`, `OptionsAccepted`, `PricingAccepted`, `ParentResolved`, `Acyclic` conditions after push.

**Independent Test**: Push a `ProductVariant` whose `productRef` names a product not in the datastore. Run `go test ./internal/cataloggrpc/...`. Verify the admitted resource's status contains `ProductResolved: false`.

> **Write tests FIRST — verify they FAIL before writing implementation**

### Tests for User Story 1 ⚠️

- [x] T007 [P] [US1] Write unit tests for `ValidateSelectedOptions` and `ValidateCELExpressions` in `gitstore-api/internal/admission/catalog/product_variant_policy_test.go` — port and expand the three tests from `cataloggrpc/server_variant_test.go`; add cases: nil CEL env (skip), nil pricing (skip), valid expression, invalid expression (field path in reason)
- [x] T008 [P] [US1] Write unit tests for `DetectCycles`, `TopoSortCategories`, and `CategoryTaxonomyValidatingPolicy.Validate` in `gitstore-api/internal/admission/catalog/category_taxonomy_policy_test.go` — cover: no cycles; direct cycle A↔B; tail cycle A→B→C→B (A acyclic, B and C in cycle); root (no parentRef, `ParentResolved: true`); child with in-push parent; child with datastore parent; child with missing parent (`ParentResolved: false`); cycle member (`Acyclic: false`)
- [x] T009 [P] [US1] Write unit tests for `ProductValidatingPolicy` stub in `gitstore-api/internal/admission/catalog/product_policy_test.go` — verify `Name()` returns `"ProductValidatingPolicy"`, `Validate` returns `Allowed` with no conditions for `Kind == "Product"`, non-Product kind returns `Allowed` without side-effects
- [x] T010 [P] [US1] Write unit tests for `CollectionValidatingPolicy` stub in `gitstore-api/internal/admission/catalog/collection_policy_test.go` — same pattern as T009, `Kind == "Collection"`

### Implementation for User Story 1

- [x] T011 [P] [US1] Create `gitstore-api/internal/admission/catalog/product_variant_policy.go` — export `ValidateSelectedOptions` and `ValidateCELExpressions` (migrated from `cataloggrpc/server.go`); implement `ProductVariantValidatingPolicy` with `Name()` and `Validate()` emitting `ProductResolved`, `OptionsAccepted`, `PricingAccepted` conditions — make T007 pass
- [x] T012 [P] [US1] Create `gitstore-api/internal/admission/catalog/category_taxonomy_policy.go` — export `DetectCycles` and `TopoSortCategories` (migrated from `cataloggrpc/server.go`); implement `CategoryTaxonomyValidatingPolicy` with `Name()` and `Validate()` using `req.PushSet` for in-push parent resolution and cycle detection — make T008 pass
- [x] T013 [P] [US1] Create `gitstore-api/internal/admission/catalog/product_policy.go` — `ProductValidatingPolicy` stub; constructor `NewProductValidatingPolicy(log *zap.Logger)`; `Name()` returns `"ProductValidatingPolicy"`; `Validate` returns `DecisionAllow()` — make T009 pass
- [x] T014 [P] [US1] Create `gitstore-api/internal/admission/catalog/collection_policy.go` — `CollectionValidatingPolicy` stub; constructor `NewCollectionValidatingPolicy(log *zap.Logger)`; `Name()` returns `"CollectionValidatingPolicy"`; `Validate` returns `DecisionAllow()` — make T010 pass
- [x] T015 [US1] Add `chain *admission.Chain` field to `Server` in `gitstore-api/internal/cataloggrpc/server.go`; update `NewServer` to construct the chain and register all four policies: `NewProductValidatingPolicy`, `NewCollectionValidatingPolicy`, `NewProductVariantValidatingPolicy`, `NewCategoryTaxonomyValidatingPolicy`
- [x] T016 [US1] Refactor `admitProductVariant` in `gitstore-api/internal/cataloggrpc/server.go` — replace the inline semantic check block (product lookup, `validateSelectedOptions`, `celValidateExpressions`) with a call to `s.chain.Admit`; map `Allowed.Conditions` back to `variantAdmitResult` fields for status building; upsert and status-builder logic unchanged
- [x] T017 [US1] Refactor `admitCategoryTaxonomyWithContext` in `gitstore-api/internal/cataloggrpc/server.go` — replace inline cycle/parent checks with a call to `s.chain.Admit`, passing the full `PushSet`; batch topo-sort pre-processing in `AdmitResources` stays for admission ordering
- [x] T018 [US1] Wire chain calls in `admitProduct` and `admitCollection` in `gitstore-api/internal/cataloggrpc/server.go` — call `s.chain.Admit`; stub policies return `Allowed` immediately; ensures all four kinds flow through the chain uniformly
- [x] T019 [US1] Remove `validateSelectedOptions` and `celValidateExpressions` from `gitstore-api/internal/cataloggrpc/server.go`; remove `detectCycles` and `topoSortCategories` from `server.go`; delete `gitstore-api/internal/cataloggrpc/server_variant_test.go` (tests ported to T007)
- [x] T020 [US1] Remove `ValidationContext` from `gitstore-api/internal/cataloggrpc/context.go`

**Checkpoint**: `go test ./internal/cataloggrpc/...` passes unchanged — migration is correct and all existing integration tests are green

---

## Phase 4: User Story 2 — Register a New Policy Without Modifying the Storage Layer (Priority: P2)

**Goal**: Demonstrate that a new `ValidatingAdmissionPolicy` can be registered and exercised through a resource's admission path without any changes to `admitProductVariant`, `admitCategoryTaxonomyWithContext`, `admitProduct`, or `admitCollection`.

**Independent Test**: Write a test that adds a custom policy to the chain and verifies its condition appears in the admitted resource's status — without touching any `admit*` method body.

> **Write test FIRST — verify it FAILS before writing implementation**

- [x] T021 [US2] Write an integration test in `gitstore-api/internal/cataloggrpc/server_test.go` that adds a custom `ValidatingAdmissionPolicy` stub to the chain via `ServerDeps.ExtraValidatingPolicies []admission.ValidatingAdmissionPolicy` and verifies the policy fires for an admitted `Product` (condition appears in status) without any change to `admitProduct`'s body — test should fail to compile until T022
- [x] T022 [US2] Add `ExtraValidatingPolicies []admission.ValidatingAdmissionPolicy` field to `ServerDeps` in `gitstore-api/internal/cataloggrpc/server.go`; update `NewServer` to register extras via `chain.RegisterValidatingPolicy` after the four default policies — make T021 pass

**Checkpoint**: `go test ./internal/cataloggrpc/... -run TestExtensibleAdmissionPolicy` passes — extensibility is demonstrated

---

## Phase 5: User Story 3 — Mutating Policy Extension Point (Priority: P3)

**Goal**: Confirm the `MutatingAdmissionPolicy` interface and the chain's phase 1 execution are correct. The acceptance scenarios (Mutate called before Validate; patched object passed downstream) are proven by the chain unit tests written in T005.

**Independent Test**: `go test ./internal/admission/... -run TestChain_MutatingPhase` passes with tests covering both acceptance scenarios.

- [x] T023 [US3] Verify `chain_test.go` contains test functions covering both US3 acceptance scenarios: (1) `TestChain_MutatingPolicyBeforeValidating` — `Mutate` is invoked before any `Validate`; (2) `TestChain_PatchPropagation` — patched object in `Allowed.Patches` is accumulated in the final result; run `go test ./internal/admission/... -v -run "TestChain_Mutating|TestChain_Patch"` and confirm green

**Checkpoint**: US3 acceptance scenarios verified at the unit level — mutating extension point contract is demonstrable

---

## Phase 6: User Story 4 — Webhook Extension Points (Priority: P4)

**Goal**: Confirm `MutatingAdmissionWebhook` and `ValidatingAdmissionWebhook` interfaces are defined and positioned correctly in the chain's phase ordering. The acceptance scenarios are proven by the chain unit tests written in T005.

**Independent Test**: `go test ./internal/admission/... -run TestChain_WebhookPhase` passes with tests covering both acceptance scenarios.

- [x] T024 [US4] Verify `chain_test.go` contains test functions covering both US4 acceptance scenarios: (1) `TestChain_ValidatingWebhookAfterPolicies` — `ValidatingAdmissionWebhook.Validate` is invoked after all `ValidatingAdmissionPolicy.Validate` calls; (2) `TestChain_MutatingWebhookPhase` — `MutatingAdmissionWebhook.Mutate` is invoked after built-in mutating policies and before validating policies; run `go test ./internal/admission/... -v -run "TestChain_.*Webhook"` and confirm green

**Checkpoint**: US4 acceptance scenarios verified at the unit level — webhook extension point contract is demonstrable

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Final verification, documentation updates, and gate checks.

- [x] T025 [P] Update `docs/implementation/phases.md` (or equivalent architecture doc) to reference the new `internal/admission/` package and its role in the resource lifecycle in `docs/`
- [x] T026 [P] Update `quickstart.md` with final verified file map and end-to-end push commands in `specs/027-admission-contracts/quickstart.md`
- [x] T027 Run `make test` and confirm all packages pass — `go test ./...` across `gitstore-api`
- [x] T028 Run `make pr-ready` and confirm lint + license-check + test gate is green

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundation (Phase 2)**: Depends on Phase 1 — **BLOCKS all user-story phases**
- **US1 (Phase 3)**: Depends on Foundation — the primary delivery phase
- **US2 (Phase 4)**: Depends on US1 (chain must be wired in Server; `ExtraValidatingPolicies` requires chain in NewServer)
- **US3 (Phase 5)**: Depends on Foundation (chain_test.go written in T005); independent of US1/US2
- **US4 (Phase 6)**: Depends on Foundation (chain_test.go written in T005); independent of US1/US2/US3
- **Polish (Phase 7)**: Depends on all user-story phases completing

### User Story Dependencies

- **US1 (P1)**: Can start after Foundation — no dependency on other stories
- **US2 (P2)**: Depends on US1 (chain wired in `NewServer`/`Server`)
- **US3 (P3)**: Depends on Foundation only — chain tests written in T005
- **US4 (P4)**: Depends on Foundation only — chain tests written in T005

### Within Each User Story

- Tests (T007–T010) MUST be written and confirmed failing before policy implementations (T011–T014)
- Policy files (T011–T014) must exist before migration tasks (T015–T020) — policies are imported by server.go
- Migration tasks T015→T016→T017→T018→T019→T020 are sequential (all touch `server.go` / `context.go`)
- T021 (test) must fail before T022 (implementation)

### Parallel Opportunities

- T007, T008, T009, T010 — all different test files in `admission/catalog/` — run in parallel
- T011, T012, T013, T014 — all different implementation files in `admission/catalog/` — run in parallel with each other (after respective tests)
- T025, T026 — different documentation files — run in parallel
- US3 (T023) and US4 (T024) — different test run targets — run in parallel
- US3/US4 can start in parallel with US1 once Foundation is done

---

## Parallel Example: User Story 1

```bash
# Step 1 — write all four policy tests in parallel (all different files):
Task T007: product_variant_policy_test.go
Task T008: category_taxonomy_policy_test.go
Task T009: product_policy_test.go
Task T010: collection_policy_test.go

# Step 2 — implement all four policies in parallel (all different files, after their tests):
Task T011: product_variant_policy.go
Task T012: category_taxonomy_policy.go
Task T013: product_policy.go
Task T014: collection_policy.go

# Step 3 — migrate cataloggrpc sequentially (same file, must be ordered):
Task T015 → T016 → T017 → T018 → T019 → T020
```

## Parallel Example: US3 + US4 + US1

```bash
# After Foundation (Phase 2) completes, launch in parallel:
Thread A: US1 Phase 3 (T007–T020)
Thread B: Verify US3 chain tests (T023)
Thread C: Verify US4 chain tests (T024)

# US2 (T021–T022) starts after US1 Thread A completes
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001)
2. Complete Phase 2: Foundation (T002–T006) — CRITICAL, blocks everything
3. Complete Phase 3: US1 (T007–T020)
4. **STOP and VALIDATE**: `go test ./internal/admission/... ./internal/cataloggrpc/...` — all green
5. **MVP delivered**: All four resource kinds flow through the admission chain; migrated semantic checks produce identical conditions; no consumer breakage

### Incremental Delivery

1. Setup (T001) → Foundation (T002–T006) → US1 (T007–T020) → **MVP ✅**
2. Add US2 (T021–T022) → extensibility demo ✅
3. Add US3 (T023) + US4 (T024) → extension-point contracts verified ✅
4. Polish (T025–T028) → `make pr-ready` green ✅

---

## Notes

- **[P]** tasks operate on different files with no in-flight dependencies — safe to run concurrently
- **[Story]** label maps every task to its user story for traceability to spec.md
- Constitution Principle I: every implementation task must have a preceding failing test task
- Migration tasks T015–T020 are sequential; do not attempt to parallelize work on `server.go`
- `server_variant_test.go` is deleted in T019 — its three tests are superseded by the expanded T007 test set
- `ValidationContext` removed in T020 — no production code references it
- `variantAdmitResult` stays in `cataloggrpc/server.go` (status builder concern); only the semantic check helpers migrate
- The `TriggerCommitFile` constant defined in T003 is a forward-compatibility marker; no wiring is added in this spec

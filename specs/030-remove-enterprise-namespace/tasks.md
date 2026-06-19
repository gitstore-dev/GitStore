# Tasks: Namespace Types — Remove Enterprise

**Input**: Design documents from `specs/030-remove-enterprise-namespace/`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Regression test MUST be written and confirmed **failing** before any schema or implementation task in Phase 3 begins.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel with other [P] tasks in the same phase (different files, no conflicting edits)
- **[Story]**: Maps to user stories US1, US2, US3 from spec.md
- All paths are relative to the repository root

---

## Phase 1: Setup

**Purpose**: Create migration infrastructure before implementation begins.

- [x] T001 Create `gitstore-api/internal/datastore/scylla/migrations/004_drop_parent_enterprise_id.cql` with body `ALTER TABLE namespaces DROP parent_enterprise_id;`

---

## Phase 2: Foundational — Failing Regression Test (Constitution Principle I — REQUIRED GATE)

**Purpose**: Write the regression test that guards against future re-introduction of `ENTERPRISE` and confirm it FAILS against the current codebase.

**⚠️ CRITICAL**: After T002, run `cd gitstore-api && go test -run TestCreateNamespace_enterpriseTier_rejected ./internal/graph/resolver/` and confirm the test FAILS (currently `ENTERPRISE` is accepted). Do NOT proceed to Phase 3 until failure is confirmed.

- [x] T002 In `gitstore-api/internal/graph/resolver/namespace_service_test.go`, add `TestCreateNamespace_enterpriseTier_rejected`: call `CreateNamespace` with `tier: model.NamespaceTierEnterprise` and `isAdmin: true`, assert a `gqlerror` is returned, and assert the error message identifies `enterprise` as an invalid value. Run the test and confirm it **FAILS**.

**Checkpoint**: Regression test written and confirmed failing — Phase 3 may begin.

---

## Phase 3: User Story 1 — API Contract Enforces Two-Type Namespace Model (Priority: P1) 🎯 MVP

**Goal**: Remove `ENTERPRISE` from the type contract; rename `ORGANISATION` → `ORGANIZATION` in all user-facing surfaces; drop `parentEnterpriseId`/`parentEnterpriseIdentifier` from schema and code; apply the Scylla column drop. After this phase any request using `ENTERPRISE` is rejected at the GraphQL schema layer.

**Independent Test**: `cd gitstore-api && go build ./... && go test ./...` — `TestCreateNamespace_enterpriseTier_rejected` passes; all other namespace tests pass; no compilation errors.

> Regression test already written in Phase 2 (Constitution Principle I satisfied).

### Implementation for User Story 1

- [x] T003 [US1] Update `shared/schemas/namespace.graphqls`: remove the `ENTERPRISE` enum value and its doc comment; rename `ORGANISATION` → `ORGANIZATION` and update its doc comment to "Organization namespace"; remove the `parentEnterpriseId: ID` field from `Namespace` type; remove the `parentEnterpriseIdentifier: String` field from `CreateNamespaceInput`; remove the `ENTERPRISE tier requires isAdmin` sentence from the `createNamespace` mutation doc string; update `CreateNamespaceInput.tier` doc to reference only `USER` and `ORGANIZATION`
- [x] T004 [US1] Run `cd gitstore-api && go generate ./...` to regenerate `internal/graph/model/models_gen.go`, `internal/graph/generated/namespace.generated.go`, and `internal/graph/generated/root_.generated.go` from the updated schema (do NOT edit generated files by hand)
- [x] T005 [P] [US1] Update `gitstore-api/internal/datastore/entities.go`: remove the `NamespaceTierEnterprise NamespaceTier = "enterprise"` constant; rename `NamespaceTierOrganisation` → `NamespaceTierOrganization` (keep the stored string value `"organisation"` unchanged); remove the `ParentEnterpriseID *string` field from the `Namespace` struct
- [x] T006 [P] [US1] Update `gitstore-api/internal/datastore/scylla/models.go`: remove `ParentEnterpriseID *string \`db:"parent_enterprise_id"\`` from the `namespaceRow` struct; remove `"parent_enterprise_id"` from the column name slice/list used in queries
- [x] T007 [P] [US1] Update `gitstore-api/internal/datastore/scylla/backend.go`: remove `"parent_enterprise_id"` from the namespace column list (line 213 area)
- [x] T008 [P] [US1] Update `gitstore-api/internal/graph/resolver/service.go`: remove the enterprise-tier admin gate block at lines 239–258 (the `if tier == datastore.NamespaceTierEnterprise` check, the `parentEnterpriseIdentifier` resolution, and the `parentEnterpriseID` variable); remove the `parentEnterpriseID` field from the `datastore.Namespace` struct literal in the create path; in `datastoreNamespaceTierFromModel` remove the `model.NamespaceTierEnterprise` case and update the `model.NamespaceTierOrganisation` case to `model.NamespaceTierOrganization → datastore.NamespaceTierOrganization`
- [x] T009 [P] [US1] Update `gitstore-api/internal/graph/resolver/converters.go`: remove the `ParentEnterpriseID` encoding block (lines 30–34) and the `ParentEnterpriseID` field from the returned `model.Namespace` literal (line ~40); in `datastoreNamespaceTierToModel` remove the `datastore.NamespaceTierEnterprise` case and update the `datastore.NamespaceTierOrganisation` case to `datastore.NamespaceTierOrganization → model.NamespaceTierOrganization`; update the scylla row-to-entity mapper's `ParentEnterpriseID` copy (lines ~1336, ~1349 in scylla/backend.go if present in converters — trace and remove)
- [x] T010 [P] [US1] Update `gitstore-api/gqlgen.yml`: remove the commented-out `# - enterprise` line
- [x] T011 [P] [US1] In `gitstore-api/internal/graph/resolver/namespace_service_test.go`, remove `TestCreateNamespace_enterpriseTier_withoutAdmin_denied` and `TestCreateNamespace_enterpriseTier_withAdmin_succeeds`; update any remaining reference to `model.NamespaceTierOrganisation` → `model.NamespaceTierOrganization`
- [x] T012 [P] [US1] In `gitstore-api/tests/contract/datastore/contract_test.go`, remove the enterprise namespace fixture at line ~514 (`ns := newNamespace(datastore.NamespaceTierEnterprise)`) and any associated test assertions; update any reference to `datastore.NamespaceTierOrganisation` → `datastore.NamespaceTierOrganization`
- [x] T013 [US1] Run `cd gitstore-api && go build ./... && go test ./...` — confirm `TestCreateNamespace_enterpriseTier_rejected` now passes and all other tests pass with no compilation errors

**Checkpoint**: US1 fully functional — `ENTERPRISE` rejected at schema layer, `ORGANIZATION` accepted, Scylla column dropped, build clean.

---

## Phase 4: User Story 2 — Schema and Documentation Reflect Two-Type Model (Priority: P2)

**Goal**: Remove all `enterprise`/`ENTERPRISE` namespace type references from documentation and update `ORGANISATION` → `ORGANIZATION`. Add a note that enterprise-level modeling, if ever required, belongs outside the namespace type model.

**Independent Test**: Open `docs/architecture.md`, `docs/api-reference.md`, and `docs/resources/git-backed.md` — confirm `ENTERPRISE`, `parentEnterpriseId`, and `parentEnterpriseIdentifier` do not appear in any namespace type context; confirm `ORGANIZATION` (American spelling) is used throughout.

### Implementation for User Story 2

- [x] T014 [P] [US2] Update `docs/architecture.md`: remove `ENTERPRISE` from the namespace tier table (lines ~509–513); remove the `isAdmin` admin-gate note for enterprise creation (line ~522); remove `parentEnterpriseId` from example GraphQL query snippets (lines ~552, ~560); rename any remaining `ORGANISATION` → `ORGANIZATION`
- [x] T015 [P] [US2] Update `docs/api-reference.md`: remove `ENTERPRISE` from the tier enum value table (line ~609); remove the `parentEnterpriseIdentifier` input field row (line ~610); remove `parentEnterpriseId: ID` field entries (lines ~167, ~746); remove the ENTERPRISE admin-token requirement note (line ~580); rename any remaining `ORGANISATION` → `ORGANIZATION`; add a sentence noting that enterprise-level grouping, if ever needed, will be modeled outside the namespace path
- [x] T016 [P] [US2] Update `docs/resources/git-backed.md`: remove `parentEnterpriseRef` from the namespace resource spec field list (line ~43); rename any remaining `ORGANISATION` → `ORGANIZATION`

**Checkpoint**: US2 complete — all documentation consistent with the two-type `USER`/`ORGANIZATION` model.

---

## Phase 5: User Story 3 — Test Suite Protects Against Regression (Priority: P3)

**Goal**: Confirm the full test suite is clean with no remaining `enterprise` namespace type references in code or fixtures, and that the regression test would catch any future re-introduction of `ENTERPRISE`.

**Independent Test**: `grep -r "NamespaceTierEnterprise\|NamespaceTierOrganisation\b\|ParentEnterpriseID" gitstore-api/internal gitstore-api/tests` returns zero hits; `make test` passes.

### Implementation for User Story 3

- [x] T017 [US3] Run `grep -rn "NamespaceTierEnterprise\|NamespaceTierOrganisation\|ParentEnterpriseID\|parentEnterpriseId\|parentEnterpriseIdentifier" gitstore-api/internal gitstore-api/tests` from the repo root and confirm zero hits; if any remain, resolve them before proceeding
- [x] T018 [US3] Run `make test` from the repository root and confirm all tests pass with zero compilation errors and zero test failures

**Checkpoint**: US3 complete — test suite clean, regression test (`TestCreateNamespace_enterpriseTier_rejected`) is the sole guardian of the two-type model invariant.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Final spelling and PR readiness checks.

- [x] T019 [P] Run `grep -rn "ORGANISATION\|NamespaceTierOrganisation" shared/schemas/ gitstore-api/internal gitstore-api/tests docs/` and confirm zero hits (note: the stored value `"organisation"` in resolver/service.go's `datastoreNamespaceTierFromModel` default is the datastore wire value and is expected — exclude Go string literals from this check if needed)
- [x] T020 Run `make pr-ready` from the repository root and confirm all checks pass

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — BLOCKS all user story work
- **US1 (Phase 3)**: Depends on Phase 2 completion (regression test written and confirmed failing)
- **US2 (Phase 4)**: Depends on US1 — documentation references the new `ORGANIZATION` enum value from the updated schema
- **US3 (Phase 5)**: Depends on US1 and US2 — verifies full codebase + docs are clean
- **Polish (Phase 6)**: Depends on all user stories complete

### User Story Dependencies

- **US1 (P1)**: Unblocked after Phase 2 — core implementation change
- **US2 (P2)**: After US1 — documentation follows the schema changes
- **US3 (P3)**: After US1 + US2 — final verification pass

### Within User Story 1 (sequential constraints)

- T003 (schema update) → T004 (code generation) → T005–T012 (parallel file updates) → T013 (verify)
- T005–T012 can all be edited in parallel (different files); they are gated on T004 completing so the generated types are available
- T008 and T009 reference `NamespaceTierOrganization` from T005 — ensure T005 is merged/present before compiling

### Parallel Opportunities

- T005, T006, T007, T008, T009, T010, T011, T012 can all be edited in parallel within one session after T004 completes
- T014, T015, T016 are fully independent documentation files
- T019 can run in parallel with T020

---

## Parallel Example: User Story 1

```bash
# Sequential prerequisite — must complete first:
T003: Update shared/schemas/namespace.graphqls (remove ENTERPRISE, rename ORGANISATION→ORGANIZATION, remove parentEnterprise* fields)
T004: cd gitstore-api && go generate ./...   # regenerate models_gen.go + generated/*.go

# After T004 — all can be edited in parallel (different files):
T005: Update internal/datastore/entities.go
T006: Update internal/datastore/scylla/models.go
T007: Update internal/datastore/scylla/backend.go
T008: Update internal/graph/resolver/service.go
T009: Update internal/graph/resolver/converters.go
T010: Update gqlgen.yml
T011: Update internal/graph/resolver/namespace_service_test.go
T012: Update tests/contract/datastore/contract_test.go

# After all parallel edits:
T013: go build ./... && go test ./...   # regression test must pass
```

## Parallel Example: User Story 2

```bash
# All three documentation tasks are fully parallel:
T014: Update docs/architecture.md
T015: Update docs/api-reference.md
T016: Update docs/resources/git-backed.md
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Create migration file
2. Complete Phase 2: Write regression test, confirm it FAILS (constitution gate)
3. Complete Phase 3: Update schema → generate → update all Go files → clean up old tests → verify
4. **STOP and VALIDATE**: `go test ./...` green, `TestCreateNamespace_enterpriseTier_rejected` passes
5. Deploy/demo — the API contract change is complete and protected

### Incremental Delivery

1. Phase 1 + Phase 2 → Safety net in place (test written, confirmed failing)
2. Phase 3 (US1) → Two-type API contract live, migration applied → Deploy
3. Phase 4 (US2) → Documentation aligned → Communicate change to consumers
4. Phase 5 (US3) → Full suite verified clean
5. Phase 6 (Polish) → PR ready, ship

### Parallel Team Strategy

With two developers after Phase 2 is complete:

- Developer A: US1 (T003–T013) — schema + code changes
- Developer B: US2 (T014–T016) — can start documentation updates in parallel once T003 (schema) is done

---

## Notes

- [P] tasks = different files, no edit conflicts; can be worked on simultaneously within a session
- Constitution Principle I is satisfied by Phase 2: regression test written and confirmed **failing** before any schema or code change
- Generated files (`models_gen.go`, `namespace.generated.go`, `root_.generated.go`) are regenerated by T004 — never edit them by hand
- The stored datastore string value `"organisation"` in `entities.go` is intentionally unchanged; only Go constants and GraphQL enum values use the American spelling `organization`
- The reserved identifier `"enterprise"` in `service.go` (`reservedIdentifiers` map) is intentionally **retained** — the string `enterprise` cannot be used as a namespace slug regardless of tier status
- Migration `004_drop_parent_enterprise_id.cql` handles the Scylla column drop; the `go-memdb` in-memory backend needs no migration (struct field removal in T005 is sufficient)

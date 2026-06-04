# Tasks: Product Parser and Kind Validation Enforcement

**Input**: Design documents from `specs/015-product-parser/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified to FAIL before proceeding.

**Organization**: Tasks are grouped by user story. No setup or foundational phase is needed — `internal/validate` and `internal/catalog` packages exist and build cleanly from spec#014.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared state)
- **[Story]**: Which user story this task belongs to
- Exact file paths in every description

---

## Phase 1: Foundational — Validate Test Baseline

**Purpose**: Confirm all existing tests pass before any changes. This is the red/green baseline; all new tests added in subsequent phases MUST initially fail.

**⚠️ CRITICAL**: No user story work begins until this phase is complete.

- [x] T001 Run `go test ./internal/validate/... -v` and confirm all 9 existing tests pass in `gitstore-api/internal/validate/validator_test.go`
- [x] T002 Run `go test ./internal/catalog/... -v` and confirm all existing catalog tests pass in `gitstore-api/internal/catalog/`

**Checkpoint**: All existing tests green — baseline confirmed.

---

## Phase 2: User Story 1 — Parse a Valid Product File (Priority: P1) 🎯 MVP

**Goal**: The happy-path parser already passes from spec#014. This phase adds explicit test coverage for the optional-fields and labels/annotations cases not yet exercised.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_Valid" -v` — all valid-file scenarios pass.

### Tests for User Story 1 ⚠️ Write FIRST — must FAIL before T005

- [x] T003 [P] [US1] Add `TestParse_ValidProduct_OptionalFieldsOmitted` to `gitstore-api/internal/validate/validator_test.go` — submit a file with only `apiVersion`, `kind`, `metadata.name`; assert no error and empty/nil optional fields
- [x] T004 [P] [US1] Add `TestParse_ValidProduct_LabelsAndAnnotations` to `gitstore-api/internal/validate/validator_test.go` — submit a file with populated `metadata.labels` and `metadata.annotations`; assert both maps correctly extracted

### Implementation for User Story 1

- [x] T005 [US1] Verify `validate.Parse` in `gitstore-api/internal/validate/validator.go` already handles US1 correctly — run T003/T004 tests and confirm they pass without any code change (US1 is complete from spec#014)

**Checkpoint**: US1 fully covered — valid-file parsing confirmed green.

---

## Phase 3: User Story 2 — Reject Wrong or Missing Kind (Priority: P1)

**Goal**: `kind` field validation is enforced with case-sensitive exact match.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_.*Kind" -v`

### Tests for User Story 2 ⚠️ Write FIRST — must FAIL before T007

- [x] T006 [P] [US2] Add `TestParse_KindLowercaseRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with `kind: product` (lowercase); assert error containing `"kind"`

### Implementation for User Story 2

- [x] T007 [US2] Verify `TestParse_KindLowercaseRejected` passes — the struct tag `validate:"eq=Product"` already enforces this; confirm no code change needed in `gitstore-api/internal/validate/validator.go`

**Checkpoint**: US2 fully covered — kind rejection confirmed for wrong value, missing, and lowercase.

---

## Phase 4: User Story 3 — Reject Missing Required Fields (Priority: P1)

**Goal**: `apiVersion` wrong value, `spec` absent, and `metadata.name` empty string all produce specific errors.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_(WrongApiVersion|SpecAbsent|EmptyName)" -v`

### Tests for User Story 3 ⚠️ Write FIRST — must FAIL before T010–T011

- [x] T008 [P] [US3] Add `TestParse_WrongApiVersionRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with `apiVersion: catalog.gitstore.dev/v1`; assert error containing `"apiVersion"`
- [x] T009 [P] [US3] Add `TestParse_SpecAbsentRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with no `spec` key; assert error containing `"spec"`
- [x] T010 [P] [US3] Add `TestParse_EmptyNameRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with `metadata.name: ""` (explicit empty string); assert error containing `"name"`

### Implementation for User Story 3

- [x] T011 [US3] Add `spec` absent pre-parse check to `preParseChecks` in `gitstore-api/internal/validate/validator.go`: if `raw["spec"]` is nil/absent return `fmt.Errorf("validate: spec is required")`
- [x] T012 [US3] Confirm `TestParse_WrongApiVersionRejected` and `TestParse_EmptyNameRejected` pass without further changes — struct tags `validate:"eq=catalog.gitstore.dev/v1beta1"` and `validate:"required"` already cover these

**Checkpoint**: US3 fully covered — missing-required-field rejection confirmed for apiVersion, spec, and name.

---

## Phase 5: User Story 4 — Reject Forbidden System-Managed Fields (Priority: P1)

**Goal**: All five read-only `metadata` sub-keys and the top-level `status` key are rejected with field-specific errors.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_.*Rejected.*(ReadOnly|Status|OwnerRef|ResourceVersion|Generation|Creation|Revision)" -v`

### Tests for User Story 4 ⚠️ Write FIRST — must FAIL before T015

- [x] T013 [P] [US4] Add `TestParse_ReadOnlyMetadataOwnerReferencesRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with `metadata.ownerReferences: []`; assert error containing `"ownerReferences"`
- [x] T014 [P] [US4] Add `TestParse_ReadOnlyMetadataAllFieldsRejected` to `gitstore-api/internal/validate/validator_test.go` — four sub-tests (one per field: `resourceVersion`, `generation`, `creationTimestamp`, `revision`) each asserting error containing the field name

### Implementation for User Story 4

- [x] T015 [US4] Verify `TestParse_ReadOnlyMetadataOwnerReferencesRejected` and `TestParse_ReadOnlyMetadataAllFieldsRejected` pass — `ownerReferences`, `resourceVersion`, `generation`, `creationTimestamp`, and `revision` are already in the `readOnly` slice in `preParseChecks` in `gitstore-api/internal/validate/validator.go`; confirm no code change needed

**Checkpoint**: US4 fully covered — all forbidden read-only fields and `status` rejected.

---

## Phase 6: User Story 5 — Enforce Spec-Level Constraint Rules (Priority: P2)

**Goal**: Label prefix/value length constraints and multi-error collection are enforced.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_(LabelPrefix|LabelValue|MultipleViolations)" -v`

### Tests for User Story 5 ⚠️ Write FIRST — must FAIL before T019–T020

- [x] T016 [P] [US5] Add `TestParse_LabelKeyPrefixTooLongRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with a label key whose prefix part exceeds 253 characters; assert error containing `"prefix"`
- [x] T017 [P] [US5] Add `TestParse_LabelValueTooLongRejected` to `gitstore-api/internal/validate/validator_test.go` — submit a file with a label value exceeding 63 characters; assert error containing `"label value"`
- [x] T018 [P] [US5] Add `TestParse_MultipleViolationsReportedTogether` to `gitstore-api/internal/validate/validator_test.go` — submit a file with both a wrong `kind` value and a duplicate option name; assert the returned error contains messages referencing both violations

### Implementation for User Story 5

- [x] T019 [US5] Refactor post-parse validation in `gitstore-api/internal/validate/validator.go` to multi-error collection: accumulate errors from `validate.Struct`, `validateSpec`, and `validateLabels` into `[]error`; return `errors.Join(errs...)` instead of returning on first error. Pre-parse checks (`preParseChecks`) remain short-circuit.
- [x] T020 [US5] Confirm `TestParse_LabelKeyPrefixTooLongRejected` and `TestParse_LabelValueTooLongRejected` pass — `validateLabels` already enforces both; confirm tests pass after T019 lands

**Checkpoint**: US5 fully covered — label length and multi-error collection confirmed.

---

## Phase 7: User Story 5 Continued — Frontmatter Opt-In Skip (FR-013)

**Goal**: Files without `---` are silently skipped; README.md and all non-product Markdown return `(nil, rawBytes, nil)`.

**Independent Test**: `go test ./internal/validate/... -run "TestParse_NoFrontmatterSkipped" -v`

### Tests ⚠️ Write FIRST — must FAIL before T022

- [x] T021 [US5] Add `TestParse_NoFrontmatterSkipped` to `gitstore-api/internal/validate/validator_test.go` — submit a plain Markdown string with no `---` delimiter (e.g. a README); assert all three return values are `nil, non-nil bytes, nil` (no error, raw bytes returned)

### Implementation

- [x] T022 [US5] Implement frontmatter opt-in skip at the top of `Parse` in `gitstore-api/internal/validate/validator.go`: after `io.ReadAll`, if the trimmed content does not begin with `---`, return `(nil, raw, nil)` immediately — remove the current error from `extractFrontmatterBlock` for the missing-delimiter case

**Checkpoint**: US5 complete — opt-in skip confirmed; all 5 user stories fully implemented.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Final validation, documentation, and licence headers.

- [x] T023 Run full test suite `go test -race ./internal/validate/... ./internal/catalog/... -v` in `gitstore-api/` and confirm all tests pass with no race conditions
- [x] T024 [P] Run `gofmt -s -l gitstore-api/internal/validate/` and fix any formatting issues in `gitstore-api/internal/validate/validator.go` and `gitstore-api/internal/validate/validator_test.go`
- [x] T025 [P] Verify AGPL-3.0-or-later licence header is present at the top of `gitstore-api/internal/validate/validator.go` and `gitstore-api/internal/validate/validator_test.go`
- [x] T026 Run `make pr-ready` from repo root and confirm all checks pass

**Checkpoint**: All checks green — feature ready for PR.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Baseline)**: No dependencies — run immediately
- **Phase 2–7 (User Stories)**: Depend on Phase 1 completion; within each phase, tests MUST be written and verified failing before implementation tasks
- **Phase 8 (Polish)**: Depends on all user story phases complete

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 1 — no dependencies on other stories
- **US2 (P1)**: Can start after Phase 1 — independent of US1
- **US3 (P1)**: Can start after Phase 1 — independent; T011 adds a pre-parse check
- **US4 (P1)**: Can start after Phase 1 — independent
- **US5 (P2)**: Can start after Phase 1 — T019 (multi-error) and T022 (opt-in skip) are the only implementation changes; both are in `validator.go` and should be done sequentially

### Within Each Phase

1. Write all `[P]` test tasks in parallel (different test functions, same file is fine — no conflicts)
2. Run tests to confirm FAIL (red)
3. Implement in task order
4. Run tests to confirm PASS (green)

### Parallel Opportunities

- T003/T004 (US1 tests), T006 (US2 test), T008/T009/T010 (US3 tests), T013/T014 (US4 tests), T016/T017/T018 (US5 tests) are all marked `[P]` — test functions in the same file do not conflict
- T024/T025 (polish) can run in parallel

---

## Parallel Example: US3

```bash
# Write all three US3 tests together (all in validator_test.go, different functions):
Task T008: "Add TestParse_WrongApiVersionRejected"
Task T009: "Add TestParse_SpecAbsentRejected"
Task T010: "Add TestParse_EmptyNameRejected"

# Then run to confirm all three fail:
go test ./internal/validate/... -run "TestParse_(WrongApiVersion|SpecAbsent|EmptyName)" -v

# Then implement sequentially:
Task T011: Add spec absent pre-parse check
Task T012: Confirm struct-tag cases pass without changes
```

---

## Implementation Strategy

### MVP (US1–US4 only — all P1 stories)

1. Complete Phase 1: Baseline
2. Complete Phases 2–5: US1–US4 (all P1 stories — kind, required fields, forbidden fields)
3. **STOP and VALIDATE**: `go test ./internal/validate/... -v` — all scenarios pass
4. US5 (P2: label lengths, multi-error, opt-in skip) can follow in a second increment

### Full Delivery (all stories)

1. Phases 1–7 sequentially per story
2. Phase 8 polish
3. `make pr-ready`

### Note on Implementation Volume

Most implementation tasks confirm that spec#014 code already satisfies the requirement — no code change needed. The actual code changes are:

- **T011**: ~3 lines — `spec` absent pre-parse check
- **T019**: ~15 lines — multi-error refactor in post-parse stages
- **T022**: ~5 lines — opt-in skip at top of `Parse`

The majority of the work is test authorship (T003, T004, T006, T008–T010, T013, T014, T016–T018, T021 — 13 test functions).

---

## Notes

- `[P]` test tasks write to the same file (`validator_test.go`) but add different functions — safe to author in parallel, commit together
- All implementation tasks in `validator.go` are sequential (same file); do not parallelize
- Verify `errors.Join` is available — requires Go 1.20+; project uses Go 1.25 ✅
- `bytes.HasPrefix` used in quickstart sketch — use `bytes.HasPrefix(bytes.TrimLeftFunc(raw, unicode.IsSpace), []byte("---"))` to handle leading whitespace correctly

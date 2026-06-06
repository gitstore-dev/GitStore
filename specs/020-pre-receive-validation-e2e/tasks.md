# Tasks: Pre-Receive Validation End-to-End

**Input**: Design documents from `/specs/020-pre-receive-validation-e2e/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, quickstart.md ✅

**Tests**: Existing integration tests already exist and are failing (red). Constitution Principle I is already satisfied — tests were written first in spec#018/019. These tasks make them pass (green).

**Organization**: Tasks are grouped by user story. All changes land in a single file: `.github/workflows/ci-integration.yml`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different sections of the workflow file)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- All file paths are relative to repo root

---

## Phase 1: Setup

**Purpose**: No new project structure needed — all changes are in one existing CI file.

- [x] T001 Confirm `.github/workflows/ci-integration.yml` is on the `020-pre-receive-validation-e2e` branch (no divergence from `main`)

---

## Phase 2: Foundational (Blocking Prerequisite)

**Purpose**: Add the port-6000 readiness check to the existing `integration-test` job. This single step unblocks **all three user stories** for the memdb backend by ensuring the CatalogService gRPC endpoint is accepting connections before any test push is attempted.

**⚠️ CRITICAL**: US1, US2, and US3 on memdb cannot pass until this task is complete.

- [x] T002 Add "Wait for CatalogService gRPC to be ready" step (`timeout 30 sh -c 'until nc -z localhost 6000; do sleep 1; done'`) to the `integration-test` job in `.github/workflows/ci-integration.yml` — place it immediately after the existing "Wait for git-service gRPC to be ready" step (port 50051)

**Checkpoint**: After T002, run the memdb integration tests locally per `quickstart.md`. All 10 `TestProductLifecycle_*` and `TestDocumentationExamples_ParseCorrectly` tests must pass.

---

## Phase 3: User Story 1 — Invalid product push is blocked at the gate (Priority: P1) 🎯 MVP

**Goal**: Invalid product pushes are rejected pre-receive with a field-scoped error message against **both** memdb and ScyllaDB.

**Independent Test**: `TestProductLifecycle_InvalidTitle_PushRejected`, `TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected`, and the three failing `TestDocumentationExamples_ParseCorrectly` sub-tests (`invalid-status.md`, `invalid-title.md`, `invalid-media.md`) must pass.

### Implementation for User Story 1

- [x] T003 [US1] Add new `integration-test-scylla` job to `.github/workflows/ci-integration.yml` — job must: (a) use `docker compose -f compose.yml -f compose.scylla.yml up -d --build`, (b) add "Wait for ScyllaDB" step (`timeout 120 sh -c 'until nc -z localhost 9042; do sleep 2; done'`) before the service health checks, (c) extend the API health timeout to 90 s (`timeout 90 sh -c 'until curl -sf http://localhost:4000/health; do sleep 2; done'`) to account for ScyllaDB migration startup, (d) include the same port-6000 readiness step from T002, (e) run the same bootstrap, seed, and `go test` steps with `NAMESPACE=gitci REPOSITORY=catalog`, (f) use `docker compose -f compose.yml -f compose.scylla.yml` for logs and cleanup

**Checkpoint**: After T003, the `integration-test-scylla` job in CI must pass all rejection tests (`TestProductLifecycle_Invalid*` and the invalid `TestDocumentationExamples_ParseCorrectly` sub-tests) against ScyllaDB.

---

## Phase 4: User Story 2 — Valid product push is stored and queryable (Priority: P1)

**Goal**: Valid product pushes land in the catalog and are queryable via GraphQL against **both** memdb and ScyllaDB.

**Independent Test**: `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and `TestProductLifecycle_StatusHydration` must pass.

**Note**: US2 shares the same CI changes as US1 — T002 and T003 already cover the infrastructure. No additional tasks are required beyond verifying these two tests pass in both CI jobs.

**Checkpoint**: After T002 and T003, both `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and `TestProductLifecycle_StatusHydration` pass in both `integration-test` (memdb) and `integration-test-scylla` (ScyllaDB) jobs.

---

## Phase 5: User Story 3 — Documentation example files are validated on push (Priority: P2)

**Goal**: `docs/products/examples/` example files pass or fail as documented against **both** backends.

**Independent Test**: All four `TestDocumentationExamples_ParseCorrectly` sub-tests pass: `valid-product.md` accepted, `invalid-status.md`/`invalid-title.md`/`invalid-media.md` rejected with the correct error fragment.

**Note**: US3 requires no additional CI changes beyond T002 and T003. The `TestDocumentationExamples_ParseCorrectly` test reads from `docs/products/examples/` which is already populated with all four example files.

- [x] T004 [P] [US3] Verify `docs/products/examples/valid-product.md` uses the `apiVersion: catalog.gitstore.dev/v1beta1` Kubernetes-style schema (not the old flat YAML format) — read the file and confirm it matches the `validProductFixture` format used by `TestProductLifecycle_ValidFile_AcceptedAndQueryable`
- [x] T005 [P] [US3] Verify `docs/products/examples/invalid-status.md` contains a top-level `status:` key and is rejected with `system-managed` in the error — read the file and confirm it matches `invalidStatusFixture`
- [x] T006 [P] [US3] Verify `docs/products/examples/invalid-title.md` contains `spec.title` longer than 200 characters and is rejected with `200` in the error — read the file and confirm it matches `invalidTitleFixture`
- [x] T007 [P] [US3] Verify `docs/products/examples/invalid-media.md` contains a media entry missing `fileRef.name` and is rejected with `fileref` in the error — read the file and confirm it matches `invalidMediaFixture`

**Checkpoint**: All four example files have the correct content for the test assertions. The full `TestDocumentationExamples_ParseCorrectly` suite passes in both CI jobs.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [x] T008 Update `specs/020-pre-receive-validation-e2e/checklists/requirements.md` — mark all items checked now that the spec is implemented
- [x] T009 Run `make pr-ready` locally and confirm all checks pass (Go tests, lint, Rust tests, license headers)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 — blocks all user story work
- **US1 (Phase 3)**: Depends on Phase 2 completion
- **US2 (Phase 4)**: Depends on Phase 2 completion — shares infrastructure with US1; T003 (the ScyllaDB job) covers both
- **US3 (Phase 5)**: Depends on Phase 2 completion — T004–T007 are independent verification tasks
- **Polish (Phase 6)**: Depends on all user story phases

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 2
- **US2 (P1)**: Can start after Phase 2; shares T003 with US1
- **US3 (P2)**: Can start after Phase 2; T004–T007 are independent of US1/US2

### Within Each User Story

- T003 (the ScyllaDB job) serves US1, US2, and US3 on ScyllaDB — it is the one implementation task in this spec besides T002

### Parallel Opportunities

- T004, T005, T006, T007 (US3 verification) can all run in parallel — each reads a different file
- T002 and the phase-3 task T003 are both edits to the same file and must be sequential (T002 before T003)

---

## Parallel Example: User Story 3 verification

```bash
# These four tasks can run in parallel:
Task: "Verify docs/products/examples/valid-product.md schema"
Task: "Verify docs/products/examples/invalid-status.md content"
Task: "Verify docs/products/examples/invalid-title.md content"
Task: "Verify docs/products/examples/invalid-media.md content"
```

---

## Implementation Strategy

### MVP First (US1 + US2)

1. Complete Phase 1: confirm branch state
2. Complete Phase 2: add port-6000 readiness check → **all 10 tests now pass on memdb**
3. Complete Phase 3: add ScyllaDB job → **all 10 tests now pass on ScyllaDB**
4. **STOP and VALIDATE**: Push branch; confirm both CI jobs go green
5. US1 and US2 are fully satisfied

### Incremental Delivery

1. T001 + T002 → memdb integration tests go green (US1 + US2 + US3 on memdb) → demo-able
2. T003 → ScyllaDB coverage added (US1 + US2 + US3 on ScyllaDB) → full scope delivered
3. T004–T007 → documentation example files verified → US3 fully signed off
4. T008 + T009 → polish and PR readiness

### Parallel Team Strategy

With the entire change in one file, parallelism is limited to the verification tasks (T004–T007) in Phase 5, which can be done concurrently while T003 is being reviewed in CI.

---

## Notes

- [P] tasks = different files or independent verifications; no dependencies between them
- T002 is the single highest-leverage change — one line in CI unblocks 10 failing tests on memdb
- T003 extends coverage to ScyllaDB — the compose overlay is already complete; the task is purely additive YAML in the workflow file
- Avoid editing `compose.yml`, `compose.scylla.yml`, `tests/integration/`, or any service code — research confirms no changes are needed there
- Commit after T002 and T003 separately to keep the git history bisectable

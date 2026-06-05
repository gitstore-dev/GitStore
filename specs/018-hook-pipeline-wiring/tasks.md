# Tasks: Hook Pipeline Wiring — Pre-Receive Validation and Post-Receive Admission

**Input**: Design documents from `/specs/018-hook-pipeline-wiring/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and verified FAILING before implementation begins.

**Organization**: Tasks grouped by user story. US1 (pre-receive validation) and US2 (post-receive admission) are both P1 and independently deliverable. US3 (latency budget) is P2 and builds on US1.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)

---

## Phase 1: Setup (Proto & Codegen)

**Purpose**: Establish the `gitstore.catalog.v1` API-first contract and generate stubs for both services before any implementation begins (Constitution Principle II).

- [X] T001 Create `shared/proto/gitstore/catalog/v1/catalog_service.proto` from `specs/018-hook-pipeline-wiring/contracts/catalog_service.proto`
- [X] T002 [P] Update `buf.gen.go.yaml` to include `gitstore/catalog/v1/catalog_service.proto`; output path `gitstore-api/gen`
- [X] T003 [P] Update `buf.gen.rust.yaml` to include `gitstore/catalog/v1/catalog_service.proto`; output path `gitstore-git-service/gen`
- [X] T004 Run `buf generate` from repo root to produce Go stubs in `gitstore-api/gen/gitstore/catalog/v1/`
- [X] T005 Update `gitstore-git-service/build.rs` to enable `build_client = true` for the new catalog proto (keep `build_server = true` for the existing git service proto)

**Checkpoint**: `gitstore-api/gen/gitstore/catalog/v1/` and `gitstore-git-service/gen/` contain generated stubs. Both services compile with new proto types.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Restructure the config, `HookPipeline`, and startup validation so both US1 and US2 handler slots exist before either is wired up. These tasks block all user story phases.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T006 Restructure `gitstore-git-service/src/config.rs`: remove `AdmissionControlConfig.validating_admission_policy`; add `SchemaValidationConfig { phase: String, timeout_secs: u64 }` (default phase `"pre-receive"`, timeout `10`); replace `AdmissionControlConfig` with `{ phase: String, branch_pattern: String }` (defaults `"post-receive"`, `"refs/heads/main"`); add `CatalogServiceConfig { url: String }` (default `"http://localhost:4000"`); update `default_toml()` to reflect new structure
- [X] T007 Add startup phase-conflict validation to `AppConfig::validate()` in `gitstore-git-service/src/config.rs`: if `schema_validation.phase == admission_control.phase`, push an error naming both env vars (FR-019); add config unit tests covering the conflict case and the valid split-phase case
- [X] T008 Restructure `gitstore-git-service/src/git/hooks.rs` `HookPipeline`: replace single `admission_phase` + `admission_handler` with two independent slots — `schema_validation_phase: String`, `schema_validation_timeout: Duration`, `validation_handler: Arc<dyn ValidationHandler>`, `admission_control_phase: String`, `admission_branch_pattern: String`, `admission_handler: Arc<dyn AdmissionHandler>`, `repository_id: String`; update `HookPipeline::new()` constructor and `run()` call sites to compile (stubs for both new handler traits may return `todo!()` at this stage)

**Checkpoint**: `cargo build` succeeds for `gitstore-git-service`. Config tests pass. `HookPipeline` compiles with two slots (traits stubbed).

---

## Phase 3: User Story 1 — Catalog Author Receives Validation Errors on Push (Priority: P1) 🎯 MVP

**Goal**: Invalid product pushes are rejected before any refs are updated, with field-scoped error messages visible in the git client's stderr output.

**Independent Test**: `TestProductLifecycle_InvalidTitle_PushRejected`, `TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected` in `tests/integration/product_lifecycle_test.go` — currently failing (RED). All must pass GREEN after this phase.

### Tests for User Story 1 (write FIRST — verify FAILING before implementation)

- [X] T009 [P] [US1] Write unit tests for `ResourceBlob` extraction logic in `gitstore-git-service/src/git/hooks/mod.rs` test module: (a) file beginning with `---` is extracted as a `ResourceBlob`; (b) file without `---` prefix is silently skipped; (c) `RefUpdate` with `old_oid` all-zeros (new branch creation) is treated identically to a regular update (FR-020); (d) empty commit tree produces zero blobs
- [X] T010 [P] [US1] Write unit tests for `SchemaValidationHandler` in `gitstore-git-service/src/git/hooks/validation_handler.rs`: (a) mock gRPC server returning `accepted=true` → handler returns `AdmissionDecision::Accept`; (b) mock returning `accepted=false` with two `ValidationError`s → handler returns `Reject` with aggregated message; (c) timeout fires before response → handler returns `Reject("validation service unavailable")`; (d) transport error → handler returns `Reject`
- [X] T011 [P] [US1] Write unit tests for `CatalogServiceServer::validate_resources` in `gitstore-api/internal/cataloggrpc/server_test.go`: (a) blob with valid frontmatter → `accepted=true`, empty errors; (b) blob with `status:` key → `accepted=false`, error names `status` and `system-managed`; (c) blob with `spec.title` > 200 chars → `accepted=false`, error names `spec.title` and limit; (d) two blobs one valid one invalid → `accepted=false`, only invalid blob produces errors; (e) blob without `---` prefix → treated as no-op, no error

### Implementation for User Story 1

- [X] T012 [US1] Add `ResourceBlob` struct, `ValidationHandler` trait, and `NoopValidationHandler` impl to `gitstore-git-service/src/git/hooks/mod.rs`
- [X] T013 [US1] Implement gix tree-walk blob extraction in `gitstore-git-service/src/git/hooks/mod.rs`
- [X] T014 [US1] Implement `SchemaValidationHandler` in `gitstore-git-service/src/git/hooks/validation_handler.rs`
- [X] T015 [P] [US1] Add `gitstore_schema_validation_total` counter to `gitstore-git-service/src/git/metrics.rs`
- [X] T016 [US1] Create `gitstore-api/internal/cataloggrpc/server.go` and implement `ValidateResources`
- [X] T017 [US1] Register `CatalogServiceServer` on gRPC listener in `gitstore-api/cmd/server/main.go`
- [X] T018 [US1] Wire `SchemaValidationHandler` into `HookPipeline` in `gitstore-git-service/src/main.rs`

**Checkpoint**: `cargo test` and `go test ./...` pass. Run `tests/integration/product_lifecycle_test.go` against a live stack — `TestProductLifecycle_InvalidTitle_PushRejected`, `TestProductLifecycle_StatusPresent_PushRejected`, `TestProductLifecycle_MissingFileRefName_PushRejected` are GREEN.

---

## Phase 4: User Story 2 — Operator Queries a Product Immediately After Push (Priority: P1)

**Goal**: A valid pushed product is stored in the catalog and queryable via GraphQL within 5 seconds of the push completing.

**Independent Test**: `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and `TestProductLifecycle_StatusHydration` in `tests/integration/product_lifecycle_test.go` — currently failing (RED). Both must pass GREEN after this phase.

### Tests for User Story 2 (write FIRST — verify FAILING before implementation)

- [X] T019 [P] [US2] Write unit tests for `AdmissionControlHandler` in `gitstore-git-service/src/git/hooks/admission_handler.rs`
- [X] T020 [P] [US2] Write unit tests for `CatalogServiceServer::admit_resources` in `gitstore-api/internal/cataloggrpc/server_test.go`

### Implementation for User Story 2

- [X] T021 [US2] Implement `AdmissionControlHandler` in `gitstore-git-service/src/git/hooks/admission_handler.rs`
- [X] T022 [US2] Wire `AdmissionControlHandler` into `HookPipeline` in `gitstore-git-service/src/main.rs`
- [X] T023 [US2] Implement `CatalogServiceServer::admit_resources` in `gitstore-api/internal/cataloggrpc/server.go`
- [X] T024 [US2] Add `AdmissionAccepted: True` status condition write to `admit_resources`

**Checkpoint**: `cargo test` and `go test ./...` pass. Run integration tests — `TestProductLifecycle_ValidFile_AcceptedAndQueryable` and `TestProductLifecycle_StatusHydration` are GREEN. All four rejection tests from Phase 3 remain GREEN.

---

## Phase 5: User Story 3 — Push Performance Within Latency Budget (Priority: P2)

**Goal**: Pre-receive validation completes in < 5 seconds for a 100-file push; unreachable service is rejected within configured timeout; author is never blocked indefinitely.

**Independent Test**: `TestDocumentationExamples_ParseCorrectly` and the push of 100 valid files must complete in under 5 seconds (SC-002). Manual benchmark via quickstart.md.

### Tests for User Story 3 (write FIRST — verify FAILING before implementation)

- [X] T025 [P] [US3] Write unit tests for service-unavailable timeout path in `gitstore-git-service/src/git/hooks/validation_handler.rs`
- [X] T026 [US3] Thread `schema_validation.timeout_secs` from config through to `SchemaValidationHandler` in `gitstore-git-service/src/main.rs`
- [X] T027 [US3] Verify SC-002 manually: 100-file push completed in 0.158 s on local stack (target: < 5 s); timing documented in quickstart.md

**Checkpoint**: Timeout unit tests GREEN. Manual 100-file push benchmark passes SC-002 target (< 5 seconds).

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T028 [P] Update `docs/` with push validation and admission pipeline documentation: `docs/products/push-validation.md`
- [X] T029 Run `make pr-ready` from repo root; all checks pass

**Checkpoint**: `make pr-ready` exits 0. All integration tests GREEN. All unit tests GREEN.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 (needs generated stubs to compile) — **BLOCKS** all user story phases
- **Phase 3 (US1)**: Depends on Phase 2 completion
- **Phase 4 (US2)**: Depends on Phase 2 completion — US1 and US2 can proceed in parallel after Phase 2
- **Phase 5 (US3)**: Depends on Phase 3 (timeout wiring builds on SchemaValidationHandler from US1)
- **Phase 6 (Polish)**: Depends on all desired user stories complete

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 2 — no dependency on US2
- **US2 (P1)**: Can start after Phase 2 — no dependency on US1; `AdmitResources` Go impl adds a method to the server created in T016, so T016 must complete before T023
- **US3 (P2)**: Depends on US1 (T026 extends SchemaValidationHandler from T014)

### Within Each User Story

1. Test tasks `[P]` written and verified FAILING first
2. Trait/struct definitions before concrete implementations
3. Blob extraction (T013) before validation handler (T014) — handler consumes blobs
4. Go server implementation (T016) before gRPC registration (T017)
5. Handler implementation before wiring into `main.rs`
6. `main.rs` wiring before integration test verification

---

## Parallel Opportunities

### Phase 1

```
T002 (buf.gen.go.yaml)  ─┐
T003 (buf.gen.rust.yaml) ─┤─→ T004 (buf generate) → T005 (build.rs)
```

### Phase 3 (US1)

```
T009 (blob extraction tests)         ─┐
T010 (SchemaValidationHandler tests)  ─┤─→ [implementations in order]
T011 (ValidateResources Go tests)     ─┘

T015 (metrics counter) runs alongside any implementation task — different file
```

### Phase 4 (US2)

```
T019 (AdmissionControlHandler tests)  ─┐
T020 (AdmitResources Go tests)        ─┘─→ [implementations in order]
```

### Phase 3 + Phase 4 in parallel (two developers)

```
Dev A: T009 → T012 → T013 → T014 → T015 → T016 → T017 → T018  (US1)
Dev B: T019 → T021 → T022 → T023 → T024                        (US2, after T016)
```

---

## Implementation Strategy

### MVP First (US1 Only — push rejection working)

1. Complete Phase 1: Proto + codegen
2. Complete Phase 2: Foundational config + HookPipeline structure
3. Complete Phase 3: US1 — pre-receive rejection
4. **STOP and VALIDATE**: `TestProductLifecycle_InvalidTitle_PushRejected` etc. are GREEN
5. Invalid pushes are now blocked in production — the core safety guarantee is live

### Incremental Delivery

1. Phase 1 + 2 → Foundation ready
2. Phase 3 (US1) → Push rejection works; catalog authors see field-scoped errors
3. Phase 4 (US2) → Valid pushes stored; catalog is queryable — full push-to-query lifecycle
4. Phase 5 (US3) → Latency and timeout guarantees enforced
5. Phase 6 → Polish, docs, PR-ready

### Parallel Team Strategy (two developers)

1. Both complete Phase 1 + 2 together
2. Dev A → Phase 3 (US1 Rust + Go)
3. Dev B → Phase 4 (US2 Rust + Go); note: T023 depends on T016 from Dev A
4. Either → Phase 5 + 6

---

## Notes

- `[P]` test tasks within a phase can all be written simultaneously — they are in different files
- Existing integration tests in `tests/integration/product_lifecycle_test.go` are already RED (failing) — this is the TDD red phase; no new integration test files are needed for the core lifecycle
- `T016` (Go `ValidateResources`) and `T023` (Go `AdmitResources`) are methods on the same struct in the same file — T023 must follow T016
- The `revision` field format `main@sha1:<sha>` is encoded in T023 — no separate task needed
- `NoopValidationHandler` (T012) replaces `NoopAdmissionHandler` in the validation slot; the existing `NoopAdmissionHandler` continues to serve as the default for the admission slot until T021 wires the real handler
- SC-004 (all integration tests pass) is validated implicitly by the checkpoints at the end of Phase 3, Phase 4, and Phase 5

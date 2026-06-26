# Tasks: Pluggable AuthN/AuthZ — Phase 4 gRPC HMAC Inter-Service Authentication

**Input**: Design documents from `/specs/033-auth-phase-4/`
**Prerequisites**: plan.md ✅, spec.md ✅, research.md ✅, data-model.md ✅, contracts/ ✅, quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation code and MUST fail before implementation begins.

**Organization**: Tasks grouped by user story (US1 = reject unauthorized callers, US2 = transparent injection, US3 = rotation window). All three stories depend on the same foundational changes (config + binary rename) so Phase 2 is strictly prerequisite.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no shared dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)

---

## Phase 1: Setup

**Purpose**: Rename `cmd/hashpw` → `cmd/gitctl` and add `gitctl gen-hmac-secret` / `gen-jwt-secret` subcommands. This is a pure binary rename — no runtime behavior changes yet.

- [x] T001 Delete `gitstore-api/cmd/hashpw/` directory (the Go source file `main.go` is replaced by the new binary)
- [x] T002 Create `gitstore-api/cmd/gitctl/main.go` with AGPL license header, CLI entry point, and three subcommands: `hash-password`, `gen-jwt-secret`, `gen-hmac-secret`
- [x] T003 Implement `hash-password` subcommand in `gitstore-api/cmd/gitctl/main.go` — identical behaviour to old `hashpw`: `bcrypt.GenerateFromPassword([]byte(arg), bcrypt.DefaultCost)` printed to stdout
- [x] T004 [P] Implement `gen-jwt-secret` subcommand in `gitstore-api/cmd/gitctl/main.go` — `crypto/rand` 32 bytes → `base64.URLEncoding.WithPadding(base64.NoPadding)`, print `GITSTORE_AUTH__JWT__SECRET=<val>` to stdout
- [x] T005 [P] Implement `gen-hmac-secret` subcommand in `gitstore-api/cmd/gitctl/main.go` — same generation logic as T004, print `GITSTORE_AUTH__GRPC__HMAC_SECRET=<val>` to stdout
- [x] T006 Update `Makefile` `gen-admin-password` target: replace `go run ./cmd/hashpw` with `go run ./cmd/gitctl hash-password`
- [x] T007 Add `Makefile` target `gen-jwt-secret` — runs `cd $(API_DIR) && go run ./cmd/gitctl gen-jwt-secret >> .env` with usage guard if `.env` absent
- [x] T008 Add `Makefile` target `gen-hmac-secret` — runs `cd $(API_DIR) && go run ./cmd/gitctl gen-hmac-secret >> .env` with usage guard if `.env` absent
- [x] T009 Update `Makefile` `.PHONY` line to include `gen-jwt-secret gen-hmac-secret`

**Checkpoint**: `go build ./cmd/gitctl` succeeds; `go run ./cmd/gitctl hash-password testpass` prints a bcrypt hash; `make gen-admin-password ADMIN_PASSWORD=test` still works.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Config struct changes and the new `hmacCreds` Go type. These are required by all three user stories and must land before any interceptor or client credential work begins.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T010 Add `AuthConfig` and `GrpcAuthConfig` structs to `gitstore-git-service/src/config.rs` (AGPL header already present); add `pub auth: AuthConfig` field to `AppConfig`; add `hmac_secret: String` (required) and `hmac_secret_previous: Option<String>` (optional) fields per `data-model.md`
- [x] T011 Add `auth.grpc.hmac_secret` validation to `AppConfig::validate()` in `gitstore-git-service/src/config.rs` — push error `"auth.grpc.hmac_secret must not be empty"` when field is empty; existing `ConfigErrors` pattern applies
- [x] T012 Add TOML default `[auth.grpc]` section with `hmac_secret = ""` to the `default_toml()` inline string in `gitstore-git-service/src/config.rs` so `load_config_from(None)` does not panic on missing field; the validation step (T011) will catch the empty value at startup
- [x] T013 Add `HmacSecret string` field (`mapstructure:"hmac_secret"`) to `GitEndpointConfig` in `gitstore-api/internal/config/config.go`
- [x] T014 Add `"auth.grpc.hmac_secret"` to the `requiredKeys` map in `gitstore-api/internal/config/config.go` so the API fails to start with a clear error when the key is absent
- [x] T015 Add Viper default and env binding for `auth.grpc.hmac_secret` in `gitstore-api/internal/config/config.go` (no default value — required key; add only the `v.BindEnv` / env var name so Viper reads `GITSTORE_AUTH__GRPC__HMAC_SECRET`)
- [x] T016 Create `gitstore-api/internal/gitclient/auth.go` with AGPL header — implement `hmacCreds` struct satisfying `google.golang.org/grpc/credentials.PerRPCCredentials`: `GetRequestMetadata` returns `map[string]string{"authorization": "Bearer " + c.token}`, `RequireTransportSecurity` returns `false`

**Checkpoint**: `go build ./...` (gitstore-api) and `cargo build` (gitstore-git-service) both pass; `AppConfig::validate()` unit test fails on empty `hmac_secret` (test to be added in T017).

---

## Phase 3: User Story 1 — Git service rejects callers without the shared secret (Priority: P1) 🎯 MVP

**Goal**: Every inbound gRPC call to the git service that lacks the correct bearer token is rejected before any handler runs.

**Independent Test**: Start git service with `GITSTORE_AUTH__GRPC__HMAC_SECRET=test-secret`; send a raw gRPC call with no `Authorization` header → `UNAUTHENTICATED`; send with wrong token → `UNAUTHENTICATED`; send with correct token → call proceeds.

### Tests for User Story 1

> **Write these tests FIRST — they MUST fail before T021 is implemented**

- [x] T017 [P] [US1] Add unit test `test_validate_hmac_secret_empty_fails` to `gitstore-git-service/src/config.rs` `#[cfg(test)]` block — set `hmac_secret = ""` via env var, call `cfg.validate()`, assert `Err` containing `"auth.grpc.hmac_secret"` (follows existing T020/T007 pattern)
- [x] T018 [P] [US1] Add unit test `test_validate_hmac_secret_nonempty_passes` to `gitstore-git-service/src/config.rs` — set `hmac_secret = "some-secret"`, assert `cfg.validate()` returns `Ok(())`
- [x] T019 [P] [US1] Add unit test `test_hmac_interceptor_rejects_missing_header` to `gitstore-git-service/src/auth/interceptor.rs` — construct `HmacInterceptor::new("secret", None)`, call `interceptor.call(Request::new(()))` with no metadata, assert `Err(Status::unauthenticated(_))`
- [x] T020 [P] [US1] Add unit test `test_hmac_interceptor_rejects_wrong_token` to `gitstore-git-service/src/auth/interceptor.rs` — same interceptor, attach `authorization: "Bearer wrong"` metadata, assert `Err(Status::unauthenticated(_))`
- [x] T021 [P] [US1] Add unit test `test_hmac_interceptor_accepts_correct_token` to `gitstore-git-service/src/auth/interceptor.rs` — attach `authorization: "Bearer secret"`, assert `Ok(_)`

### Implementation for User Story 1

- [x] T022 [US1] Create `gitstore-git-service/src/auth/mod.rs` with AGPL header — declare `pub mod interceptor;`
- [x] T023 [US1] Create `gitstore-git-service/src/auth/interceptor.rs` with AGPL header — implement `HmacInterceptor { secret: Arc<str>, secret_previous: Option<Arc<str>> }` with `fn new(secret: &str, previous: Option<&str>) -> Self` constructor; implement `tonic::service::Interceptor` trait: extract `authorization` metadata, strip `"Bearer "` prefix, compare using `==` (string equality is sufficient per research Decision 3); return `Status::unauthenticated("missing inter-service token")` when header absent, `Status::unauthenticated("invalid inter-service token")` when token wrong, `Ok(req)` on match
- [x] T024 [US1] Add `pub mod auth;` to `gitstore-git-service/src/lib.rs` so the module is reachable from `main.rs`
- [x] T025 [US1] Update `gitstore-git-service/src/main.rs`: read `cfg.auth.grpc.hmac_secret` and `cfg.auth.grpc.hmac_secret_previous` after `cfg.validate()`; construct `HmacInterceptor::new(&cfg.auth.grpc.hmac_secret, cfg.auth.grpc.hmac_secret_previous.as_deref())`; replace `GitServiceServer::new(grpc_service)` with `GitServiceServer::with_interceptor(grpc_service, interceptor)`; emit `info!("gRPC HMAC auth active", rotation_window_open = cfg.auth.grpc.hmac_secret_previous.is_some())` after construction

**Checkpoint**: `cargo test` passes (T017–T021 now green); starting git service without `GITSTORE_AUTH__GRPC__HMAC_SECRET` exits with config error; a raw `grpcurl` call without the token returns `UNAUTHENTICATED`.

---

## Phase 4: User Story 2 — API transparently attaches the secret to all gRPC calls (Priority: P1)

**Goal**: Every outbound gRPC call from `gitstore-api` to `gitstore-git-service` carries the bearer token automatically. No resolver or service code changes required.

**Independent Test**: Run `make bootstrap` with matching `GITSTORE_AUTH__GRPC__HMAC_SECRET` on both services; bootstrap must complete without any `unauthenticated` gRPC errors.

### Tests for User Story 2

> **Write these tests FIRST — they MUST fail before T031 is implemented**

- [x] T026 [P] [US2] Add unit test `test_hmac_creds_get_request_metadata` to `gitstore-api/internal/gitclient/auth_test.go` — construct `hmacCreds{token: "mysecret"}`, call `GetRequestMetadata(context.Background())`, assert returned map contains `"authorization": "Bearer mysecret"`
- [x] T027 [P] [US2] Add unit test `test_hmac_creds_require_transport_security_false` to `gitstore-api/internal/gitclient/auth_test.go` — assert `hmacCreds{}.RequireTransportSecurity()` returns `false`
- [x] T028 [P] [US2] Add unit test for API config validation to `gitstore-api/internal/config/config_test.go` — load config without `GITSTORE_AUTH__GRPC__HMAC_SECRET` set, call `cfg.Validate()`, assert error contains `"auth.grpc.hmac_secret"` (or whichever error string the required key validation produces)

### Implementation for User Story 2

- [x] T029 [US2] Create `gitstore-api/internal/gitclient/auth_test.go` with AGPL header — file stub for tests T026, T027 in `package gitclient`
- [x] T030 [US2] Update `gitstore-api/internal/gitclient/grpc_client.go`: add `grpc.WithPerRPCCredentials(hmacCreds{token: hmacSecret})` dial option to `NewClientWithAddr`; add `hmacSecret string` parameter to `NewClientWithAddr` (or introduce `NewClientWithAddrAndSecret(addr, hmacSecret string) (*Client, error)` keeping the old signature for zero-secret callers in tests)
- [x] T031 [US2] Update `gitstore-api/internal/app/server.go`: pass `cfg.Git.Grpc.HmacSecret` when constructing the gRPC client (update the `NewClientWithAddr` call at line 90 to supply the secret)

**Checkpoint**: `go test ./...` passes (T026–T028 now green); `make bootstrap` succeeds end-to-end with matching secrets on both services.

---

## Phase 5: User Story 3 — HMAC secret rotation without service outage (Priority: P2)

**Goal**: The git service accepts both the current and the previous HMAC secret during a rolling deployment window.

**Independent Test**: Configure git service with `hmac_secret = "new"` and `hmac_secret_previous = "old"`; send request with old token → accepted; send with new token → accepted; remove `hmac_secret_previous`; send with old token → `UNAUTHENTICATED`.

### Tests for User Story 3

> **Write these tests FIRST — they MUST fail before T034–T035 pass**

- [x] T032 [P] [US3] Add unit test `test_hmac_interceptor_accepts_previous_token` to `gitstore-git-service/src/auth/interceptor.rs` — construct `HmacInterceptor::new("new-secret", Some("old-secret"))`, attach `authorization: "Bearer old-secret"`, assert `Ok(_)` (rotation window open)
- [x] T033 [P] [US3] Add unit test `test_hmac_interceptor_rejects_old_token_after_window_closed` to `gitstore-git-service/src/auth/interceptor.rs` — construct `HmacInterceptor::new("new-secret", None)`, attach `authorization: "Bearer old-secret"`, assert `Err(Status::unauthenticated(_))` (window closed)

### Implementation for User Story 3

- [x] T034 [US3] Ensure `HmacInterceptor::call` in `gitstore-git-service/src/auth/interceptor.rs` checks `secret_previous` branch (this should already be handled by T023; verify the `if let Some(prev) = &self.secret_previous { token == prev.as_ref() }` path is exercised by T032)
- [x] T035 [US3] Verify `gitstore-git-service/src/config.rs` env-var round-trip: add unit test `test_hmac_secret_previous_env_var` — set `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS=old`, assert `cfg.auth.grpc.hmac_secret_previous == Some("old".to_string())`

**Checkpoint**: All five interceptor tests pass (T019–T021, T032–T033); `cargo test` green; rotation scenario documented in `quickstart.md` is manually verifiable.

---

## Phase 6: CI & Compose Wiring

**Purpose**: Propagate the new secret through Docker Compose and the integration CI jobs so all existing CI workflows continue to pass.

- [x] T036 Update `compose.yml` git-service `environment:` block — add `GITSTORE_AUTH__GRPC__HMAC_SECRET=${GITSTORE_AUTH__GRPC__HMAC_SECRET}` (passthrough from host env; the git service now requires this key)
- [x] T037 Update `compose.yml` api `environment:` block — add `GITSTORE_AUTH__GRPC__HMAC_SECRET=${GITSTORE_AUTH__GRPC__HMAC_SECRET}` (API must send the matching token)
- [x] T038 Update `.github/workflows/ci-integration.yml` `integration-test` (memdb) job — add `GITSTORE_AUTH__GRPC__HMAC_SECRET: ci-test-grpc-hmac-secret` to the `docker compose up -d --build` step's `env:` block
- [x] T039 Update `.github/workflows/ci-integration.yml` `integration-test-scylla` job — same addition as T038 to its `docker compose … up -d --build` step `env:` block
- [x] T040 Update `.github/workflows/ci-integration.yml` `grpc-contract-test` job — add `GITSTORE_AUTH__GRPC__HMAC_SECRET: ci-test-grpc-hmac-secret` as an env var passed to the docker build step and/or the `go test` step so testcontainers-based tests supply the secret when starting the git service container and when constructing the gRPC client

**Checkpoint**: Pushing the branch triggers `ci-integration.yml`; all four jobs (`integration-test`, `integration-test-scylla`, `datastore-contract-test`, `grpc-contract-test`) pass. `ci.yml` `build-status` gate remains green.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [x] T041 [P] Update `docs/implementation/pluggable_auth_architecture.md` — mark Phase 4 milestone `auth-framework-git-v1` as complete in §7 Rollout Phases
- [x] T042 [P] Update `CLAUDE.md` / `AGENTS.md` `## Recent Changes` section to record Phase 4 tech additions (no new deps; `cmd/gitctl` binary; `GITSTORE_AUTH__GRPC__HMAC_SECRET` config key)
- [x] T043 Run `make pr-ready` and fix any lint, format, or license-header failures before opening the PR

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Phase 1 completion — BLOCKS all user stories
- **US1 (Phase 3)**: Depends on Phase 2 — interceptor + config validation
- **US2 (Phase 4)**: Depends on Phase 2 — `hmacCreds` + API config wiring
- **US3 (Phase 5)**: Depends on Phase 3 (rotation window is part of the interceptor implementation)
- **CI & Compose (Phase 6)**: Depends on Phases 3 + 4 (both sides must be wired before compose/CI changes are meaningful)
- **Polish (Phase 7)**: Depends on Phase 6

### User Story Dependencies

- **US1 (P1)**: Can start after Phase 2 — independent of US2/US3
- **US2 (P1)**: Can start after Phase 2 — independent of US1 (different files: Rust interceptor vs Go client)
- **US3 (P2)**: Depends on US1 (rotation window is a property of the same `HmacInterceptor`)

### Parallel Opportunities

- T004 and T005 (`gen-jwt-secret` / `gen-hmac-secret` subcommands) can be written in parallel — same file, different functions; coordinate to avoid conflicts or implement sequentially
- T010–T012 (Rust config) can proceed in parallel with T013–T016 (Go config + `hmacCreds`) — different repos
- T017–T021 (Rust interceptor tests) can all be written in parallel — same file, different test functions
- T026–T028 (Go auth/config tests) can be written in parallel — different files
- T036–T037 (`compose.yml`) must be done sequentially (same file); T038–T040 (CI workflow) are all in the same file — do sequentially

---

## Parallel Example: Phase 2 Foundational

```bash
# Rust side (gitstore-git-service) and Go side (gitstore-api) are fully independent:
Task: T010–T012 in gitstore-git-service/src/config.rs
Task: T013–T016 in gitstore-api/internal/config/config.go + gitstore-api/internal/gitclient/auth.go
```

## Parallel Example: Phase 3 (US1) Tests

```bash
# All five interceptor unit tests can be written in the same pass:
Task: T017 — validate empty hmac_secret fails config validation
Task: T018 — validate non-empty hmac_secret passes
Task: T019 — interceptor rejects missing header
Task: T020 — interceptor rejects wrong token
Task: T021 — interceptor accepts correct token
```

---

## Implementation Strategy

### MVP First (US1 + US2 Only)

1. Complete Phase 1: Setup (gitctl binary rename)
2. Complete Phase 2: Foundational (config structs on both sides)
3. Complete Phase 3: US1 (git service interceptor)
4. Complete Phase 4: US2 (Go client credentials)
5. Complete Phase 6: CI & Compose wiring
6. **STOP and VALIDATE**: Run `make bootstrap` end-to-end; run `cargo test` + `go test ./...`; verify `grpcurl` rejection

### Full Delivery (adds US3)

7. Complete Phase 5: US3 (rotation window)
8. Complete Phase 7: Polish

### Notes

- Constitution Principle I is enforced: every implementation task (T022–T025, T030–T031, T034–T035) has a corresponding failing test written first
- The `cmd/hashpw` deletion (T001) and `cmd/gitctl` creation (T002–T005) must land in the same commit so `go build ./...` stays green throughout
- `ci-admin.yml` is intentionally excluded — it is currently failing due to an incomplete admin implementation and is not a required branch-protection check
- All new `.go` and `.rs` files require the AGPL-3.0 license header or the `go-license-headers` / `rust-license-headers` CI job will fail

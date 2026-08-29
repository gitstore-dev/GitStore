# Tasks: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

**Input**: Design documents from `/specs/059-optional-oidc-provider/`
**Prerequisites**: plan.md (required), spec.md (required for user stories), data-model.md, contracts/, research.md, quickstart.md

**Tests**: Test-first development is required for the `gitstore-oidc-bridge` route handlers.

**Organization**: Tasks are grouped by user story to enable independent implementation and validation.

## Format: `[ID] [P?] [Story] Description with exact file path`

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the new optional service and deployment surfaces without touching any existing service.

- [ ] T001 Create the `gitstore-oidc-bridge/` Go module (`go.mod`, `cmd/bridge/main.go` stub) and the `deploy/oidc/` directory layout
- [ ] T002 [P] Add the Kratos identity schema `deploy/oidc/kratos/identity.schema.json` per `contracts/kratos-identity-schema.md`
- [ ] T003 [P] Add Hydra serve config `deploy/oidc/hydra/config.yaml` and Kratos serve config `deploy/oidc/kratos/kratos.yml`, referencing the identity schema from T002

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Land the bridge's config/HTTP scaffolding and Hydra/Kratos client wrappers before route-handler work begins.

**Checkpoint**: The bridge can start, serve `/healthz`, and reach both Admin APIs; US1–US5 can proceed.

- [ ] T004 Add `GITSTORE_OIDC_BRIDGE__*` Viper config schema in `gitstore-oidc-bridge/internal/config/config.go` per `contracts/oidc-bridge-routes.md`, with a failing startup-validation test written first
- [ ] T005 [P] Add `github.com/ory/client-go`-based Hydra Admin API wrapper in `gitstore-oidc-bridge/internal/hydraclient/client.go`
- [ ] T006 [P] Add `github.com/ory/client-go`-based Kratos public/admin API wrapper in `gitstore-oidc-bridge/internal/kratosclient/client.go`
- [ ] T007 Add `GET /healthz` handler in `gitstore-oidc-bridge/internal/bridge/health.go` and wire the HTTP server in `gitstore-oidc-bridge/cmd/bridge/main.go`

---

## Phase 3: User Story 1 - An operator without their own OIDC IdP can stand up a working identity provider in one step (Priority: P1) 🎯 MVP

**Goal**: Bringing up the reference stack alone yields a standards-compliant OIDC issuer, with zero `gitstore-api`/`gitstore-git-service`/`gitstore-controller-manager` source changes required to point at it.

**Independent Test**: Bring up `compose.oidc.yml` alone; fetch `/.well-known/openid-configuration` and the JWKS it references; confirm both are well-formed and require no core-service change to be consumable.

### Tests for User Story 1

- [ ] T008 [P] [US1] Add a manual verification checklist entry (this is infra, not a Go unit test) confirming `docker compose -f compose.oidc.yml up hydra-postgres hydra-migrate hydra` alone exposes a valid discovery document and JWKS

### Implementation for User Story 1

- [ ] T009 [US1] Add `hydra-postgres`, `hydra-migrate`, `hydra` services to `compose.oidc.yml`, publishing only Hydra's public API port (mirroring `data-model.md`'s network topology table)
- [ ] T010 [US1] Confirm (documentation-only change, no code) that `docs/implementation/020-pluggable_auth_architecture.md` §5a's existing `auth.oidc.issuer_url`/`client_id`/`audience`/`clock_skew` keys need no modification to point at this stack's issuer

**Checkpoint**: A bare Hydra+Postgres stack exposes a working OIDC discovery document with no bridge or Kratos involvement yet.

---

## Phase 4: User Story 2 - A new user can self-register and log in through Kratos without any custom UI (Priority: P1)

**Goal**: Kratos's own self-service registration/login flows work against the GitStore identity schema with no custom UI built.

**Independent Test**: Complete Kratos's self-service registration flow for a new identity carrying `email`+`username`, then complete login for it, using only Kratos's own browser UI.

### Tests for User Story 2

- [ ] T011 [P] [US2] Add a manual verification checklist entry confirming self-service registration against `identity.schema.json` (T002) produces an identity with both required traits populated

### Implementation for User Story 2

- [ ] T012 [US2] Add `kratos-postgres`, `kratos-migrate`, `kratos`, `mailslurper` services to `compose.oidc.yml`, publishing only Kratos's public API port and mailslurper's dev UI (Admin API stays internal-only per `data-model.md`)

**Checkpoint**: A user can self-register and log in via Kratos alone, independent of Hydra/the bridge.

---

## Phase 5: User Story 3 - Hydra's login and consent challenges are resolved automatically against the current Kratos session (Priority: P1)

**Goal**: `gitstore-oidc-bridge` correctly resolves both challenge types, with no user-facing consent screen and correct redirect-to-Kratos-login behavior when no session exists.

**Independent Test**: Drive an Authorization Code request against Hydra twice — with and without a prior Kratos session — and confirm the two divergent outcomes described in `spec.md` User Story 3's acceptance scenarios.

### Tests for User Story 3

- [ ] T013 [P] [US3] Add failing tests in `gitstore-oidc-bridge/internal/bridge/login_test.go`: valid session → accept with `subject`=identity id; no session → redirect to Kratos login with `return_to` preserved; upstream API failure → reject, no raw 500
- [ ] T014 [P] [US3] Add failing tests in `gitstore-oidc-bridge/internal/bridge/consent_test.go`: full scope grant when requested ⊆ permitted; partial grant when requested ⊃ permitted; claims populated from looked-up identity traits; upstream API failure → reject

### Implementation for User Story 3

- [ ] T015 [US3] Implement `GET /login` in `gitstore-oidc-bridge/internal/bridge/login.go` per `contracts/oidc-bridge-routes.md` until T013 is green
- [ ] T016 [US3] Implement `GET /consent` in `gitstore-oidc-bridge/internal/bridge/consent.go` per `contracts/oidc-bridge-routes.md` until T014 is green
- [ ] T017 [US3] Add `docker/oidc-bridge.Dockerfile` and the `oidc-bridge` service to `compose.oidc.yml`, wiring `GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL`/`KRATOS__PUBLIC_URL`/`KRATOS__ADMIN_URL` to the internal Compose service names
- [ ] T018 [US3] Add the idempotent `hydra-client-setup` one-shot service to `compose.oidc.yml`, registering the OAuth2 client per `data-model.md`'s registered-client table, configured with `HYDRA_LOGIN_CONSENT_URL` pointed at `oidc-bridge`'s `/login`/`/consent`

**Checkpoint**: A full Authorization Code + PKCE round trip completes end-to-end (Kratos session → Hydra login/consent via the bridge → authorization code → token) per `quickstart.md`'s manual verification steps.

---

## Phase 6: User Story 4 - Kratos identity traits map cleanly onto `gitstore-api`'s `Principal` (Priority: P2)

**Goal**: The claims-mapping contract in `data-model.md`/`contracts/kratos-identity-schema.md` is verified against a real issued token, not just documented.

**Independent Test**: Inspect a token issued for a known Kratos identity and confirm `sub`/`email`/`preferred_username` match the mapping table exactly.

### Tests for User Story 4

- [ ] T019 [P] [US4] Add a manual verification checklist entry decoding a real issued ID token and diff-checking its claims against `data-model.md`'s mapping table (`sub` = identity id, not email)

### Implementation for User Story 4

- [ ] T020 [US4] Confirm (and adjust if needed) that `consent.go` (T016) populates `session.id_token.email`/`session.id_token.preferred_username` exactly as `data-model.md` specifies, with a regression test added to `consent_test.go` if any gap is found

**Checkpoint**: Claims mapping is proven against a live token, not just asserted in docs.

---

## Phase 7: User Story 5 - The reference stack is deployed/run/torn down like GitStore's other optional stacks (Priority: P2)

**Goal**: `make` targets for this stack match the naming/layering conventions of `scylla`/`admin-compose` exactly.

**Independent Test**: Run the new targets end-to-end and confirm idempotency/layering parity with `make scylla`/`make admin-compose`.

### Tests for User Story 5

- [ ] T021 [P] [US5] Add a manual verification checklist entry running `make oidc` twice in a row and confirming the second run is a no-op with respect to Hydra client registration and Postgres data

### Implementation for User Story 5

- [ ] T022 [US5] Add `oidc`, `compose-oidc`, `oidc-down`, `oidc-stop`, `oidc-logs` targets to the root `Makefile`, mirroring `scylla`/`compose-scylla`/`admin-down`/`admin-stop`/`admin-logs`'s exact body shape
- [ ] T023 [US5] Update `make help`'s target listing and this repository's `CLAUDE.md`/`AGENTS.md` Commands section with the new targets

**Checkpoint**: Full operator lifecycle (`make oidc` → inspect → `make oidc-down`) works exactly like the existing optional stacks.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Close the loop with Phase 7's documentation and confirm zero regressions elsewhere.

- [ ] T024 [P] Add the additive cross-reference addendum to `docs/implementation/020-pluggable_auth_architecture.md` §7, per `plan.md`'s Implementation sequence step 7 — no change to Phase 7's existing Relying-Party description
- [ ] T025 Run `make build`, `make test`, `make lint`, `make pr-ready` and confirm zero regressions in `gitstore-api`, `gitstore-git-service`, `gitstore-controller-manager`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies; start immediately.
- **Foundational (Phase 2)**: Must complete before route-handler work starts.
- **User Stories (Phases 3–7)**: Phase 3 (US1, Hydra alone) and Phase 4 (US2, Kratos alone) can proceed in parallel once Phase 1/2 are done. Phase 5 (US3, the bridge) depends on both, since it integrates Hydra and Kratos together. Phase 6 (US4) and Phase 7 (US5) depend on Phase 5's working end-to-end path.
- **Polish (Phase 8)**: Must wait for all desired user stories to complete.

### User Story Dependencies

- **User Story 1 (P1)**: No story dependency; can ship as a standalone "issuer exists" checkpoint.
- **User Story 2 (P1)**: No story dependency; can ship as a standalone "Kratos self-service works" checkpoint, in parallel with US1.
- **User Story 3 (P1)**: Depends on US1 and US2's services both being present in `compose.oidc.yml`; this is the story that actually connects them.
- **User Story 4 (P2)**: Depends on US3's working end-to-end login path (needs a real issued token to inspect).
- **User Story 5 (P2)**: Depends on US3's full service set existing in `compose.oidc.yml` (needs every service present before `make` targets are meaningful).

### Parallel Opportunities

- `T002`, `T003` are parallelizable (independent config files).
- `T005`, `T006` are parallelizable (independent client wrapper packages).
- `T008`, `T011` (US1/US2 manual-verification tasks) are parallelizable once Phase 2 is done.
- `T013`, `T014` (US3 test files) are parallelizable across independent test files.
- `T019`, `T021` are parallelizable once US3 is green.

---

## Implementation Strategy

### MVP First (User Stories 1–3 Only)

1. Complete Phase 1 and Phase 2.
2. Bring up Hydra alone (US1) and Kratos alone (US2) — confirm each independently.
3. Add the bridge (US3) and confirm the full Authorization Code + PKCE round trip end-to-end.
4. Stop and confirm all three P1 stories pass before moving on to claims-mapping verification (US4) and `make`-target polish (US5).

### Incremental Delivery

- US1 + US2 deliver the two foundational identity/OAuth2-provider halves independently.
- US3 is the integration point that makes them work together as a real OIDC issuer.
- US4 proves the claims contract this spec exists to guarantee for `gitstore-api`'s future Phase 7 consumption.
- US5 closes the loop on operator experience parity with the rest of GitStore's optional stacks.

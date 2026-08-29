# Implementation Plan: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

**Branch**: `059-optional-oidc-provider` | **Date**: 2026-08-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/059-optional-oidc-provider/spec.md`

## Summary

Ship an optional, separately-deployable reference OIDC provider stack — Ory Hydra (OAuth2/OIDC provider) + Ory Kratos (identity/session source of truth) — as GitStore's "bring your own, but we also ship a usable default" answer for OIDC, mirroring the pattern already established for `gitstore-admin`. A new minimal standalone service, `gitstore-oidc-bridge`, implements Hydra's `/login` and `/consent` challenge-resolution routes against the current Kratos session. The stack is wired up via a new optional `compose.oidc.yml` overlay (mirroring `compose.scylla.yml`/`compose.admin.yml`) and new `make` targets, and is documented as one possible `issuer_url` choice for `gitstore-api`'s Phase 7 `OIDCJWTProvider` — this plan makes zero changes to that provider's Relying-Party-side design, interface, or config schema. `docs/implementation/020-pluggable_auth_architecture.md` §7 gains a short, additive cross-reference addendum only.

## Technical Context

**Language/Version**: Go 1.25 (new `gitstore-oidc-bridge` service, matching `gitstore-api`'s stack); YAML (Hydra/Kratos serve config, Docker Compose overlay); no Rust or new frontend-language surface.
**Primary Dependencies**: `github.com/ory/client-go` (official Ory SDK, for both Hydra's Admin API and Kratos's public/admin APIs — see `research.md` Decision 4); `net/http`/`github.com/gin-gonic/gin` for the bridge's own two routes plus `/healthz` (matching the HTTP-framework choice already in use for `gitstore-api`'s smart-HTTP surface); `go.uber.org/zap` (structured logging, matching every other Go service); `github.com/spf13/viper` (config, matching `gitstore-api`/`gitstore-controller-manager`'s existing pattern). Docker images: `oryd/hydra`, `oryd/kratos`, `postgres` (each Hydra/Kratos instance owns its own Postgres — see `research.md` Decision 8). No new dependency is added to `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager`'s existing `go.mod`/`Cargo.toml`.
**Storage**: None in GitStore's own `datastore.Datastore` abstraction. Hydra and Kratos each persist to their own dedicated Postgres instance within `compose.oidc.yml`, entirely outside GitStore's `go-memdb`/ScyllaDB storage model.
**Testing**: Go unit tests for `gitstore-oidc-bridge`'s route handlers (`login.go`, `consent.go`) against a mocked Hydra Admin API and mocked Kratos API (no live containers required); an opt-in, Compose-backed integration test/manual verification flow (mirroring the existing `make test-scylla-integration`'s "requires an external instance" pattern) that exercises a real Authorization Code + PKCE round trip against a running `compose.oidc.yml` stack. No changes to any existing Go/Rust test suite in `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager`.
**Target Platform**: Linux server and Darwin development hosts already supported by every other GitStore service; Docker Compose for the reference stack itself.
**Project Type**: New optional service (`gitstore-oidc-bridge`) plus new optional deployment configuration (`compose.oidc.yml`, `deploy/oidc/**`, `docker/oidc-bridge.Dockerfile`, new `Makefile` targets). No changes to `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` source.
**Performance Goals**: Not on any hot path — Hydra/Kratos/the bridge are only involved during a login/consent/token-refresh round trip driven by whichever client application performs the Authorization Code flow, never during `gitstore-api`'s own per-GraphQL-request token verification (which remains local, JWKS-cache-backed verification, unchanged by this plan). No new performance target is introduced beyond "does not alter the latency of Phase 7's existing token-verification path."
**Constraints**: MUST NOT modify `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` source; the reference stack MUST remain entirely optional (no default `make`/compose path depends on it); Hydra's and Kratos's Admin APIs MUST stay off any browser/public-reachable network; secrets follow the existing `.env`-driven, never-committed convention already used by `compose.scylla.yml`/`compose.admin.yml`; OAuth2 client registration MUST be idempotent on repeated startup.
**Scale/Scope**: Single-node reference deployment; not designed for HA/multi-region Hydra/Kratos topologies (out of scope per `spec.md`'s Assumptions).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Bridge route-handler tests (mocked Hydra/Kratos) are written before `login.go`/`consent.go` implementation (Phase 2 task ordering in `tasks.md`). |
| II. API-First Design | PASS | The bridge's HTTP contract (`contracts/oidc-bridge-routes.md`) and the Kratos identity schema (`contracts/kratos-identity-schema.md`) are defined before any handler code is written. |
| III. Clear Contracts & Versioning | PASS | No existing `gitstore-api` GraphQL/gRPC contract changes at all. The one new contract (`gitstore-oidc-bridge`'s HTTP routes) is internal to the optional stack and has no external versioning obligation beyond what this spec documents. |
| IV. Observability & Debuggability | PASS | The bridge logs every login/consent accept/reject decision with structured `zap` fields (challenge id, outcome, reason on rejection), mirroring the existing admission/reconciler logging conventions elsewhere in the codebase. |
| V. User Story Driven Development | PASS | Work maps to US1 (stack stands up a working issuer), US2 (Kratos self-service registration/login), US3 (bridge resolves challenges), US4 (claims mapping), US5 (`make`-target parity). |
| VI. Incremental Delivery | PASS | US1–US3 are independently testable per spec once the stack is up; US4 (claims-mapping documentation/verification) and US5 (`make`-target polish) can land after the core login path works without blocking it. |
| VII. Simplicity & YAGNI | PASS | Reuses an already-executed, real side-by-side comparison to make the architecture decision (no speculative federation support, no HA topology, no bespoke session layer — Kratos's own self-service UI is used as-is). |

**Gate result**: PASS. No complexity exceptions required — this is new, additive, entirely optional surface area with no changes to any existing core-service contract.

### Post-design gate

Phase 1 design preserves the pre-design result:

- the bridge is the smallest integration Hydra's own documented `/login`+`/consent` model requires — no additional abstraction layer beyond what `contracts/oidc-bridge-routes.md` specifies;
- the Kratos identity schema (`contracts/kratos-identity-schema.md`) defines exactly the two traits (`email`, `username`) this spec's claims-mapping contract needs, with explicit non-goals recorded rather than speculative extra fields;
- `compose.oidc.yml` and its `make` targets copy the existing `compose.scylla.yml`/`compose.admin.yml` overlay-and-target pattern verbatim rather than inventing a new deployment convention.

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/059-optional-oidc-provider/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── oidc-bridge-routes.md        # /login, /consent, /healthz contract against Hydra + Kratos Admin APIs
│   └── kratos-identity-schema.md    # traits schema + claims mapping onto gitstore-api's Principal
├── checklists/
│   └── requirements.md
└── tasks.md                          # created by /speckit-tasks
```

### Source Code (repository root)

```text
gitstore-oidc-bridge/                       # NEW standalone Go service
├── cmd/bridge/main.go                       # HTTP server wiring, Viper config load, zap logger init
├── internal/
│   ├── config/config.go                     # GITSTORE_OIDC_BRIDGE__* schema (contracts/oidc-bridge-routes.md)
│   ├── hydraclient/client.go                 # thin wrapper over github.com/ory/client-go's Hydra Admin API surface
│   ├── kratosclient/client.go                 # thin wrapper over github.com/ory/client-go's Kratos public+admin API surface
│   └── bridge/
│       ├── login.go                          # GET /login handler
│       ├── login_test.go
│       ├── consent.go                        # GET /consent handler
│       ├── consent_test.go
│       └── health.go                         # GET /healthz handler
└── go.mod                                    # new module; own dependency set, no coupling to gitstore-api's go.mod

deploy/oidc/
├── hydra/
│   └── config.yaml                           # Hydra serve config: urls.self.issuer, login/consent bridge URLs, secrets via env
└── kratos/
    ├── kratos.yml                             # Kratos serve config: identity.schemas, selfservice flows, courier (dev mailslurper)
    └── identity.schema.json                   # contracts/kratos-identity-schema.md, verbatim

docker/
└── oidc-bridge.Dockerfile                     # NEW, mirrors docker/admin.Dockerfile's per-service Dockerfile convention

compose.oidc.yml                              # NEW optional overlay (mirrors compose.scylla.yml/compose.admin.yml):
                                                #   hydra-postgres, hydra-migrate, hydra, hydra-client-setup (one-shot,
                                                #   idempotent client registration), kratos-postgres, kratos-migrate,
                                                #   kratos, mailslurper (dev-only mail catcher for Kratos self-service
                                                #   flows), oidc-bridge — all on the existing gitstore-network, with
                                                #   Hydra/Kratos Admin APIs published to no host port (internal-network
                                                #   only, per data-model.md's network topology table)

Makefile                                       # NEW targets, mirroring the scylla/admin naming convention:
                                                #   oidc            — run only Hydra/Kratos/bridge services
                                                #   compose-oidc    — run the core stack + oidc stack together
                                                #   oidc-down/-stop/-logs — lifecycle helpers, mirroring admin-down/-stop/-logs

docs/implementation/
└── 020-pluggable_auth_architecture.md         # §7 gains a short, additive cross-reference addendum only —
                                                #   no change to Phase 7's existing Relying-Party description
```

**Structure Decision**: A new, independent optional service (`gitstore-oidc-bridge`) plus new optional deployment configuration, following the exact precedent `gitstore-admin`/`compose.admin.yml` already set for "an optional reference component gets its own top-level directory, its own Dockerfile, its own compose overlay, its own `make` targets." No existing core-service source is touched.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Architecture is Ory Hydra + Ory Kratos ("Approach B"), not Dex + Oathkeeper + Kratos ("Approach A") — confirmed via a real side-by-side experiment; the deciding factor is Hydra's real refresh-token support against GitStore's own `ErrNotSupported`-for-OIDC-refresh design (Phase 3d). Multi-directory federation (Dex's strength) is a considered-and-deferred alternative, not an oversight.
2. The login/consent bridge is a new standalone service, `gitstore-oidc-bridge` — not folded into `gitstore-admin`, whose own paused/uncertain-rewrite status makes it unsuitable as a home for infrastructure the OIDC stack cannot function without.
3. The bridge is written in Go, matching the codebase's existing control-plane language, rather than reusing the reference experiment's Next.js implementation (which existed to demo a browser session UX GitStore's bridge does not need).
4. The bridge uses the official `github.com/ory/client-go` SDK for both Hydra's and Kratos's Admin/public APIs, mirroring spec 039's precedent of adopting a maintained client library for a well-defined external API surface.
5. The Kratos identity schema carries `email` + `username` traits; the bridge sets Hydra's login-challenge `subject` to the Kratos identity's own stable `id`, never the mutable `email` trait, so `Principal.Subject` stays stable.
6. `offline_access`/refresh-token handling is entirely between the client application and Hydra's public token endpoint; neither `gitstore-api` nor `gitstore-oidc-bridge` intercepts it, consistent with Phase 3d's existing scope boundary.
7. Hydra's and Kratos's Admin APIs are internal-network-only within `gitstore-network`, reachable by `gitstore-oidc-bridge` and nothing browser-facing.
8. Hydra and Kratos each get their own dedicated Postgres in `compose.oidc.yml`; neither touches GitStore's own `datastore.Datastore` abstraction.
9. Federation beyond Kratos is explicitly out of scope for this spec.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines:

- the Kratos identity schema (`traits.email`, `traits.username`);
- the full claims-mapping table from Kratos identity → OIDC ID token claim → `gitstore-api` `Principal` field;
- the login-challenge and consent-challenge state machines the bridge implements;
- the registered OAuth2 client's shape (grant types, scopes, redirect URIs);
- the Compose network topology (public vs. internal reachability for every component).

### Interface contracts

- [contracts/oidc-bridge-routes.md](contracts/oidc-bridge-routes.md): the `GET /login`, `GET /consent`, and `GET /healthz` contract, including error handling and the bridge's own config schema (`GITSTORE_OIDC_BRIDGE__*`).
- [contracts/kratos-identity-schema.md](contracts/kratos-identity-schema.md): the identity JSON Schema itself, plus the stability contract for `Principal.Subject` vs. mutable trait-derived claims, plus this schema's explicit non-goals.
- [quickstart.md](quickstart.md): test-first implementation order, plus manual end-to-end verification steps (bring up the stack, register via Kratos, complete an Authorization Code + PKCE round trip, inspect the resulting token's claims).

### Implementation sequence

1. Add the Kratos identity schema and Hydra/Kratos serve config files under `deploy/oidc/`; verify both images boot against them via a manual `docker compose -f compose.oidc.yml up` before writing any Go code.
2. Scaffold the `gitstore-oidc-bridge` Go module (`go.mod`, `cmd/bridge/main.go`, `internal/config`), wired to load `GITSTORE_OIDC_BRIDGE__*` config and start an HTTP server with a `/healthz` route only.
3. Add failing unit tests for `GET /login` against a mocked Hydra Admin API + mocked Kratos `/sessions/whoami` (valid session → accept; no session → redirect to Kratos login); implement `login.go` until green.
4. Add failing unit tests for `GET /consent` against a mocked Hydra Admin API + mocked Kratos Admin API identity lookup (scope intersection, claims population, no-consent-screen accept path); implement `consent.go` until green.
5. Add `docker/oidc-bridge.Dockerfile`, the `hydra-client-setup` one-shot idempotent registration service, and assemble `compose.oidc.yml` end-to-end; verify a full Authorization Code + PKCE round trip manually per `quickstart.md`.
6. Add the new `Makefile` targets (`oidc`, `compose-oidc`, `oidc-down`, `oidc-stop`, `oidc-logs`), mirroring the existing `scylla`/`admin` target bodies exactly.
7. Add the Phase 7 addendum to `docs/implementation/020-pluggable_auth_architecture.md` §7, cross-referencing this spec; update `docs/` per the repository's standing "after implementing a feature, update `docs/`" guideline.
8. Run `make build`, `make test`, `make lint`, `make pr-ready` to confirm zero regressions in any existing service.

## Complexity Tracking

*No entries — the Constitution Check gate passed with no exceptions. This plan adds a new, entirely optional, independently-deployable service and configuration surface; it does not modify any existing core-service contract, so no complexity trade-off needed to be justified.*

# Implementation Plan: Pluggable AuthN/AuthZ — Phase 4 gRPC HMAC Inter-Service Auth

**Branch**: `033-auth-phase-4` | **Date**: 2026-06-26 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/033-auth-phase-4/spec.md`
**GH Issue**: #126
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`

## Summary

Secure the gRPC channel between `gitstore-api` and `gitstore-git-service` with a shared
HMAC bearer token. Add a Tonic interceptor on the Rust side that validates
`Authorization: Bearer <secret>` on every inbound call; add a `PerRPCCredentials`
implementation on the Go side that injects the same secret on every outbound call.
Rename `cmd/hashpw` → `cmd/gitctl` with three subcommands (`hash-password`,
`gen-jwt-secret`, `gen-hmac-secret`) and add corresponding Makefile targets. All
existing CI workflows continue to pass unchanged.

## Technical Context

**Language/Version**: Go 1.25 (gitstore-api) + Rust 1.x (gitstore-git-service)
**Primary Dependencies**:
- Go: `google.golang.org/grpc v1.81.1` (already in go.mod), `golang.org/x/crypto` (already in go.mod for bcrypt in gitctl), no new deps
- Rust: `tonic 0.14.6` (already in Cargo.toml), no new deps (string equality comparison; no new HMAC crate required)
**New Dependencies**: None
**Storage**: No datastore changes — in-process config values only
**Testing**: `go test ./...` (unit tests in `gitstore-api`), `cargo test --verbose` (unit tests in `gitstore-git-service`)
**Target Platform**: Linux server (CI: ubuntu-latest)
**Project Type**: Web service (GraphQL API) + gRPC backend (git service)
**Performance Goals**: Interceptor adds < 1µs per call (string equality on a short token); no measurable overhead
**Constraints**: Zero breaking changes to existing env vars or gRPC contract; `go build ./...` and `cargo build` must pass in CI; all CI workflows that start the Docker Compose stack or use testcontainers must supply `GITSTORE_AUTH__GRPC__HMAC_SECRET`; license headers required on all new files
**Scale/Scope**: Single-instance deployment; no distributed coordination needed

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | Unit tests for `HmacInterceptor` (accept/reject/rotation-window) written before implementation; unit tests for `hmacCreds`/`gitctl` written before implementation |
| II. API-First | PASS | `contracts/grpc-hmac-contract.md` defines the bearer token protocol, error codes, and startup invariants before any code is written |
| III. Clear Contracts | PASS | `HmacInterceptor` is the stable server-side contract; `hmacCreds` is the stable client-side contract; both expose no public mutable state |
| IV. Observability | PASS | Git service emits `info!` on startup confirming HMAC active + rotation-window state; `UNAUTHENTICATED` errors are logged at the gRPC transport level |
| V. User Story Driven | PASS | All tasks map to US1 (rejection), US2 (transparent injection), or US3 (rotation) from spec.md |
| VI. Incremental Delivery | PASS | Phase 4 is independently deployable; does not require Phase 5; CI stack changes land in the same PR so the branch is always green |
| VII. Simplicity/YAGNI | PASS | String equality (not per-request HMAC-SHA256 digest); no new crates; `gitctl` only adds what the spec asks for |

**Complexity Justification**: None — no violations.

## Project Structure

### Documentation (this feature)

```text
specs/033-auth-phase-4/
├── plan.md                      # This file
├── spec.md                      # Feature specification
├── research.md                  # Phase 0 — all decisions resolved
├── data-model.md                # Phase 1 — config keys, structs, binary schema
├── quickstart.md                # Phase 1 — local dev setup
├── contracts/
│   └── grpc-hmac-contract.md   # Phase 1 — bearer token protocol contract
└── tasks.md                     # Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-api/
├── cmd/
│   ├── hashpw/                  # DELETED — replaced by gitctl
│   └── gitctl/
│       └── main.go              # NEW — hash-password / gen-jwt-secret / gen-hmac-secret
├── internal/
│   ├── config/
│   │   └── config.go            # MODIFIED — add HmacSecret to GitEndpointConfig; add to requiredKeys validation
│   └── gitclient/
│       ├── auth.go              # NEW — hmacCreds (PerRPCCredentials)
│       └── grpc_client.go       # MODIFIED — NewClientWithAddr accepts hmacCreds via option or new constructor

gitstore-git-service/
├── src/
│   ├── auth/
│   │   ├── mod.rs               # NEW — pub mod interceptor
│   │   └── interceptor.rs       # NEW — HmacInterceptor (tonic::service::Interceptor)
│   ├── config.rs                # MODIFIED — add AuthConfig + GrpcAuthConfig structs; add auth field to AppConfig; add validation
│   └── main.rs                  # MODIFIED — construct HmacInterceptor from cfg.auth.grpc; use GitServiceServer::with_interceptor

Makefile (root)
├── gen-admin-password           # MODIFIED — call ./cmd/gitctl hash-password instead of ./cmd/hashpw
├── gen-jwt-secret               # NEW — generate JWT secret, append to gitstore-api/.env
└── gen-hmac-secret              # NEW — generate HMAC secret, append to gitstore-api/.env

compose.yml                      # MODIFIED — add GITSTORE_AUTH__GRPC__HMAC_SECRET to git-service and api env blocks

.github/workflows/
├── ci-integration.yml           # MODIFIED — add GITSTORE_AUTH__GRPC__HMAC_SECRET to docker compose up env on integration-test, integration-test-scylla, and grpc-contract-test jobs
└── ci-admin.yml                 # NOT MODIFIED — currently failing due to incomplete admin; excluded from scope

docs/
└── implementation/
    └── pluggable_auth_architecture.md  # MODIFIED — Phase 4 milestone marker (already documented, mark as complete)
```

**Structure Decision**: Two-service change (Go API + Rust git service) matching the existing polyglot structure. No new top-level directories.

## Phase 0 Research Summary

Research is complete. See [research.md](research.md) for all 8 decisions.

Key resolved decisions:
1. Raw bearer string equality — no per-request HMAC digest; mTLS deferred
2. `GitServiceServer::with_interceptor` — Tonic 0.14 synchronous interceptor
3. No new Rust crates — `tonic` already provides everything needed
4. `grpc.WithPerRPCCredentials(hmacCreds{...})` — idiomatic Go pattern
5. Startup-fail on missing secret (both sides) — no silent fallback
6. `cmd/hashpw` → `cmd/gitctl` with three subcommands
7. `hmac_secret_previous` as `Option<String>` for rotation window
8. `compose.yml` + `ci-integration.yml` must supply `GITSTORE_AUTH__GRPC__HMAC_SECRET`; `ci-admin.yml` excluded (currently broken, not a required check); `ci.yml`, `ci-proto.yml`, `cd.yml`, and license-header workflows are unaffected; license headers required on all new Go and Rust files

## Phase 1 Design Summary

### New Go types

| File | Type | Responsibility |
|------|------|---------------|
| `gitclient/auth.go` | `hmacCreds` | `PerRPCCredentials` — injects Bearer token into every outbound gRPC call's metadata |
| `cmd/gitctl/main.go` | `main` | CLI with `hash-password`, `gen-jwt-secret`, `gen-hmac-secret` subcommands |

### Modified Go files

| File | Change |
|------|--------|
| `config/config.go` | Add `HmacSecret string` to `GitEndpointConfig`; add `"auth.grpc.hmac_secret"` to `requiredKeys`; add Viper default/env |
| `gitclient/grpc_client.go` | Pass `grpc.WithPerRPCCredentials(hmacCreds{token: cfg.Git.Grpc.HmacSecret})` in `NewClientWithAddr` (or new constructor) |

### New Rust types

| File | Type | Responsibility |
|------|------|---------------|
| `src/auth/interceptor.rs` | `HmacInterceptor` | Validates `Authorization: Bearer` on every inbound gRPC call |
| `src/auth/mod.rs` | module | Exposes `pub mod interceptor` |

### Modified Rust files

| File | Change |
|------|--------|
| `src/config.rs` | Add `AuthConfig`, `GrpcAuthConfig` structs; add `pub auth: AuthConfig` to `AppConfig`; add validation (`hmac_secret` non-empty) |
| `src/main.rs` | Construct `HmacInterceptor::new(&cfg.auth.grpc)`; replace `GitServiceServer::new(svc)` with `GitServiceServer::with_interceptor(svc, interceptor)` |

### Makefile changes

| Target | Change |
|--------|--------|
| `gen-admin-password` | Replace `./cmd/hashpw` with `./cmd/gitctl hash-password` |
| `gen-jwt-secret` | **NEW** — `gitctl gen-jwt-secret >> gitstore-api/.env` |
| `gen-hmac-secret` | **NEW** — `gitctl gen-hmac-secret >> gitstore-api/.env` |

### No GraphQL schema changes

The public GraphQL schema is untouched. This is a pure infrastructure change.

## Complexity Tracking

No constitution violations.

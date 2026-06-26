# Research: Pluggable AuthN/AuthZ — Phase 4 gRPC HMAC

**Branch**: `033-auth-phase-4` | **Phase**: 0 | **Date**: 2026-06-26

All decisions required for Phase 4 are resolved below.

---

## Decision 1: HMAC token format — raw shared secret vs. per-request HMAC-SHA256 digest

**Decision:** Raw shared-secret bearer token (`Authorization: Bearer <secret>`).

**Rationale:** The design doc §4a explicitly specifies "shared HMAC bearer token passed as gRPC metadata." The simplicity is intentional: the secret itself is the token. There is no per-request signing, nonce, or timestamp because the channel is intended to become mTLS in a future phase. Adding per-message signing now would be premature — the threat model is insider-network adversaries who reach the gRPC port, not replay attacks on an already-authenticated channel.

**Alternatives considered:**
- HMAC-SHA256 per-request digest (token = HMAC(secret, body)) — provides replay protection but requires syncing message bodies across Rust/Go and adds latency to every RPC; rejected as premature per design doc Decision HMAC/mTLS.
- mTLS — requires CA + cert provisioning infrastructure that does not exist; explicitly deferred to Phase 2 per design doc §4a.

---

## Decision 2: Where to place the Rust interceptor in the server startup path

**Decision:** Wire `HmacInterceptor` as a Tonic server interceptor using `GitServiceServer::with_interceptor` at the call site in `main.rs` (replacing the current `GitServiceServer::new`).

**Rationale:** Tonic 0.14 interceptors are synchronous functions (`fn call`), not async. Attaching the interceptor at service registration time (`.with_interceptor`) is the idiomatic pattern — it runs before any handler and requires zero changes to `GitServiceImpl`. The `HmacInterceptor` needs only the configured secret; it is constructed from `cfg.auth.grpc.hmac_secret` in `main.rs`.

**Alternatives considered:**
- Tower middleware layer — more flexible but requires `ServiceBuilder`/`Layer` boilerplate; overkill for a single secret check.
- Per-handler manual check — violates DRY; interceptor approach is already demonstrated in the design doc.

---

## Decision 3: Rust HMAC dependency — new crate vs. stdlib

**Decision:** No new Rust crate needed. The interceptor validates by string equality (`token == secret`), not by computing a keyed hash. The `tonic` crate (already in `Cargo.toml`) provides the `Interceptor` trait and `metadata()` accessor; no additional crypto dep is required for Phase 4.

**Rationale:** Phase 4's threat model is network-perimeter isolation, not cryptographic authentication of individual messages. String equality on a randomly-generated secret is equivalent to a pre-shared key in this context. A future mTLS upgrade (Phase 2 of security hardening) would replace this entirely.

**Alternatives considered:**
- `hmac` + `sha2` crates for HMAC-SHA256 — overkill, adds compile-time dependencies, and the added security is marginal when the channel will eventually become mTLS.

---

## Decision 4: Go gRPC credentials implementation pattern

**Decision:** Implement `google.golang.org/grpc/credentials.PerRPCCredentials` interface (`hmacCreds` struct with `GetRequestMetadata` and `RequireTransportSecurity`). Pass it via `grpc.WithPerRPCCredentials(hmacCreds{...})` as a dial option in `NewClientWithAddr` (or a new `NewClientWithAddrAndSecret` constructor).

**Rationale:** `PerRPCCredentials` is the idiomatic gRPC Go interface for per-call metadata injection. It is already the pattern shown in the design doc §4a, and it keeps `hmacCreds` as a small, self-contained struct with no external deps. `RequireTransportSecurity` returns `false` because we are not adding TLS in this phase.

**Alternatives considered:**
- `grpc.WithUnaryInterceptor` on the client side — works but more verbose and requires a separate interceptor for streaming calls.
- Hardcode in `NewClientWithAddr` — would break existing callers that do not yet have the secret configured; a new constructor or an option argument is safer.

---

## Decision 5: API startup failure mode — missing HMAC secret

**Decision:** The API reads `GITSTORE_AUTH__GRPC__HMAC_SECRET` via Viper at startup (alongside the existing `git.grpc.uri`). If the key is empty, startup fails with a clear zap fatal log and non-zero exit code. This mirrors how `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` is validated — no silent fallback.

**Rationale:** A missing secret would cause every gRPC call to fail at runtime with an `unauthenticated` error from the git service — a confusing error for operators. Failing fast at startup with a named config key is strictly better. The existing `config.go` validation pattern (`requiredKeys` map) is already present and can be extended.

**Alternatives considered:**
- Warn on missing secret and fall back to unauthenticated calls — violates FR-005 and would silently disable security.
- Accept empty string as "auth disabled" mode — rejected; the spec has no such mode; use a dedicated `GITSTORE_AUTH__GRPC__DISABLED=true` flag if ever needed.

---

## Decision 6: gittools binary rename — new name and subcommand structure

**Decision:** Rename `gitstore-api/cmd/hashpw` → `gitstore-api/cmd/gitctl`. The binary supports three subcommands:
- `gitctl hash-password <password>` — bcrypt hash (replaces current `hashpw` behaviour)
- `gitctl gen-jwt-secret` — outputs a 256-bit (32-byte) cryptographically random base64url secret for `GITSTORE_AUTH__JWT__SECRET`
- `gitctl gen-hmac-secret` — outputs a 256-bit (32-byte) cryptographically random base64url secret for `GITSTORE_AUTH__GRPC__HMAC_SECRET`

**Rationale:** All three operations share the same bootstrap concern: generating secrets that go into `.env`. A single binary with subcommands is simpler than three separate binaries and makes the `Makefile` targets discoverable (`gitctl --help`). The subcommand names are self-documenting. `crypto/rand` is already available in the Go stdlib; no new dependencies required.

**Alternatives considered:**
- Keep `hashpw` as-is and add new binaries `gen-jwt` and `gen-hmac` — creates three entry-points; harder to discover.
- Use a flag (`hashpw --mode=jwt`) — less ergonomic than subcommands; harder to add future operations.
- Use `os.Args[0]` multi-call binary — overcomplicated for three subcommands.

**New Makefile targets:**

| Target | Command | Description |
|--------|---------|-------------|
| `gen-jwt-secret` | `gitctl gen-jwt-secret >> gitstore-api/.env` | Generate and append `GITSTORE_AUTH__JWT__SECRET=<val>` |
| `gen-hmac-secret` | `gitctl gen-hmac-secret >> gitstore-api/.env` | Generate and append `GITSTORE_AUTH__GRPC__HMAC_SECRET=<val>` on both sides |

The existing `gen-admin-password` target is updated to call `./cmd/gitctl hash-password` instead of `./cmd/hashpw`. **CI passes because `go build ./...` still succeeds** — `cmd/hashpw` is removed and `cmd/gitctl` is added; the Makefile target is the only caller of this binary and is updated in the same PR.

---

## Decision 7: rotation window (`hmac_secret_previous`) in Rust config

**Decision:** Add `hmac_secret_previous` as an optional field (Rust: `Option<String>`, default `None`) to `GrpcAuthConfig`. The `HmacInterceptor` accepts a call if the token matches either `hmac_secret` or, when `Some`, `hmac_secret_previous`. The TOML default and env var `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS` are both optional.

**Rationale:** This matches FR-003 directly. Making it `Option<String>` avoids any breaking config change — existing deployments without the field set continue to work with the default (rotation window closed).

---

## Decision 8: CI compatibility — full impact across all workflow files

**Decision:** Changes are required to `compose.yml` and three workflow files. `ci.yml` itself requires no changes — only the workflows that start the Docker Compose stack or use testcontainers to run the git service are affected.

**Root cause:** Once the git-service interceptor is live, the service will fail startup if `GITSTORE_AUTH__GRPC__HMAC_SECRET` is absent, and it will reject every inbound gRPC call without the correct token. Any CI job that starts the git service must supply the secret, and any test that calls into the git service via gRPC must send it.

### Files requiring changes

| File | Change |
|------|--------|
| `compose.yml` | Add `GITSTORE_AUTH__GRPC__HMAC_SECRET=${GITSTORE_AUTH__GRPC__HMAC_SECRET}` to both `git-service` and `api` service `environment:` blocks |
| `.github/workflows/ci-integration.yml` | Add `GITSTORE_AUTH__GRPC__HMAC_SECRET: ci-test-grpc-hmac-secret` to `env:` on the `integration-test` (memdb) and `integration-test-scylla` jobs' `docker compose up` steps |
| `.github/workflows/ci-admin.yml` | **No change** — workflow currently fails due to incomplete implementation; excluded from Phase 4 scope |
| `.github/workflows/ci-integration.yml` | `grpc-contract-test` job: pass `GITSTORE_AUTH__GRPC__HMAC_SECRET` as an environment variable when the testcontainers-based gRPC test starts the git service container; update the Go test client setup to inject `hmacCreds` |

**`ci.yml` is unaffected** — its `rust-test` job runs `cargo build` + `cargo test` without starting the git service server, and its `go-test` job runs `go test ./...` over the API module without a running git service.

**`ci-admin.yml`** is excluded from Phase 4 scope — the workflow is currently failing due to an incomplete admin implementation and is not a required branch-protection check. Do not modify it.

**`ci-proto.yml`** is unaffected — only lints proto files.

**`cd.yml`** is unaffected — Docker image builds are pure compile-time; the secret is runtime config injected by the deployment target, not baked into images.

**License header workflows** (`go-license-headers.yml`, `rust-license-headers.yml`) require no changes to the workflow YAML, but the new files added in this PR must carry the standard AGPL header:
- `gitstore-api/cmd/gitctl/main.go` (Go)
- `gitstore-api/internal/gitclient/auth.go` (Go)
- `gitstore-git-service/src/auth/interceptor.rs` (Rust)
- `gitstore-git-service/src/auth/mod.rs` (Rust)

### CI secret value strategy

All CI jobs use a hardcoded placeholder value (`ci-test-grpc-hmac-secret`) — the same pattern already used for `GITSTORE_AUTH__JWT__SECRET` (`ci-test-jwt-secret-minimum-32-characters-long`). This value is not a real secret; it only needs to match between the API client and the git service within the test environment.

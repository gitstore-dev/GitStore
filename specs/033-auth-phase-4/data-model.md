# Data Model: Pluggable AuthN/AuthZ — Phase 4 gRPC HMAC

**Branch**: `033-auth-phase-4` | **Phase**: 1 | **Date**: 2026-06-26

Phase 4 introduces no new datastore tables or GraphQL schema changes. The "entities" are
configuration values and in-process structs.

---

## 1. Configuration Keys (new)

| Config key (Viper) | Env var | Type | Default | Required |
|--------------------|---------|------|---------|----------|
| `auth.grpc.hmac_secret` | `GITSTORE_AUTH__GRPC__HMAC_SECRET` | string | (none) | **Yes** — API fails to start if absent |

*This key is added to `gitstore-api/internal/config/config.go` inside the existing `AuthConfig` struct.*

### Rust (gitstore-git-service)

| Config key (TOML / env) | Env var | Type | Default | Required |
|-------------------------|---------|------|---------|----------|
| `auth.grpc.hmac_secret` | `GITSTORE_AUTH__GRPC__HMAC_SECRET` | string | (none) | **Yes** — service fails to start if absent |
| `auth.grpc.hmac_secret_previous` | `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS` | string (optional) | `""` (disabled) | No |

*These keys are added to `AppConfig.auth.grpc` in `gitstore-git-service/src/config.rs`.*

---

## 2. Go Structs (gitstore-api)

### `hmacCreds` (new — `gitstore-api/internal/gitclient/auth.go`)

```
hmacCreds
  token   string   // value of auth.grpc.hmac_secret from config

Implements: google.golang.org/grpc/credentials.PerRPCCredentials
  GetRequestMetadata() → map["authorization"] = "Bearer <token>"
  RequireTransportSecurity() → false
```

**Invariants:**
- `token` MUST NOT be empty at construction; callers validate before passing.
- `GetRequestMetadata` is called per-RPC by the gRPC runtime; it must be thread-safe (it is, because `token` is read-only after construction).

### `GitEndpointConfig` extension (existing — `gitstore-api/internal/config/config.go`)

The existing `GitEndpointConfig` struct gains one field:

```
GitEndpointConfig (existing)
  Uri          string   // existing
  HmacSecret   string   // NEW — mapstructure:"hmac_secret"
```

---

## 3. Rust Structs (gitstore-git-service)

### `GrpcAuthConfig` (new — `gitstore-git-service/src/config.rs`)

```
GrpcAuthConfig
  hmac_secret           String         // required; env: GITSTORE_AUTH__GRPC__HMAC_SECRET
  hmac_secret_previous  Option<String> // optional rotation key; env: GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS
```

### `AuthConfig` (new — `gitstore-git-service/src/config.rs`)

```
AuthConfig
  grpc  GrpcAuthConfig
```

Added as `pub auth: AuthConfig` to `AppConfig`.

### `HmacInterceptor` (new — `gitstore-git-service/src/auth/interceptor.rs`)

```
HmacInterceptor
  secret           Arc<str>          // primary secret
  secret_previous  Option<Arc<str>>  // rotation window secret (None = closed)

Implements: tonic::service::Interceptor (synchronous fn call)
  call(Request<()>) → Result<Request<()>, Status>
    - extracts Authorization: Bearer <token>
    - if token == secret → Ok(req)
    - elif token == secret_previous → Ok(req)  (only when Some)
    - else → Err(Status::unauthenticated("invalid inter-service token"))
```

**Invariants:**
- `secret` MUST NOT be empty at construction; `main.rs` validates config before calling `HmacInterceptor::new`.
- Comparison MUST use constant-time equality to avoid timing attacks (`subtle::ConstantTimeEq` or equivalent).
- A startup `info!` log line MUST be emitted: `"gRPC HMAC auth active"` with fields `rotation_window_open: bool`.

---

## 4. Go Binary: `cmd/gitctl` (replaces `cmd/hashpw`)

### Subcommand: `hash-password`

```
Input:  positional arg — plaintext password string
Output: bcrypt hash (DefaultCost), printed to stdout
Exit:   0 on success, 1 on error (error message to stderr)
```

### Subcommand: `gen-jwt-secret`

```
Input:  none
Output: prints "GITSTORE_AUTH__JWT__SECRET=<base64url-32-bytes>" to stdout
Exit:   0 on success, 1 on error
Source: crypto/rand.Read(32 bytes) → base64.URLEncoding (no padding)
```

### Subcommand: `gen-hmac-secret`

```
Input:  none
Output: prints "GITSTORE_AUTH__GRPC__HMAC_SECRET=<base64url-32-bytes>" to stdout
Exit:   0 on success, 1 on error
Source: crypto/rand.Read(32 bytes) → base64.URLEncoding (no padding)
```

**Notes:**
- Output format `KEY=VALUE` is intentionally suitable for appending to `.env` with `>>`.
- Both `gen-*` commands use the same underlying `genSecret()` helper (32 random bytes, base64url-encoded).
- `hash-password` behaviour is identical to the old `hashpw` binary; only the invocation changes.

---

## 5. State Transitions

The `HmacInterceptor` is stateless between calls — there are no session or state transitions. The only lifecycle event is:

```
Startup:
  cfg.auth.grpc.hmac_secret empty? → process exits (validation error)
  cfg.auth.grpc.hmac_secret non-empty → HmacInterceptor constructed, server starts,
    info log emitted: "gRPC HMAC auth active" {rotation_window_open: bool}

Per-call:
  Authorization header present and token matches? → call proceeds
  Authorization header absent or token wrong? → Status::unauthenticated returned
    (no state change, call rejected before handler runs)
```

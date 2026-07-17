# Data Model: Git Smart-HTTP Authentication (035)

**Date**: 2026-07-12

## Modified Entities

### `datastore.Repository` (extend existing)

Add push policy fields. Zero value means no limit enforced (FR-015).

```go
type Repository struct {
    // existing fields unchanged
    ID            string
    NamespaceID   string
    Name          string
    DefaultBranch string
    StorageClass  string
    CreatedAt     time.Time
    CreatedBy     string
    UpdatedAt     time.Time
    UpdatedBy     string

    // NEW: push policy limits (zero = unlimited)
    MaxPackSizeBytes int64 // max total pack size per push; 0 = unlimited
    MaxFileSizeBytes int64 // max single blob size per push; 0 = unlimited
}
```

**Validation rules**:
- Both fields must be `>= 0`; negative values are invalid.
- Zero is the sentinel for "no limit" — not a default that falls back to a global config.

**State transitions**: Policy fields are mutable (operator updates repository config). Changes take effect on the next push; in-progress push streams use a snapshot captured at stream-open.

---

## New Proto Messages (`gitstore/git/v1`)

### `PushContext`

Attached to the first chunk of every `ReceivePackRequest`. Immutable for the stream lifetime.

```proto
message PushContext {
  string namespace             = 1;  // namespace identifier (human-readable)
  string repository_name       = 2;  // repository name within namespace
  string repository_id         = 3;  // UUIDv7 stable repo ID (must match stream repository_id)
  string config_resource_version = 4; // opaque version tag of the policy snapshot (audit)
  AuthContext actor            = 5;  // sanitised principal identity
  PushPolicy policy            = 6;  // resolved limits and hook config
}
```

### `AuthContext`

Sanitised snapshot of the authenticated principal. Never carries raw credentials or tokens.

```proto
message AuthContext {
  string subject     = 1;  // e.g. "admin" or OIDC sub claim
  string issuer      = 2;  // e.g. "static-admin" or OIDC issuer URL
  string auth_method = 3;  // e.g. "basic", "bearer", "none"
  repeated string roles  = 4;
  repeated string groups = 5;
  repeated string scopes = 6;
}
```

### `PushPolicy`

Per-repository push limits resolved from the `Repository` datastore record.

```proto
message PushPolicy {
  int64 max_pack_size_bytes = 1;  // 0 = unlimited
  int64 max_file_size_bytes = 2;  // 0 = unlimited
  // hook enablement fields to be added in future specs
}
```

### `ReceivePackRequest` (extend existing)

Add `push_context` at field 4 (first-chunk only; proto3 field convention: "MUST be set on the first chunk only").

```proto
message ReceivePackRequest {
  string repository_id        = 15; // MUST be set on the first chunk only
  repeated RefCommand ref_commands = 1;  // MUST be set on the first chunk only
  bytes  pack_data            = 2;
  bool   is_last              = 3;
  PushContext push_context    = 4;  // MUST be set on the first chunk only
}
```

**Field number rationale**: Fields 1–15 encode in 1 proto3 wire byte. Field 4 is the next open slot after the core streaming fields (1–3) and keeps `push_context` in the 1-byte encoding range alongside the other hot fields.

---

## New Go Types

### `repoID` gin context key (githttp package)

`repoID` stays within the gin handler chain — consumed only by `GitHttpAuthorizer`, `PushContextInserter`, and route handlers, all of which have access to `*gin.Context`. Use `c.Set` / `c.Get` rather than `context.WithValue`, which is reserved for values that must escape gin into downstream gRPC or datastore calls (e.g., `Principal`).

```go
const repoIDKey = "repoID"
```

Usage:
```go
// set (RepoResolver middleware)
c.Set(repoIDKey, repoID)

// get (GitHttpAuthorizer, PushContextInserter, handlers)
repoID, exists := c.Get(repoIDKey)
if !exists {
    // RepoResolver must always run first; this is a middleware wiring bug
    c.AbortWithStatus(http.StatusInternalServerError)
    return
}
```

---

## New Rust Types

### `HookContext` (`git/hooks/mod.rs`)

Typed context passed to every hook pipeline stage. Derived from `PushContext` at stream-open.

```rust
#[derive(Clone, Debug)]
pub struct HookContext {
    pub actor_subject: String,
    pub actor_auth_method: String,
    pub max_pack_size_bytes: i64, // 0 = unlimited
    pub max_file_size_bytes: i64, // 0 = unlimited
    pub config_resource_version: String,
}
```

**Derivation**: Built from `PushContext` fields when the first chunk is validated. Passed by reference to `ValidationHandler::run` and `AdmissionHandler::run`. Hook stages MUST NOT read auth or policy state from environment variables (FR-010).

---

## Prometheus Metrics

### `gitstore_git_http_auth_requests_total`

| Attribute | Value                                                                                       |
|-----------|---------------------------------------------------------------------------------------------|
| Type      | `CounterVec`                                                                                |
| Namespace | `gitstore`                                                                                  |
| Subsystem | `git_http`                                                                                  |
| Name      | `auth_requests_total`                                                                       |
| Labels    | `outcome` (`allow`/`deny`/`error`), `service` (`upload_pack`/`receive_pack`)                |
| Registry  | injected `prometheus.Registerer` (defaults to `prometheus.DefaultRegisterer` in production) |

Incremented by `BasicAuthenticator` on every auth outcome before the request proceeds or is aborted.

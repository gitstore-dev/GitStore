# Data Model: Local Multi-User AuthN Provider (`static-users`)

No `datastore.Datastore` schema changes. Everything in this feature lives in a new config-file-loaded, in-memory structure inside `gitstore-api`, structurally parallel to `rbaclocal.Policy`.

## `users.yaml` file schema

```yaml
# users.yaml — static-users provider user list
# Version must be "v1".
version: v1

users:
  - username: alice
    password_hash: "$2a$10$..."   # bcrypt, generated via `gitctl hash-password`
  - username: bob
    password_hash: "$2a$10$..."
```

| Field | Type | Required | Validation |
|---|---|---|---|
| `version` | `string` | Yes | Must equal `"v1"` — mirrors `rbaclocal.Policy.Version`'s exact validation. |
| `users` | `[]UserEntry` | Yes | Must contain at least one entry. |
| `users[].username` | `string` | Yes | Non-empty; unique across all entries in the file (FR-008). |
| `users[].password_hash` | `string` | Yes | Non-empty; a valid bcrypt hash (verified structurally at `bcrypt.CompareHashAndPassword` call time, not re-parsed at load time — mirrors `static-admin`'s existing trust model, which also never validates its own configured hash's format at load time). |

## In-memory representation

```go
// gitstore-api/internal/auth/provider/staticusers/users.go
package staticusers

// UserList is the in-memory representation of a v1 YAML user-list file.
type UserList struct {
    Version string       `yaml:"version"`
    Users   []UserEntry  `yaml:"users"`
}

// UserEntry is a single configured local identity.
type UserEntry struct {
    Username     string `yaml:"username"`
    PasswordHash string `yaml:"password_hash"`
}
```

This mirrors `rbaclocal.Policy`/`RolePolicy`'s exact shape (a top-level versioned struct, `gopkg.in/yaml.v3` tags, a slice of per-entry structs) — no new YAML-handling dependency, no new struct-tag convention.

## `StaticUsersProvider` (in-memory, not persisted)

```go
type StaticUsersProvider struct {
    mu        sync.RWMutex
    users     map[string]string // username -> bcrypt hash, built from UserList at load/reload time
    path      string
    jwtSecret []byte
    jwtIssuer string
    jwtDuration  time.Duration
    refreshGrace time.Duration
    blacklist *sessionBlacklist   // same shape as staticadmin's sessionBlacklist, own instance
    logger    *zap.Logger
}
```

This is structurally the union of `RBACLocalProvider`'s file-load/reload fields (`mu`, `path`, `logger`, atomic `Reload()`) and `StaticAdminProvider`'s session-lifecycle fields (`jwtSecret`, `jwtIssuer`, `jwtDuration`, `refreshGrace`, `blacklist`) — no new field shape is invented; every field already has a precedent in one of the two existing providers this spec draws from.

## Config schema additions (Viper)

New `AuthConfig` sub-struct, additive only — no existing key changes:

| Key path (Viper) | Env var | Type | Default |
|---|---|---|---|
| `auth.staticusers.users_file` | `GITSTORE_AUTH__STATICUSERS__USERS_FILE` | `string` | `"users.yaml"` |
| `auth.staticusers.jwt.secret` | `GITSTORE_AUTH__STATICUSERS__JWT__SECRET` | `string` | (required if `static-users` is in `auth.authn.chain`) |
| `auth.staticusers.jwt.issuer` | `GITSTORE_AUTH__STATICUSERS__JWT__ISSUER` | `string` | `"gitstore-static-users"` |
| `auth.staticusers.jwt.duration` | `GITSTORE_AUTH__STATICUSERS__JWT__DURATION` | `duration` | `"24h"` |
| `auth.staticusers.jwt.refresh_grace` | `GITSTORE_AUTH__STATICUSERS__JWT__REFRESH_GRACE` | `duration` | `"60s"` |

`auth.staticusers.jwt.secret`/`issuer` are intentionally namespaced under `staticusers`, not `auth.jwt.*` — see `research.md` Decision 4. `auth.authn.chain` (existing key, unchanged shape) gains `"static-users"` as a new valid chain entry, validated the same way `"static-admin"`/`"anonymous"` already are in `buildProviderRegistry`'s `switch name { ... default: return ... "unknown authn provider %q" ... }`.

## Principal shape produced by `static-users`

| Field | Value |
|---|---|
| `Subject` | The authenticated `username`, verbatim. |
| `Issuer` | `auth.staticusers.jwt.issuer`'s configured value. |
| `Roles` | **Always empty** (`nil`/`[]string{}`) — see FR-005. Never `["admin"]`, never any other hardcoded value. |
| `AuthMethod` | `"static-users"` |
| `TokenID` | The bearer token's `jti` claim (Basic Auth sessions carry no `TokenID`, mirroring `static-admin`). |

## State transitions

`static-users` has the same three states per configured user that `static-admin` has for its one: **active** (listed in `users.yaml`, can authenticate and receive new tokens) → **removed from file + reload** (can no longer authenticate with Basic Auth or mint new tokens; already-issued unexpired tokens remain valid until natural expiry or explicit `logout`/revocation — see spec.md Edge Cases). There is no third state (e.g. "disabled but not removed") in this spec's scope — that would require a richer per-user status field, which is explicitly deferred (see spec.md Assumptions: no self-registration/management UI for v1).

## Relationship to `rbac-local`'s `policy.yaml` (existing, unmodified)

No new fields are added to `Policy`/`RolePolicy`. Operators wire real, multi-subject authorization purely by adding entries under the *existing* `role_bindings` key for each `static-users` username:

```yaml
# policy.yaml (existing file, operator-edited — no schema change)
role_bindings:
  "admin":
    - admin
  "alice":       # new: a static-users identity
    - namespace-owner
  "bob":         # new: a static-users identity
    - developer
```

# Data Model: Local Multi-User AuthN + UserDir Provider (`static-users`)

No `datastore.Datastore` schema changes. No GraphQL schema changes. Everything in this feature lives in a new config-file-loaded, in-memory structure inside `gitstore-api`, structurally parallel to `rbaclocal.Policy`, replacing `staticadmin`'s equivalent single-credential fields.

## `users.yaml` file schema

```yaml
# users.yaml — static-users provider user list
# Version must be "v1".
version: v1

users:
  - username: alice
    password_hash: "$2a$10$..."   # bcrypt, generated via `gitctl hash-password`
    display_name: "Alice Doe"      # optional
    email: "alice@example.com"     # optional
  - username: bob
    password_hash: "$2a$10$..."
```

| Field | Type | Required | Validation |
|---|---|---|---|
| `version` | `string` | Yes | Must equal `"v1"` — mirrors `rbaclocal.Policy.Version`'s exact validation. |
| `users` | `[]UserEntry` | Yes | Must contain at least one entry. |
| `users[].username` | `string` | Yes | Non-empty; unique across all entries in the file (FR-007). |
| `users[].password_hash` | `string` | Yes | Non-empty; a bcrypt hash, verified structurally at `bcrypt.CompareHashAndPassword` call time — mirrors `static-admin`'s prior trust model. |
| `users[].display_name` | `string` | No | Free text; used only by `UserDirProvider.GetBySubject`/`SearchUsers`. |
| `users[].email` | `string` | No | Free text (no format validation in v1 — not consumed by anything that requires a valid address today); used only by `UserDirProvider.GetBySubject`/`SearchUsers`. |

## In-memory representation

```go
// gitstore-api/internal/auth/provider/staticusers/users.go
package staticusers

// UserList is the in-memory representation of a v1 YAML user-list file.
type UserList struct {
    Version string      `yaml:"version"`
    Users   []UserEntry `yaml:"users"`
}

// UserEntry is a single configured local identity.
type UserEntry struct {
    Username     string `yaml:"username"`
    PasswordHash string `yaml:"password_hash"`
    DisplayName  string `yaml:"display_name"`
    Email        string `yaml:"email"`
}
```

Mirrors `rbaclocal.Policy`/`RolePolicy`'s exact shape (a top-level versioned struct, `gopkg.in/yaml.v3` tags, a slice of per-entry structs) — no new YAML-handling dependency.

## `StaticUsersProvider` (in-memory, not persisted)

```go
type StaticUsersProvider struct {
    mu           sync.RWMutex
    users        map[string]UserEntry // username -> entry, built from UserList at load/reload time
    path         string
    jwtSecret    []byte
    jwtIssuer    string
    jwtDuration  time.Duration
    refreshGrace time.Duration
    blacklist    *sessionBlacklist   // same shape as staticadmin's former sessionBlacklist
    logger       *zap.Logger
}
```

This is the union of `RBACLocalProvider`'s file-load/reload fields (`mu`, `path`, `logger`, atomic `Reload()`) and `StaticAdminProvider`'s former session-lifecycle fields (`jwtSecret`, `jwtIssuer`, `jwtDuration`, `refreshGrace`, `blacklist`) — every field already has a precedent in one of the two providers this replaces/draws from. `users` is keyed by username and stores the full `UserEntry` (not just the hash) so `UserDirProvider` methods can read `DisplayName`/`Email` without a second load path.

`StaticUsersProvider` implements both `auth.AuthNProvider` and `auth.UserDirProvider` — the existing interfaces, unmodified.

## Config schema changes (Viper)

`static-admin`'s config surface is removed, not left in place:

| Removed key | Removed struct field |
|---|---|
| `auth.admin.username` | `AuthConfig.Admin.Username` |
| `auth.admin.password_hash` | `AuthConfig.Admin.Password` |

`AuthConfig.Admin UserConfig` and the `UserConfig` type itself are deleted (confirmed sole use was this field).

`auth.jwt.*` (existing `JWTConfig`, unchanged shape/env-var names) is **retained and repurposed** to belong to `static-users`:

| Key path (Viper) | Env var | Type | Default | Owner after this spec |
|---|---|---|---|---|
| `auth.jwt.secret` | `GITSTORE_AUTH__JWT__SECRET` | `string` | `""` (no longer unconditionally `validate:"required"` — see below) | `static-users` |
| `auth.jwt.issuer` | `GITSTORE_AUTH__JWT__ISSUER` | `string` | `"gitstore"` | `static-users` |
| `auth.jwt.duration` | `GITSTORE_AUTH__JWT__DURATION` | `duration` | `"24h"` | `static-users` |
| `auth.jwt.refresh_grace` | `GITSTORE_AUTH__JWT__REFRESH_GRACE` | `duration` | `"60s"` | `static-users` |

One new key, additive:

| Key path (Viper) | Env var | Type | Default |
|---|---|---|---|
| `auth.staticusers.users_file` | `GITSTORE_AUTH__STATICUSERS__USERS_FILE` | `string` | `"users.yaml"` |

`auth.staticusers.users_file` carries **no** `validate:"required"` struct tag (FR-016) — its file is loaded, and thus validated, only inside `buildProviderRegistry`'s `case "static-users":`, which itself only runs when `"static-users"` is present in `auth.authn.chain`.

`auth.authn.chain`'s default changes from `["static-admin","anonymous"]` to `["static-users","anonymous"]`.

## Config validation changes (`gitstore-api/internal/config/config.go`)

| Before | After |
|---|---|
| `JWTConfig.Secret` carries `validate:"required"` — unconditionally enforced by `validate.Struct(cfg)` in `validateConfig` | Struct tag removed. A new `validateAuthChainConfig(cfg *Config) error` function, called from `validateConfig` alongside the existing `validateDatastoreConfig`/`validateLogFormat`, requires `cfg.Auth.JWT.Secret` non-empty **only if** `"static-users"` ∈ `cfg.Auth.AuthN.Chain`. |
| `UserConfig.Username`/`UserConfig.Password` carry `validate:"required"` | Field and type deleted entirely along with `static-admin`'s removal — no replacement tag needed. |
| `GrpcAuthConfig.HmacSecret` carries `validate:"required"` | **Unchanged** — confirmed unconditionally correct (API↔git-service gRPC trust, independent of AuthN provider choice). FR-015. |

The same new function also implements the migration-safety check (FR-013): given the loaded `AuthNConfig.Chain`, `AuthZConfig.Provider`, and (once constructed) the `static-users` user list and `rbac-local` policy's `RoleBindings`, it fails with an actionable error if `AuthZConfig.Provider == "rbac-local"`, `"static-users"` ∈ `Chain`, and no configured username is a key in `RoleBindings`. Because this check needs the *loaded contents* of both `users.yaml` and `policy.yaml` (not just their configured paths), it runs after both providers' loaders have executed — inside `buildProviderRegistry` (`gitstore-api/internal/app/server.go`), immediately after both are constructed, not inside `config.validateConfig` itself (which only has the raw `*Config`, before either file is read). `plan.md`'s Project Structure section places this precisely.

## `MarshalLogObject` (zap startup logging) changes

`enc.AddString("auth.admin.username", ...)` and `enc.AddString("auth.admin.password_hash", redact(...))` lines are removed (field no longer exists). The four `auth.jwt.*` lines are unchanged in key name, now documented as `static-users`' own secret/issuer/duration/refresh-grace.

## Principal shape produced by `static-users`

| Field | Value |
|---|---|
| `Subject` | The authenticated `username`, verbatim. |
| `Issuer` | `auth.jwt.issuer`'s configured value. |
| `Roles` | **Always empty** (`nil`/`[]string{}`) — FR-004. Never populated from anywhere, including `role_bindings` (which is applied transiently inside `RBACLocalProvider.Authorize`, never written back). |
| `AuthMethod` | `"static-users"` |
| `TokenID` | The bearer token's `jti` claim (Basic Auth sessions carry no `TokenID`, mirroring `static-admin`'s prior behavior). |

## `UserProfile` shape produced by `static-users` (`UserDirProvider`)

| Field | Value |
|---|---|
| `Subject` | The `username`. |
| `DisplayName` | `users.yaml`'s `display_name`, or empty string if unset. |
| `Email` | `users.yaml`'s `email`, or empty string if unset. |
| `Groups` | Always `nil`/empty — `users.yaml` has no groups concept. |
| `Active` | Always `true` for any entry present in the loaded file (there is no "disabled but still listed" state in v1 — removing an entry is how a user is deactivated). |

`GetBySubject`/`ListGroups` return a new `staticusers.ErrUserNotFound` sentinel (distinct from `auth.ErrNotSupported`) for a username absent from the loaded list. `UpsertProfile`/`Deactivate` return `auth.ErrNotSupported` unconditionally (FR-010).

## State transitions

Per configured user: **active** (listed in `users.yaml`, can authenticate, mint new tokens, and be looked up via UserDir) → **removed from file + reload** (can no longer authenticate, mint new tokens, or be found by UserDir; already-issued unexpired tokens remain valid until natural expiry or explicit revocation). No third state (e.g., "disabled but not removed") exists in v1, matching the unchanged Assumption that user management has no mutation API.

## Relationship to `rbac-local`'s `policy.yaml` (existing, unmodified) — now load-bearing

No new fields are added to `Policy`/`RolePolicy`. "Admin" access for a migrated identity is granted purely by adding an entry under the *existing* `role_bindings` key:

```yaml
# policy.yaml (existing file, operator-edited — no schema change)
role_bindings:
  "alice":        # a static-users identity, migrated from the old single static-admin credential
    - admin
  "bob":          # a second static-users identity
    - developer
```

The startup safety check (FR-013) reads this same, unmodified `role_bindings` map to confirm at least one configured `static-users` username appears as a key.

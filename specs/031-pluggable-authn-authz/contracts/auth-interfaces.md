# Auth Interface Contracts — Phase 1

> These are the internal Go interface contracts. Phase 1 makes no changes to the
> public GraphQL schema (the existing `Login` mutation is unchanged; `Logout` and
> `RefreshToken` remain stubbed and are wired in Phase 3).

---

## Go Package Contracts (`gitstore-api/internal/auth`)

### `types.go` — Core types and interfaces

All types, interfaces, and sentinel errors defined in this file form the
**stable contract** for the auth framework. Providers, resolvers, and middleware
must only depend on this package — never on concrete provider packages.

#### Error sentinels

```go
var ErrNotSupported = errors.New("auth: operation not supported by this provider")
```

#### `AuthNProvider` interface

```go
type AuthNProvider interface {
    Name()         string
    Capabilities() Capability
    Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error)
    RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error
    RefreshSession(ctx context.Context, oldToken string) (newToken string, exp time.Time, err error)
}
```

**Contract guarantees**:
- `Authenticate` MUST return `OutcomeChallenge` (not `OutcomeDeny`) when the inbound
  request carries credentials that do not belong to this provider. The chain MUST be
  able to continue to the next provider after a Challenge.
- `Authenticate` returning a non-nil error implies `OutcomeDeny`; the chain MUST stop.
- `RevokeSession` and `RefreshSession` MUST return `ErrNotSupported` (not any other
  error) when the provider does not implement the operation.

#### `AuthZProvider` interface

```go
type AuthZProvider interface {
    Name()     string
    Authorize(ctx context.Context, p *Principal, action string, res ResourceContext) (Decision, error)
}
```

**Contract guarantees**:
- `Authorize` MUST return a `Decision` even when `p` is the anonymous principal.
- `action` follows dot-notation: `<resource-kind>.<verb>[.<scope>]`.
  Valid actions for Phase 1: see `data-model.md` action vocabulary table.
- A non-nil error from `Authorize` is treated as `OutcomeDeny` by callers.

#### `UserDirProvider` interface

```go
type UserDirProvider interface {
    Name()         string
    GetBySubject(ctx context.Context, subject string) (*UserProfile, error)
    ListGroups(ctx context.Context, subject string) ([]string, error)
    SearchUsers(ctx context.Context, query string, limit int) ([]*UserProfile, error)
    UpsertProfile(ctx context.Context, p *UserProfile) error
    Deactivate(ctx context.Context, subject string) error
}
```

**Contract guarantees**:
- All methods MUST return `ErrNotSupported` when the provider does not implement
  the operation (e.g., `none` provider).

---

### `context.go` — Context helpers

```go
// ContextWithPrincipal stores p in ctx under the package-private principalContextKey.
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context

// PrincipalFromContext retrieves the Principal from ctx.
// Returns nil if no principal has been stored.
func PrincipalFromContext(ctx context.Context) *Principal
```

**Contract guarantee**: `PrincipalFromContext` never panics; returns `nil` for unauthenticated
contexts. Callers that require authentication MUST check for nil / AuthMethod == "none".

---

### `registry.go` — Provider registry and chain

```go
// NewProviderRegistry constructs a registry. All three arguments are required;
// callers must pass the none-userdir provider when no UserDir is configured.
func NewProviderRegistry(chain *ChainedAuthN, authz AuthZProvider, userdir UserDirProvider) *ProviderRegistry

// NewChainedAuthN constructs an ordered chain of AuthN providers.
// Providers are tried in the order provided; slice must have at least one entry.
func NewChainedAuthN(providers ...AuthNProvider) *ChainedAuthN
```

---

## Middleware contract (`internal/middleware/auth.go`)

```go
// AuthMiddleware validates the inbound request via the ProviderRegistry's ChainedAuthN
// and stores the resulting Principal in the request context.
// If the chain returns Deny (including when credentials are present but unrecognized),
// the middleware returns 401 immediately. Anonymous access is only possible when the
// anonymous provider is in the chain and no credential signals are present.
func AuthMiddleware(registry *auth.ProviderRegistry, logger *zap.Logger) func(http.Handler) http.Handler

// RequireAuth returns 401 if the Principal in ctx is Anonymous (AuthMethod == "none").
// Apply this to all mutation routes.
func RequireAuth(next http.Handler) http.Handler

// GetUserFromContext is a compatibility shim. It wraps auth.PrincipalFromContext and
// returns a synthetic *User. Delete after all callers migrate.
//
// Deprecated: use auth.PrincipalFromContext directly.
func GetUserFromContext(ctx context.Context) (*User, bool)
```

---

## Service contract (`internal/graph/resolver/service.go`)

The following two method signatures change **only in their implementation**, not their
external signature. The `isAdmin bool` parameter is removed and replaced by injecting
the `AuthZProvider` via the `Service` struct.

```go
// CreateNamespace — tier ORGANIZATION now checked via AuthZ:
// authz.Authorize(ctx, principal, "namespace.create.organization", ResourceContext{Kind:"namespace", Attrs:{"tier": tier}})

// DeleteNamespace — owner-or-admin now checked via AuthZ:
// If ns.CreatedBy != principal.Subject:
//   authz.Authorize(ctx, principal, "namespace.delete.any", ResourceContext{Kind:"namespace", Name:id, OwnerSub:ns.CreatedBy})
// Else:
//   authz.Authorize(ctx, principal, "namespace.delete.own", ResourceContext{Kind:"namespace", Name:id})
```

---

## Config contract (Viper keys → env vars)

All new keys follow the existing `GITSTORE_<SECTION>__<KEY>` convention with `__` as
the nested separator.

| Viper key                  | Env var                              | Type       | Default         |
|----------------------------|--------------------------------------|------------|-----------------|
| `auth.authn.chain`         | `GITSTORE_AUTH__AUTHN__CHAIN`        | `[]string` | `["static-admin","anonymous"]` |
| `auth.authz.provider`      | `GITSTORE_AUTH__AUTHZ__PROVIDER`     | `string`   | `"rbac-local"`  |
| `auth.userdir.provider`    | `GITSTORE_AUTH__USERDIR__PROVIDER`   | `string`   | `"none"`        |
| `auth.rbac.policy_file`    | `GITSTORE_AUTH__RBAC__POLICY_FILE`   | `string`   | `"policy.yaml"` |

**Unchanged existing keys** (zero migration required):

| Viper key                      | Env var                                  |
|-------------------------------|-------------------------------------------|
| `auth.admin.username`          | `GITSTORE_AUTH__ADMIN__USERNAME`          |
| `auth.admin.password_hash`     | `GITSTORE_AUTH__ADMIN__PASSWORD_HASH`     |
| `auth.jwt.secret`              | `GITSTORE_AUTH__JWT__SECRET`              |
| `auth.jwt.duration`            | `GITSTORE_AUTH__JWT__DURATION`            |
| `auth.jwt.issuer`              | `GITSTORE_AUTH__JWT__ISSUER`              |

---

## Backward Compatibility Guarantees

1. `GITSTORE_AUTH__ADMIN__*` and `GITSTORE_AUTH__JWT__*` env vars work unchanged.
2. Existing JWT tokens (issued by the current `session.go`) are accepted by the new
   `static-admin` provider — same HS256 + `GITSTORE_AUTH__JWT__SECRET` key.
3. The `Login` mutation output (`LoginPayload`) is unchanged.
4. `middleware.GetUserFromContext` continues to compile and return correct data via
   the compatibility shim.

---

## Policy YAML Schema (rbac-local)

File: path from `GITSTORE_AUTH__RBAC__POLICY_FILE` (default `policy.yaml`)

```yaml
version: v1          # required; only "v1" is valid in Phase 1

roles:
  <name>:
    allow:           # list of action strings; "*" = all actions
      - <action>
    deny:            # list of action strings; explicit deny overrides allow
      - <action>

default_deny: true   # bool; when true, unmatched requests are denied

role_bindings:       # optional; maps subject → roles (used by rbac-local when UserDir = none)
  "<subject>":
    - <role>
```

**Validation rules**:
- `version` must be `"v1"` — unknown versions fail to load.
- At least one role must be defined.
- Role names must be non-empty strings.
- Actions must be non-empty strings.
- `default_deny` defaults to `true` if absent.

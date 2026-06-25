# Data Model: Pluggable AuthN/AuthZ Phase 1

> No new datastore tables. All entities are in-process Go types only.
> The `sessionBlacklist` inside `StaticAdminProvider` uses a `sync.Map` (in-memory).

---

## Core Types (`internal/auth/types.go`)

### Outcome

```
Outcome: uint8
  OutcomeAllow     = 0  // request allowed
  OutcomeDeny      = 1  // request hard-denied (chain stops)
  OutcomeChallenge = 2  // not my token — continue to next provider
```

### Decision

| Field      | Type      | Description                                    |
|------------|-----------|------------------------------------------------|
| Outcome    | Outcome   | Allow / Deny / Challenge                       |
| Reason     | string    | Human-readable explanation                     |
| RequestID  | string    | Propagated request ID (for tracing)            |
| At         | time.Time | When the decision was made                     |
| Provider   | string    | Name of the provider that made this decision   |

**Constructors**: `Allow(provider, reason)`, `Deny(provider, reason)`, `Challenge(provider, reason)`

---

### Principal

Represents the authenticated identity flowing through the system after AuthN.

| Field      | Type            | JSON tag        | Description                                       |
|------------|-----------------|-----------------|---------------------------------------------------|
| Subject    | string          | `sub`           | Unique identifier (username for static-admin)     |
| Issuer     | string          | `iss`           | Token issuer (e.g., `"gitstore"`)                 |
| Tenant     | string          | `tenant`        | Optional multi-tenancy discriminator              |
| Namespace  | string          | `namespace`     | Optional namespace scope                          |
| Groups     | []string        | `groups`        | Group memberships                                 |
| Roles      | []string        | `roles`         | Role names (e.g., `["admin"]`)                    |
| Scopes     | []string        | `scopes`        | OAuth2-style scopes                               |
| Claims     | map[string]any  | `claims`        | Extra provider-specific claims                    |
| AuthMethod | string          | `auth_method`   | e.g., `"static-admin"`, `"none"`                  |
| ExpiresAt  | time.Time       | `exp`           | Token expiry (zero for non-expiring)              |

**Methods**:
- `IsAdmin() bool` — true if `"admin"` is in `Roles`
- `Anonymous() *Principal` — returns `Principal{Subject:"anon", Issuer:"gitstore", AuthMethod:"none"}`

**Replaces**: `middleware.User{Username string, IsAdmin bool}` from `internal/middleware/auth.go`

---

### Capability

Bitmask of operations a provider can perform.

| Flag               | Value   | Meaning                                      |
|--------------------|---------|----------------------------------------------|
| CapAuthenticate    | 1 << 0  | Provider can authenticate requests           |
| CapIssueSession    | 1 << 1  | Provider can issue session tokens            |
| CapIntrospect      | 1 << 2  | Provider can introspect tokens               |
| CapGroupResolution | 1 << 3  | Provider can resolve group memberships       |
| CapUserLookup      | 1 << 4  | Provider can look up user profiles           |

---

### AuthRequest

Wraps the inbound HTTP request for testability (avoids `*http.Request` in tests).

| Field            | Type        | Description                                                  |
|------------------|-------------|--------------------------------------------------------------|
| Header           | http.Header | HTTP headers from the inbound request                        |
| RemoteAddr       | string      | Client IP:port                                               |
| ForwardedSubject | string      | Non-empty when request came via gRPC metadata (Phase 5)      |

---

### ResourceContext

Carries resource metadata for AuthZ decisions.

| Field    | Type           | Description                                         |
|----------|----------------|-----------------------------------------------------|
| Kind     | string         | Resource kind, e.g., `"namespace"`, `"repository"`  |
| Name     | string         | Resource name                                       |
| OwnerSub | string         | Subject of the resource owner                       |
| Attrs    | map[string]any | Extra attributes (e.g., `{"tier": "ORGANIZATION"}`) |

---

### UserProfile

Provider-agnostic user record for the UserDir plane.

| Field       | Type     | Description                     |
|-------------|----------|---------------------------------|
| Subject     | string   | Unique identifier               |
| DisplayName | string   | Human-readable name             |
| Email       | string   | Email address                   |
| Groups      | []string | Group memberships               |
| Active      | bool     | Whether the account is active   |

---

## Interfaces (`internal/auth/types.go`)

### AuthNProvider

```
interface AuthNProvider {
  Name()          string
  Capabilities()  Capability
  Authenticate(ctx, AuthRequest) → (*Principal, Decision, error)
  RevokeSession(ctx, jti string, expiresAt time.Time) → error
  RefreshSession(ctx, oldToken string)  → (newToken string, exp time.Time, error)
}
```

### AuthZProvider

```
interface AuthZProvider {
  Name()     string
  Authorize(ctx, *Principal, action string, ResourceContext) → (Decision, error)
}
```

Action vocabulary (dot-notation):

| Action                          | Meaning                                               |
|---------------------------------|-------------------------------------------------------|
| `namespace.create.organization` | Create a namespace with ORGANIZATION tier             |
| `namespace.delete.own`          | Delete a namespace you own                            |
| `namespace.delete.any`          | Delete any namespace (admin only)                     |
| `namespace.read`                | Read namespace metadata                               |
| `namespace.update`              | Update namespace                                      |
| `repository.read`               | Read repository                                       |
| `repository.write`              | Write to repository                                   |
| `repository.create`             | Create repository                                     |
| `repository.delete.own`         | Delete repository you own                             |

### UserDirProvider

```
interface UserDirProvider {
  Name()          string
  GetBySubject(ctx, subject)  → (*UserProfile, error)
  ListGroups(ctx, subject)    → ([]string, error)
  SearchUsers(ctx, query, limit) → ([]*UserProfile, error)
  UpsertProfile(ctx, *UserProfile) → error
  Deactivate(ctx, subject)    → error
}
```

---

## Provider Registry (`internal/auth/registry.go`)

```
ProviderRegistry
  mu:          sync.RWMutex
  authnChain:  *ChainedAuthN
  authz:       AuthZProvider
  userdir:     UserDirProvider

  AuthN()    → *ChainedAuthN
  AuthZ()    → AuthZProvider
  UserDir()  → UserDirProvider
```

---

## ChainedAuthN (`internal/auth/registry.go`)

```
ChainedAuthN
  providers: []AuthNProvider

  Authenticate(ctx, AuthRequest) → (*Principal, Decision, error)
    for each provider:
      Allow     → return immediately (first-Allow-wins)
      Deny      → return immediately (short-circuit)
      Challenge → continue to next provider
    fallthrough (all Challenge) → return nil, Deny("chain", "credentials present but no provider accepted them")

  Note: the only legitimate path to Anonymous() is via AnonymousProvider.Authenticate
        returning Allow when no credential signals are present.

  RevokeSession(ctx, jti, expiresAt) → error
    delegates to first provider that doesn't return ErrNotSupported
```

---

## Provider Implementations

### static-admin (`internal/auth/provider/staticadmin/`)

State:
- `username string` — from `GITSTORE_AUTH__ADMIN__USERNAME`
- `passwordHash string` — bcrypt hash from `GITSTORE_AUTH__ADMIN__PASSWORD_HASH`
- `blacklist *sessionBlacklist` — in-memory `sync.Map[jti → expiresAt]`

`sessionBlacklist`:
- `add(jti, expiresAt)` — stores `jti` until `expiresAt`
- `isRevoked(jti) bool` — returns true if present and not yet pruned
- Background goroutine: prunes expired entries every 5 minutes

Authenticate flow:
1. Extract `Authorization: Bearer <jwt>` header
2. Parse JWT with `golang-jwt/v5` (HS256, `GITSTORE_AUTH__JWT__SECRET`)
3. Parse failure → `OutcomeChallenge` (not my token)
4. Validate issuer == `GITSTORE_AUTH__JWT__ISSUER`
5. Check blacklist by `jti` claim → if revoked → `OutcomeDeny`
6. Build `Principal{Subject: claims.sub, Roles: ["admin"], AuthMethod: "static-admin"}`
7. Return `Allow`

Also handles Basic Auth (for Git smart-HTTP in Phase 5):
1. Extract `Authorization: Basic <b64>`
2. Decode credentials
3. Compare username, `bcrypt.CompareHashAndPassword(hash, password)`
4. On match → build Principal + return `Allow`

Capabilities: `CapAuthenticate | CapIssueSession | CapIntrospect`

---

### allow-all (`internal/auth/provider/allowall/`)

State: stateless

`Authorize(ctx, principal, action, resource)` always returns `Allow("allow-all", "allow-all provider permits everything")`.

Startup: emits `warn`-level zap log `"SECURITY: authz provider is allow-all — ALL authorization checks are disabled. DO NOT use in production."`.

---

### rbac-local (`internal/auth/provider/rbaclocal/`)

State:
- `mu sync.RWMutex`
- `policy *Policy` — loaded from YAML file
- `path string` — `GITSTORE_AUTH__RBAC__POLICY_FILE` (default `"policy.yaml"`)

Policy YAML schema (`v1`):

```yaml
version: v1
roles:
  <role-name>:
    allow: [<action>, ...]   # supports "*" wildcard
    deny: [<action>, ...]
default_deny: true
role_bindings:
  "<subject>": [<role>, ...]
```

Authorize algorithm:
1. For each role in `principal.Roles`:
   - Look up role in `policy.Roles`
   - Check if `action` is in `role.Allow` (including `"*"` wildcard)
   - Check if `action` matches any `role.Deny` entry
2. Explicit deny from any matched role → `OutcomeDeny`
3. Any allow + no deny → `OutcomeAllow`
4. No match + `default_deny: true` → `OutcomeDeny`

Hot-reload: triggered by SIGHUP handler (Phase 1); no `fsnotify` watcher.

---

### anonymous (`internal/auth/provider/anonymous/`)

State: stateless

`Authenticate(ctx, req)` flow:
1. Check `req.Header.Get("Authorization") != ""` or `req.ForwardedSubject != ""`
2. If credentials present → return `OutcomeChallenge` (let chain fall through to Deny)
3. If no credentials → return `Anonymous(), OutcomeAllow("anonymous", "no credentials presented")`

Capabilities: `CapAuthenticate`

**Placement constraint**: must be the last entry in `GITSTORE_AUTH__AUTHN__CHAIN`. Placing it earlier would prevent subsequent providers from running.

---

### none-userdir (`internal/auth/provider/userdirnone/`)

All methods return `(nil, auth.ErrNotSupported)`. Used when `GITSTORE_AUTH__USERDIR__PROVIDER=none`.

---

## Context helpers (`internal/auth/context.go`)

```go
type principalContextKey struct{}

func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context
func PrincipalFromContext(ctx context.Context) *Principal
```

---

## Compatibility shim (`internal/middleware/auth.go`)

Existing `GetUserFromContext(ctx) (*User, bool)` is updated to call `auth.PrincipalFromContext`
and return a synthetic `*middleware.User{Username: p.Subject, IsAdmin: p.IsAdmin()}`.
This shim is deleted after all callers have migrated to `auth.PrincipalFromContext`.

---

## Wiring (`internal/app/server.go`)

At startup:
1. Read `auth.authn.chain` (e.g., `["static-admin"]`) from Viper
2. Instantiate each named provider
3. Build `ChainedAuthN` from the list
4. Read `auth.authz.provider` (e.g., `"allow-all"` or `"rbac-local"`)
5. Instantiate the AuthZ provider
6. Read `auth.userdir.provider` (e.g., `"none"`)
7. Instantiate the UserDir provider
8. Construct `ProviderRegistry(chain, authz, userdir)`
9. Inject registry into resolver `Service` and HTTP middleware

---

## State Transitions

### Principal lifecycle

```
(request arrives)
  → ChainedAuthN.Authenticate
      → OutcomeAllow (authenticated provider) → Principal stored in context
      → OutcomeAllow (AnonymousProvider)      → Anonymous() stored in context
      → OutcomeDeny                           → 401 returned immediately
      → (all Challenge, no provider claimed)  → Deny → 401 returned
  → resolver reads PrincipalFromContext
  → AuthZProvider.Authorize
      → OutcomeAllow  → mutation proceeds
      → OutcomeDeny   → ErrForbidden returned
```

### Token blacklist (static-admin)

```
jti absent from blacklist  → valid
jti present + not expired  → revoked (OutcomeDeny)
jti present + expired      → pruned by background goroutine → treated as absent
```

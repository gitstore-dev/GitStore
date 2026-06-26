# Data Model: Pluggable AuthN/AuthZ — Phase 3 Session Lifecycle

**Branch**: `032-auth-phase-3` | **Date**: 2026-06-26  
**Spec**: [spec.md](spec.md) | **Research**: [research.md](research.md)

---

## Entities

### 1. `auth.Principal` (modified)

Defined in `internal/auth/types.go`. Phase 3 adds one field.

| Field | Type | Description | Set by |
|-------|------|-------------|--------|
| `Subject` | `string` | Authenticated user identifier (e.g. `"admin"`) | All providers |
| `Issuer` | `string` | Token issuer (e.g. `"gitstore"`) | All providers |
| `Tenant` | `string` | Optional tenant scope | Future |
| `Namespace` | `string` | Optional namespace scope | Future |
| `Groups` | `[]string` | Group memberships | Provider-specific |
| `Roles` | `[]string` | Role assignments (e.g. `["admin"]`) | All providers |
| `Scopes` | `[]string` | OAuth-style scopes | Future |
| `Claims` | `map[string]any` | Provider-specific extra claims | Provider-specific |
| `AuthMethod` | `string` | Provider that authenticated this principal (e.g. `"static-admin"`, `"none"`) | All providers |
| `ExpiresAt` | `time.Time` | Token expiry (zero = no expiry) | Static-admin |
| **`TokenID`** | **`string`** | **JWT `jti` claim — unique per-token identifier. Required for blacklist-based revocation via `RevokeSession`. Empty when the token has no `jti` (e.g. Basic Auth sessions, anonymous).** | **Static-admin (Bearer only)** |

**Validation rules**:
- `TokenID` is empty-string-safe — `RevokeSession` with an empty `jti` is a no-op.
- `IsAdmin()` returns `true` iff `Roles` contains `"admin"`.

---

### 2. `auth.AuthNProvider` interface (modified)

Defined in `internal/auth/types.go`. Phase 3 adds `IssueSession`.

```
IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)
```

Providers that do not support session issuance return `ErrNotSupported`.

| Provider | IssueSession | RevokeSession | RefreshSession |
|----------|-------------|---------------|----------------|
| `static-admin` | ✅ Issues HS256 JWT | ✅ Blacklists jti | ✅ Rotates token |
| `anonymous` | ❌ ErrNotSupported | ❌ ErrNotSupported | ❌ ErrNotSupported |
| `allow-all` (AuthZ only) | N/A | N/A | N/A |

---

### 3. `ChainedAuthN` (modified)

Defined in `internal/auth/registry.go`. Phase 3 adds one method.

**`IssueSession(ctx, subject)`**: Iterates providers; returns the first non-`ErrNotSupported` result. If all return `ErrNotSupported`, returns `ErrNotSupported`.

---

### 4. `StaticAdminProvider` (modified)

Defined in `internal/auth/provider/staticadmin/provider.go`.

**New fields**:

| Field | Type | Source | Default |
|-------|------|--------|---------|
| `refreshGrace` | `time.Duration` | `GITSTORE_AUTH__JWT__REFRESH_GRACE` | `60s` |

**New method**: `IssueSession(ctx, subject)` — thin wrapper over existing `issueToken(subject)`.

**Modified method**: `authenticateBearer` sets `principal.TokenID = claims.ID` on successful validation.

**Modified method**: `RefreshSession` enforces grace window:
- After parsing with `WithoutClaimsValidation`, check if token expired more than `refreshGrace` ago.
- Return an error if so (token too old to refresh).
- Otherwise proceed with existing rotate-and-revoke logic.

---

### 5. `sessionBlacklist` (unchanged)

Defined in `internal/auth/provider/staticadmin/provider.go`.

In-memory `map[jti]expiresAt` protected by `sync.RWMutex`. Background goroutine prunes entries after `expiresAt`. No changes needed in Phase 3.

---

### 6. Raw Token Context (new)

Two helpers added to `internal/auth/context.go`:

```
ContextWithRawToken(ctx context.Context, rawToken string) context.Context
RawTokenFromContext(ctx context.Context) string  // "" if not set
```

`ChainAuthMiddleware` calls `ContextWithRawToken` with the extracted Bearer value **before** calling the chain. The `refreshToken` resolver reads it via `RawTokenFromContext`.

**Why not parse the raw token in the resolver?** Parsing twice is redundant; the provider already validated and decoded the token. The raw string is a transport-layer artefact; wrapping it in a context key keeps the resolver clean.

---

### 7. `Resolver` struct (modified)

Defined in `internal/graph/resolver/resolver.go`.

**New field**: `registry *auth.ProviderRegistry`

| Field | Purpose |
|-------|---------|
| `registry` | Provides `AuthN()` chain for Login/Logout/Refresh resolvers |
| `authMiddleware` | Retained during transition; removed when `authMiddleware` code paths are fully replaced |
| `service` | Datastore + authz operations (unchanged) |
| `clock` | Time source (unchanged) |

---

## State Transitions

### Token Lifecycle

```
ISSUED (valid jwt, not in blacklist)
  │
  ├── Authenticate succeeds → ACTIVE
  │
  ├── logout called → RevokeSession(jti, exp) → REVOKED (in blacklist until exp)
  │        │
  │        └── Any request with this token → OutcomeDeny "token has been revoked"
  │
  ├── refreshToken called → RefreshSession(rawToken)
  │        │
  │        ├── old token blacklisted (jti → exp)
  │        └── new token ISSUED with new jti and exp
  │
  └── exp passes (no action) → EXPIRED
           │
           └── Authenticate → OutcomeDeny "token has expired" (if within leeway)
                          → OutcomeChallenge "jwt parse failed" (if leeway exceeded)
```

### Refresh Grace Window

```
Token expires at T
│
├── T → T+grace  : refreshToken succeeds (recent expiry, common in fast-moving clients)
└── T+grace+ε   : refreshToken fails ("token too old to refresh") → user must re-login
```

---

## Configuration Keys (Phase 3 additions)

| Env Var | Viper Key | Default | Purpose |
|---------|-----------|---------|---------|
| `GITSTORE_AUTH__JWT__REFRESH_GRACE` | `auth.jwt.refresh_grace` | `60s` | Max age beyond expiry for which refreshToken is still accepted |

All existing keys (`GITSTORE_AUTH__ADMIN__*`, `GITSTORE_AUTH__JWT__SECRET`, `GITSTORE_AUTH__JWT__DURATION`, `GITSTORE_AUTH__JWT__ISSUER`) are unchanged.

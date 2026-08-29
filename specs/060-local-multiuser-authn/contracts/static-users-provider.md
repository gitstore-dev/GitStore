# Contract: `static-users` AuthN Provider

## `AuthNProvider` interface compliance

`static-users` implements the existing, unmodified `gitstore-api/internal/auth.AuthNProvider` interface — no interface change:

```go
type AuthNProvider interface {
    Name() string
    Capabilities() Capability
    Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error)
    RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error
    RefreshSession(ctx context.Context, oldToken string) (newToken string, exp time.Time, err error)
    IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)
}
```

| Method | Contract |
|---|---|
| `Name()` | Returns `"static-users"`. |
| `Capabilities()` | `CapAuthenticate \| CapIssueSession \| CapIntrospect` — identical capability set to `static-admin`. |
| `Authenticate` | Basic Auth: look up `username` in the loaded user map; `bcrypt.CompareHashAndPassword` against the stored hash; on match, return `Allow` with a `Principal` per `data-model.md`'s Principal shape (no hardcoded roles). Bearer: verify signature with `auth.staticusers.jwt.secret` and `jwt.WithIssuer(auth.staticusers.jwt.issuer)`; on success, check the provider's own blacklist by `jti`; on any parse/verify failure that is not "wrong key/issuer" ambiguity, return `Challenge` (not my token) exactly mirroring `static-admin.authenticateBearer`'s `Challenge` vs. `Deny` split for expired-vs-foreign tokens. Unknown username, wrong password, or unrecognized scheme → `Challenge`, never `Deny` — this preserves the existing chain contract that "not my credential" falls through to the next provider rather than hard-failing the whole chain (mirrors `static-admin.authenticateBasic`'s `Challenge`-on-unknown-username behavior exactly). |
| `RevokeSession` | Adds `jti` to `static-users`' own in-memory blacklist (structurally identical to `staticadmin`'s `sessionBlacklist`, own instance, own goroutine). Returns `nil` unconditionally, exactly as `static-admin.RevokeSession` does — `ChainedAuthN.RevokeSession` already broadcasts to every provider that returns non-`ErrNotSupported`, so both providers' blacklists get the revocation regardless of which one issued the original token. |
| `RefreshSession` | Parses `oldToken` with `static-users`' own secret/issuer, honoring the same expired-within-leeway (`jwt.WithLeeway(2*time.Minute)`) and refresh-grace-window semantics `static-admin.RefreshSession` uses. **Must** fail (return an error, not silently no-op) for a token that does not verify against its own secret/issuer — this is what makes provider-identity routing (see below) safe even before any registry-level change lands, since a misrouted `RefreshSession(oldToken)` call to the wrong provider will simply fail with a parse error rather than succeeding incorrectly. |
| `IssueSession` | Mints a new HS256 JWT for the given `subject`, signed with `auth.staticusers.jwt.secret`/`issuer`, generating a fresh `jti`. **Constraint (FR-007)**: this method itself has no way to reject an "unowned" subject (it has no record of which subject just authenticated via which provider — that context lives one layer up, in the `Login` resolver) — closing FR-007 is the registry/resolver-level responsibility described below, not a per-method validation inside `IssueSession` itself. |

## Provider-identity-routed session issuance (registry/resolver-level contract)

To satisfy FR-007/FR-006 without any change to `static-admin`'s or `static-users`' own provider code, the shared chain/registry plumbing gains one additive method:

```go
// gitstore-api/internal/auth/registry.go — additive, existing methods unchanged
func (c *ChainedAuthN) IssueSessionFor(ctx context.Context, principal *Principal) (string, time.Time, error) {
    for _, p := range c.providers {
        if p.Name() == principal.AuthMethod {
            return p.IssueSession(ctx, principal.Subject)
        }
    }
    return "", time.Time{}, ErrNotSupported
}
```

And the `Login` resolver (`gitstore-api/internal/graph/resolver/auth_service.go`) calls `IssueSessionFor(ctx, principal)` instead of the existing generic `IssueSession(ctx, principal.Subject)`, using the `principal.AuthMethod` that `Authenticate` already populated (it is set to `decision.Provider`'s underlying provider name for both `static-admin` and `static-users` already — see `staticadmin/provider.go`'s `AuthMethod: "static-admin"` literal and this spec's own `data-model.md` Principal shape for `static-users`).

This is the only change to shared (non-provider-specific) code this feature makes. `ChainedAuthN.IssueSession` (the generic, first-provider-wins method) remains for any future caller that legitimately does not have a `Principal` in hand (none exists today), preserving backward compatibility.

## `buildProviderRegistry` wiring contract

`gitstore-api/internal/app/server.go`'s existing `switch name { case "static-admin": ...; case "anonymous": ...; default: ... }` dispatch (inside `buildProviderRegistry`) gains one additional case, structurally identical to the `static-admin` case:

```go
case "static-users":
    p, err := staticusers.New(cfg.Auth.StaticUsers, log)
    if err != nil {
        return nil, nil, nil, fmt.Errorf("init static-users provider: %w", err)
    }
    authnProviders = append(authnProviders, p)
    shutdowns = append(shutdowns, p)          // blacklist-pruning goroutine, mirrors static-admin
```

`static-users` also implements the same `policyReloader`-shaped `Reload() error` method `rbac-local` exposes, so the existing SIGHUP handler in `Start()` can be extended (additive change, existing SIGHUP wiring untouched for `rbac-local`) to also reload any active `static-users` provider found in the chain.

## Chain ordering guidance (non-normative operator documentation, not enforced by code)

Because `Authenticate` correctly falls through via `Challenge` regardless of order (each provider only claims requests it recognizes), and because `IssueSessionFor`/`RefreshSession`'s own-secret-verification close the ordering hazard described in `research.md` Decision 4, **no specific chain order is required for correctness**. The documented example order is:

```text
GITSTORE_AUTH__AUTHN__CHAIN=static-admin,static-users,anonymous
```

matching the existing convention that `anonymous` remains last (`docs/implementation/020-pluggable_auth_architecture.md` §2e already documents this constraint and is unchanged by this spec).

## Example `.env` additions (local-multiuser profile)

```text
# Additive to the existing local-fast profile — static-admin is untouched.
GITSTORE_AUTH__AUTHN__CHAIN=static-admin,static-users,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local
GITSTORE_AUTH__RBAC__POLICY_FILE=policy.yaml
GITSTORE_AUTH__STATICUSERS__USERS_FILE=users.yaml
GITSTORE_AUTH__STATICUSERS__JWT__SECRET=dev-static-users-secret-change-me
GITSTORE_AUTH__STATICUSERS__JWT__ISSUER=gitstore-static-users
```

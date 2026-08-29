# Contract: `static-users` AuthN + UserDir Provider

## `AuthNProvider` interface compliance

`static-users` implements the existing, unmodified `gitstore-api/internal/auth.AuthNProvider` interface:

| Method | Contract |
|---|---|
| `Name()` | Returns `"static-users"`. |
| `Capabilities()` | `CapAuthenticate \| CapIssueSession \| CapIntrospect \| CapUserLookup` — the last flag is new relative to `static-admin`'s former set, reflecting the new `UserDirProvider` implementation below. |
| `Authenticate` | Basic Auth: look up `username` in the loaded user map; `bcrypt.CompareHashAndPassword` against the stored hash; on match, return `Allow` with a `Principal` per `data-model.md`'s Principal shape (no hardcoded roles — FR-004). Bearer: verify signature with `auth.jwt.secret` and `jwt.WithIssuer(auth.jwt.issuer)`; on success, check the provider's own blacklist by `jti`. Unknown username, wrong password, or unrecognized scheme → `Challenge`, mirroring `static-admin.authenticateBasic`'s former behavior exactly (falls through the chain rather than hard-failing). |
| `RevokeSession` | Adds `jti` to `static-users`' own in-memory blacklist (structurally identical to `staticadmin`'s former `sessionBlacklist`). |
| `RefreshSession` | Parses `oldToken` with `static-users`' secret/issuer, honoring the same expired-within-leeway (`jwt.WithLeeway(2*time.Minute)`) and refresh-grace-window semantics `static-admin.RefreshSession` used. |
| `IssueSession` | Mints a new HS256 JWT for the given `subject`, signed with `auth.jwt.secret`/`issuer`, generating a fresh `jti`. Since `static-users` is the only provider in any currently-designed chain that supports `IssueSession` (research.md Decision 8), `ChainedAuthN.IssueSession`'s existing "first supporter wins" resolution is unambiguous — no registry-level change is needed for this method's correctness. |

## `UserDirProvider` interface compliance (new)

`static-users` also implements the existing, unmodified `gitstore-api/internal/auth.UserDirProvider` interface:

| Method | Contract |
|---|---|
| `Name()` | Returns `"static-users"` (shared with the `AuthNProvider` side — one Go type implements both interfaces). |
| `GetBySubject(ctx, subject)` | Returns a `*auth.UserProfile` per `data-model.md`'s shape for a known username. Returns `staticusers.ErrUserNotFound` (new sentinel, distinct from `auth.ErrNotSupported`) for an unknown one. |
| `ListGroups(ctx, subject)` | Returns `(nil, nil)` for a known username (no groups concept in `users.yaml` — "zero groups" is a correct, supported answer). Returns `(nil, staticusers.ErrUserNotFound)` for an unknown one. |
| `SearchUsers(ctx, query, limit)` | Case-insensitive substring match over `username`/`display_name`/`email`, capped at `limit`. Sufficient for this feature's "tens, not thousands" scale (spec.md Assumptions). |
| `UpsertProfile(ctx, p)` | Returns `auth.ErrNotSupported` — no mutation API in v1 (FR-010). |
| `Deactivate(ctx, subject)` | Returns `auth.ErrNotSupported` — same reason. |

No resolver wires `registry.UserDir()` to anything as part of this spec (research.md Decision 11) — these methods are implemented, tested, and ready for a future consumer, but have none today, matching this spec's own finding that `registry.UserDir()` has zero live callers anywhere in `gitstore-api/internal/graph`.

## `buildProviderRegistry` wiring contract

`gitstore-api/internal/app/server.go`'s `switch name { case "static-admin": ...; case "anonymous": ...; default: ... }` dispatch (inside `buildProviderRegistry`) loses its `"static-admin"` case and gains a `"static-users"` case:

```go
case "static-users":
    p, err := staticusers.New(cfg.Auth, log)   // cfg.Auth.JWT + cfg.Auth.StaticUsers
    if err != nil {
        return nil, nil, nil, fmt.Errorf("init static-users provider: %w", err)
    }
    authnProviders = append(authnProviders, p)
    shutdowns = append(shutdowns, p)          // blacklist-pruning goroutine, mirrors static-admin's former shutdown
```

`buildProviderRegistry` also wires `static-users` as the `UserDirProvider` when it is active (replacing the unconditional `userdirnone.New()` call): if `"static-users"` is in the chain, the same constructed `*staticusers.StaticUsersProvider` instance is passed as the `UserDirProvider` to `auth.NewProviderRegistry`; otherwise `userdirnone.New()` remains the default (e.g., for an `anonymous`-only or future `oidc-jwt`-only chain with no local user list to serve directory lookups from).

The default chain literal changes from `[]string{"static-admin", "anonymous"}` to `[]string{"static-users", "anonymous"}` — both the inline fallback in `buildProviderRegistry` and `config.go`'s `v.SetDefault("auth.authn.chain", ...)`.

`static-users` also implements the same `policyReloader`-shaped `Reload() error` method `rbac-local` exposes, so the existing SIGHUP handler in `Start()` is extended (additive change, existing `rbac-local` SIGHUP wiring untouched) to also reload any active `static-users` provider.

## Migration-safety check placement (FR-013)

Implemented in `buildProviderRegistry`, immediately after both the `static-users` and (if active) `rbac-local` providers are constructed — this is the first point both providers' *loaded* state (the actual username list and the actual `role_bindings` map) is available in the same scope:

```go
if staticUsersProvider != nil && cfg.Auth.AuthZ.Provider == "rbac-local" {
    if !rbacLocalProvider.HasAnyRoleBindingFor(staticUsersProvider.Usernames()) {
        return nil, nil, nil, fmt.Errorf(
            "static-users is configured with %d user(s), but rbac-local's policy.yaml has no " +
            "role_bindings entry for any of them — every static-users login would be denied by " +
            "default_deny; add a role_bindings entry for at least one configured username",
            len(staticUsersProvider.Usernames()),
        )
    }
}
```

`RBACLocalProvider.HasAnyRoleBindingFor([]string) bool` is a small, additive, read-only helper added to `rbaclocal/provider.go` (checks `policy.RoleBindings` for any of the given usernames as a key) — this is the one place `rbac-local`'s own package gains a new method in this spec, and it is a pure read-only query over already-loaded state, not a change to `Authorize`'s decision logic (research.md Decision 9's "zero source changes to `Authorize`/`Policy` semantics" finding is preserved; this is additive, not a semantic change).

## Config-validation contract (FR-014/FR-015/FR-016)

```go
// gitstore-api/internal/config/config.go — additive function, called from validateConfig
// alongside the existing validateDatastoreConfig/validateLogFormat calls.
func validateAuthChainConfig(cfg *Config) error {
    hasStaticUsers := slices.Contains(cfg.Auth.AuthN.Chain, "static-users")
    if hasStaticUsers && cfg.Auth.JWT.Secret == "" {
        return errors.New("auth.jwt.secret is required when static-users is in auth.authn.chain")
    }
    return nil
}
```

`JWTConfig.Secret`'s struct tag changes from `validate:"secret" validate:"required"` to no `validate` tag at all — the requirement moves entirely into this explicit function. `GrpcAuthConfig.HmacSecret`'s `validate:"required"` tag is **not touched** (FR-015). The new `auth.staticusers.users_file` key gets no `validate` tag either (FR-016) — its own loadability is enforced only inside `buildProviderRegistry`'s `case "static-users":`, exactly the pattern this fix generalizes from.

## `Principal.IsAdmin()` documentation contract (FR-022)

```go
// IsAdmin returns true when the principal carries the built-in "admin" role.
//
// This reflects only roles the AuthN provider itself set on Principal.Roles at
// authentication time (e.g. static-admin's former hardcoded assignment). It does
// NOT reflect roles granted transiently by an AuthZProvider's own subject-keyed
// mechanism (e.g. rbac-local's role_bindings), which never writes back onto
// Principal. For any provider that defers role assignment to the AuthZ layer —
// static-users included — this method always returns false, regardless of what
// role_bindings grants that subject. Callers needing an authoritative admin
// check MUST call AuthZProvider.Authorize instead.
func (p *Principal) IsAdmin() bool { ... }
```

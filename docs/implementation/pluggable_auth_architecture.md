# GitStore Pluggable AuthN/AuthZ Architecture Design

> Generated 2026-06-20 via deep-research workflow (110 agents, 27 sources, 20 verified claims).
> This document supersedes the open decisions in `pluggable_auth_design.md`.

**Research-verified findings driving key decisions:**
- `go-oidc/v3` is a required new dependency (golang-jwt/v5 already in go.mod cannot perform JWKS-backed RSA/EC verification)
- Tonic gRPC interceptors in v0.14 are synchronous (`fn check_auth`) — no `async` in the trait
- Shared HMAC token is the pragmatic phase-1 choice; mTLS moves to phase 2 when cert infra matures
- OPA is chosen over OpenFGA for production AuthZ (file-based Rego aligns with the rbac-local migration path)
- RFC 7519 MAY leeway for clock skew is operative — configure 1–2 min leeway in the oidc-jwt provider
- `RemoteKeySet` does NOT guarantee immediate rotation propagation; force `Refresh()` on key-not-found before returning 401

---

## 1. Interface Contracts

### 1a. Go Core Interfaces and Types

```go
// Package auth defines the canonical types shared by all auth planes.
// File: gitstore-api/internal/auth/types.go
package auth

import (
    "context"
    "net/http"
    "time"
)

// Outcome is the result of an auth decision.
type Outcome uint8

const (
    OutcomeAllow     Outcome = iota
    OutcomeDeny
    OutcomeChallenge // issued when credentials are present but need a second factor
)

// Decision is returned by AuthNProvider.Authenticate and AuthZProvider.Authorize.
type Decision struct {
    Outcome   Outcome   `json:"outcome"`
    Reason    string    `json:"reason"`
    RequestID string    `json:"request_id"`
    At        time.Time `json:"at"`
    // Provider that produced this decision (e.g. "static-admin", "oidc-jwt").
    Provider  string    `json:"provider"`
}

func Allow(provider, reason string) Decision {
    return Decision{Outcome: OutcomeAllow, Provider: provider, Reason: reason, At: time.Now()}
}

func Deny(provider, reason string) Decision {
    return Decision{Outcome: OutcomeDeny, Provider: provider, Reason: reason, At: time.Now()}
}

func Challenge(provider, reason string) Decision {
    return Decision{Outcome: OutcomeChallenge, Provider: provider, Reason: reason, At: time.Now()}
}

// Principal is the provider-agnostic identity passed through the system.
// After authentication, every request carries exactly one Principal in context.
type Principal struct {
    Subject    string            `json:"sub"`
    Issuer     string            `json:"iss"`
    Tenant     string            `json:"tenant,omitempty"`
    Namespace  string            `json:"namespace,omitempty"`
    Groups     []string          `json:"groups,omitempty"`
    Roles      []string          `json:"roles,omitempty"`
    Scopes     []string          `json:"scopes,omitempty"`
    Claims     map[string]any    `json:"claims,omitempty"`
    AuthMethod string            `json:"auth_method"` // e.g. "static-admin", "oidc-jwt"
    ExpiresAt  time.Time         `json:"exp,omitempty"`
}

// IsAdmin returns true when the principal carries the built-in "admin" role.
// This replaces the former middleware.User.IsAdmin boolean field.
func (p *Principal) IsAdmin() bool {
    for _, r := range p.Roles {
        if r == "admin" {
            return true
        }
    }
    return false
}

// Anonymous returns a Principal with no identity (anon calls).
func Anonymous() *Principal {
    return &Principal{Subject: "anon", Issuer: "gitstore", AuthMethod: "none"}
}

// Capability flags returned by AuthNProvider.Capabilities.
type Capability uint32

const (
    CapAuthenticate       Capability = 1 << iota
    CapIssueSession
    CapIntrospect
    CapGroupResolution
    CapUserLookup
)

// AuthRequest wraps the inbound HTTP request to pass to AuthNProvider.Authenticate.
// This is intentionally not *http.Request to keep the interface testable.
type AuthRequest struct {
    Header     http.Header
    RemoteAddr string
    // ForwardedSubject is non-empty when the request arrived via gRPC metadata
    // from the Git-HTTP proxy. Providers that see this skip HTTP credential extraction.
    ForwardedSubject string
}

// AuthNProvider authenticates an inbound request and returns a Principal + Decision.
type AuthNProvider interface {
    // Name returns the canonical provider name (e.g. "static-admin").
    Name() string
    // Capabilities returns the set of operations this provider can perform.
    Capabilities() Capability
    // Authenticate inspects req and returns (principal, decision).
    // Returns OutcomeAllow with a populated Principal on success.
    // Returns OutcomeDeny to hard-fail (chain stops).
    // Returns a nil Principal + OutcomeChallenge to signal "not my token, try next".
    Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error)
    // RevokeSession invalidates the token identified by jti.
    // Providers that don't support revocation return ErrNotSupported.
    RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error
    // RefreshSession validates oldToken and issues a replacement.
    // Providers that don't support refresh return ErrNotSupported.
    RefreshSession(ctx context.Context, oldToken string) (newToken string, exp time.Time, err error)
}

// ResourceContext carries the resource identifier passed to AuthZProvider.Authorize.
type ResourceContext struct {
    Kind      string         // e.g. "namespace", "repository"
    Name      string         // e.g. "my-org"
    OwnerSub  string         // subject of the resource owner, if known
    Attrs     map[string]any // extra attributes (e.g. tier)
}

// AuthZProvider makes access-control decisions for a given (principal, action, resource).
type AuthZProvider interface {
    Name() string
    // Authorize returns Allow or Deny for the given action on the resource.
    // action follows a dot-notation: "namespace.delete.any", "repository.write".
    Authorize(ctx context.Context, p *Principal, action string, res ResourceContext) (Decision, error)
}

// UserProfile is a provider-agnostic user record.
type UserProfile struct {
    Subject     string
    DisplayName string
    Email       string
    Groups      []string
    Active      bool
}

// UserDirProvider provides user-directory operations (optional plane).
type UserDirProvider interface {
    Name() string
    GetBySubject(ctx context.Context, subject string) (*UserProfile, error)
    ListGroups(ctx context.Context, subject string) ([]string, error)
    SearchUsers(ctx context.Context, query string, limit int) ([]*UserProfile, error)
    UpsertProfile(ctx context.Context, p *UserProfile) error
    Deactivate(ctx context.Context, subject string) error
}

// ErrNotSupported is returned by providers for operations they do not implement.
var ErrNotSupported = errors.New("auth: operation not supported by this provider")
```

### 1b. Rust AuthN Traits (git-service layer)

The git-service needs two AuthN surfaces: validating gRPC callers (HMAC interceptor) and authenticating Git smart-HTTP requests.

```rust
// File: gitstore-git-service/src/auth/mod.rs

use std::sync::Arc;
use tonic::{Request, Status};
use axum::{extract::Request as AxumRequest, response::Response};

/// Serializable principal forwarded from the API via gRPC metadata or HTTP header.
#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
pub struct Principal {
    pub sub: String,
    pub iss: String,
    pub roles: Vec<String>,
    pub auth_method: String,
}

impl Principal {
    pub fn is_admin(&self) -> bool {
        self.roles.iter().any(|r| r == "admin")
    }
    pub fn anonymous() -> Self {
        Principal {
            sub: "anon".into(),
            iss: "gitstore".into(),
            roles: vec![],
            auth_method: "none".into(),
        }
    }
}

/// Validates gRPC inter-service callers.
/// Implemented as a Tonic interceptor (synchronous fn, not async).
/// Compatible with tonic::service::Interceptor in Tonic 0.14.
pub trait GrpcAuthN: Send + Sync + 'static {
    /// Returns Ok(req) with Principal injected into extensions, or Err(Status::unauthenticated).
    fn check(&self, req: Request<()>) -> Result<Request<()>, Status>;
}

/// Authenticates Git smart-HTTP (Basic Auth) requests.
/// Used as an Axum extractor / middleware layer.
#[async_trait::async_trait]
pub trait HttpAuthN: Send + Sync + 'static {
    /// Returns Ok(principal) on success or an Axum Response (401/403) on failure.
    async fn authenticate(
        &self,
        req: &AxumRequest,
    ) -> Result<Principal, Response>;
}

/// Shared HMAC inter-service validator — phase-1 GrpcAuthN implementation.
pub struct HmacGrpcAuthN {
    secret: Arc<str>,
}

impl HmacGrpcAuthN {
    pub fn new(secret: impl Into<Arc<str>>) -> Self {
        HmacGrpcAuthN { secret: secret.into() }
    }
}

impl GrpcAuthN for HmacGrpcAuthN {
    fn check(&self, req: Request<()>) -> Result<Request<()>, Status> {
        let token = req
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "))
            .ok_or_else(|| Status::unauthenticated("missing authorization header"))?;
        if token != self.secret.as_ref() {
            return Err(Status::unauthenticated("invalid inter-service token"));
        }
        Ok(req)
    }
}
```

### 1c. Go ProviderRegistry and ChainedAuthN

```go
// File: gitstore-api/internal/auth/registry.go
package auth

import (
    "context"
    "fmt"
    "sync"
)

// ProviderRegistry holds the active provider for each plane.
type ProviderRegistry struct {
    mu         sync.RWMutex
    authnChain *ChainedAuthN
    authz      AuthZProvider
    userdir    UserDirProvider
}

func NewProviderRegistry(chain *ChainedAuthN, authz AuthZProvider, userdir UserDirProvider) *ProviderRegistry {
    return &ProviderRegistry{authnChain: chain, authz: authz, userdir: userdir}
}

func (r *ProviderRegistry) AuthN() *ChainedAuthN     { r.mu.RLock(); defer r.mu.RUnlock(); return r.authnChain }
func (r *ProviderRegistry) AuthZ() AuthZProvider     { r.mu.RLock(); defer r.mu.RUnlock(); return r.authz }
func (r *ProviderRegistry) UserDir() UserDirProvider { r.mu.RLock(); defer r.mu.RUnlock(); return r.userdir }

// ChainedAuthN tries each provider in order; first Allow wins.
// An explicit Deny from any provider short-circuits the chain immediately.
// A nil Principal with OutcomeChallenge means "not my token, continue".
type ChainedAuthN struct {
    providers []AuthNProvider
}

func NewChainedAuthN(providers ...AuthNProvider) *ChainedAuthN {
    return &ChainedAuthN{providers: providers}
}

func (c *ChainedAuthN) Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error) {
    for _, p := range c.providers {
        principal, decision, err := p.Authenticate(ctx, req)
        if err != nil {
            return nil, Deny(p.Name(), fmt.Sprintf("provider error: %v", err)), err
        }
        switch decision.Outcome {
        case OutcomeAllow:
            return principal, decision, nil // ← short-circuit: first success wins
        case OutcomeDeny:
            return nil, decision, nil       // ← short-circuit: explicit deny stops chain
        case OutcomeChallenge:
            continue                        // ← not my token, try next
        }
    }
    // All providers returned Challenge — credentials were present but no provider accepted
    // them. This is an authentication failure, not an anonymous request.
    return nil, Deny("chain", "credentials present but no provider accepted them"), nil
}

// RevokeSession delegates to the first provider that supports it.
func (c *ChainedAuthN) RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error {
    for _, p := range c.providers {
        err := p.RevokeSession(ctx, jti, expiresAt)
        if err == nil {
            return nil
        }
        if err != ErrNotSupported {
            return err
        }
    }
    return ErrNotSupported
}
```

---

## 2. Provider Implementations (Phase 1 Scope)

### 2a. static-admin (AuthN)

```go
// File: gitstore-api/internal/auth/provider/staticadmin/provider.go
package staticadmin

// Config keys (Viper paths → env vars):
//   auth.admin.username       → GITSTORE_AUTH__ADMIN__USERNAME       (default "admin")
//   auth.admin.password_hash  → GITSTORE_AUTH__ADMIN__PASSWORD_HASH  (required)
//
// These are the existing env vars — zero config migration required.

type StaticAdminProvider struct {
    username     string
    passwordHash string    // bcrypt hash
    blacklist    *sessionBlacklist
}

func New(cfg *viper.Viper, logger *zap.Logger) (*StaticAdminProvider, error) {
    username := cfg.GetString("auth.admin.username")
    hash := cfg.GetString("auth.admin.password_hash")
    if hash == "" {
        return nil, errors.New("staticadmin: GITSTORE_AUTH__ADMIN__PASSWORD_HASH is required")
    }
    return &StaticAdminProvider{
        username:     username,
        passwordHash: hash,
        blacklist:    newSessionBlacklist(),
    }, nil
}

func (p *StaticAdminProvider) Name() string { return "static-admin" }

func (p *StaticAdminProvider) Capabilities() auth.Capability {
    return auth.CapAuthenticate | auth.CapIssueSession | auth.CapIntrospect
}

func (p *StaticAdminProvider) Authenticate(ctx context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
    // 1. Extract Authorization: Bearer <jwt>
    // 2. Parse the JWT (golang-jwt/v5, HS256, using GITSTORE_AUTH__JWT__SECRET)
    // 3. If parse fails → OutcomeChallenge (not my token, try next provider)
    // 4. Validate issuer == cfg.GetString("auth.jwt.issuer")
    // 5. Check blacklist by jti claim
    // 6. If blacklisted → OutcomeDeny("token revoked")
    // 7. Build Principal{Subject: claims.sub, Issuer: issuer, Roles: ["admin"], AuthMethod: "static-admin"}
    // 8. Return Allow

    // Also handles Basic Auth for Git smart-HTTP:
    // 1. Check for Authorization: Basic header
    // 2. Decode credentials
    // 3. Compare username, bcrypt.CompareHashAndPassword(hash, password)
    // 4. On match → build Principal with Roles: ["admin"] and issue a short-lived JWT
    // 5. Return Allow
    panic("not implemented")
}

func (p *StaticAdminProvider) RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error {
    p.blacklist.add(jti, expiresAt)
    return nil
}

func (p *StaticAdminProvider) RefreshSession(ctx context.Context, oldToken string) (string, time.Time, error) {
    // Parse old token (ignore expiry), validate all other claims, check not blacklisted,
    // revoke old jti, issue new JWT with new jti and exp=now+duration, return.
    panic("not implemented")
}
```

`sessionBlacklist` is an in-memory `sync.Map` keyed by `jti → expiresAt`. A background goroutine prunes expired entries every 5 minutes. This is adequate for single-instance deployment; a Redis or ScyllaDB backend replaces it in production once multi-instance deployment is required.

### 2b. oidc-jwt (AuthN)

> **New dependency required:** `github.com/coreos/go-oidc/v3` must be added to `go.mod`. Justification: golang-jwt/v5 already in go.mod validates HS256 but cannot perform JWKS-backed RSA/EC signature verification or OIDC Discovery. No existing dependency provides equivalent functionality.

```go
// File: gitstore-api/internal/auth/provider/oidcjwt/provider.go
package oidcjwt

// Config keys (Viper paths → env vars):
//   auth.oidc.issuer_url   → GITSTORE_AUTH__OIDC__ISSUER_URL   (required)
//   auth.oidc.client_id    → GITSTORE_AUTH__OIDC__CLIENT_ID    (required)
//   auth.oidc.audience     → GITSTORE_AUTH__OIDC__AUDIENCE     (optional, defaults to client_id)
//   auth.oidc.clock_skew   → GITSTORE_AUTH__OIDC__CLOCK_SKEW   (default "2m")

type OIDCJWTProvider struct {
    verifier    *oidc.IDTokenVerifier
    issuerURL   string
    clientID    string
    provider    *oidc.Provider
    logger      *zap.Logger
}

func New(ctx context.Context, cfg *viper.Viper, logger *zap.Logger) (*OIDCJWTProvider, error) {
    issuerURL := cfg.GetString("auth.oidc.issuer_url")
    clientID  := cfg.GetString("auth.oidc.client_id")
    // go-oidc performs OIDC Discovery (/.well-known/openid-configuration) here:
    provider, err := oidc.NewProvider(ctx, issuerURL)
    // verifier enforces iss + aud; clock skew leeway is handled by go-oidc internally
    // through oidc.Config.Now which can be overridden in tests
    verifier := provider.Verifier(&oidc.Config{
        ClientID:        clientID,
        SkipExpiryCheck: false, // RFC 7519 §4.1.4 MUST validate exp
        // go-oidc applies a 1-minute built-in leeway; set GITSTORE_AUTH__OIDC__CLOCK_SKEW for override
    })
    return &OIDCJWTProvider{verifier: verifier, issuerURL: issuerURL, clientID: clientID,
        provider: provider, logger: logger}, nil
}

func (p *OIDCJWTProvider) Name() string { return "oidc-jwt" }

func (p *OIDCJWTProvider) Capabilities() auth.Capability {
    return auth.CapAuthenticate | auth.CapIntrospect | auth.CapGroupResolution
}

func (p *OIDCJWTProvider) Authenticate(ctx context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
    // 1. Extract bearer token from Authorization header
    // 2. Check JWT issuer claim WITHOUT verifying signature (just parse, no verify)
    //    If issuer != p.issuerURL → return OutcomeChallenge (not my token)
    //    This prevents passing gitstore-issued HS256 tokens to the JWKS verifier.
    // 3. verifier.Verify(ctx, rawToken):
    //    - On ErrKeyNotFound: call provider.RemoteKeySet forced refresh, retry once
    //    - On other errors: OutcomeChallenge (malformed, not an OIDC token)
    // 4. Extract standard claims:
    //    - sub, iss, exp, groups (custom claim), roles (custom claim)
    // 5. Build Principal{Subject: sub, Issuer: iss, Groups: groups, Roles: roles,
    //                     AuthMethod: "oidc-jwt", ExpiresAt: exp}
    // 6. Return Allow
    panic("not implemented")
}

func (p *OIDCJWTProvider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
    // OIDC tokens are stateless; revocation requires the IdP's revocation endpoint.
    // Phase 1: return ErrNotSupported. Phase 2: call RFC 7009 revocation endpoint.
    return auth.ErrNotSupported
}

func (p *OIDCJWTProvider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
    return "", time.Time{}, auth.ErrNotSupported
}
```

### 2c. allow-all (AuthZ)

```go
// File: gitstore-api/internal/auth/provider/allowall/provider.go
package allowall

// Config keys: none. Activated when GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all.

type AllowAllProvider struct{ warned bool }

func New(logger *zap.Logger) *AllowAllProvider {
    logger.Warn("SECURITY: authz provider is allow-all — ALL authorization checks are disabled. DO NOT use in production.")
    return &AllowAllProvider{warned: true}
}

func (p *AllowAllProvider) Name() string { return "allow-all" }

func (p *AllowAllProvider) Authorize(_ context.Context, _ *auth.Principal, action string, _ auth.ResourceContext) (auth.Decision, error) {
    return auth.Allow("allow-all", "allow-all provider permits everything"), nil
}
```

### 2d. rbac-local (AuthZ)

```go
// File: gitstore-api/internal/auth/provider/rbaclocal/provider.go
package rbaclocal

// Config keys (Viper paths → env vars):
//   auth.rbac.policy_file → GITSTORE_AUTH__RBAC__POLICY_FILE (default "policy.yaml")

type RBACLocalProvider struct {
    mu     sync.RWMutex
    policy *Policy
    path   string
    logger *zap.Logger
}

func New(cfg *viper.Viper, logger *zap.Logger) (*RBACLocalProvider, error) {
    path := cfg.GetString("auth.rbac.policy_file")
    if path == "" { path = "policy.yaml" }
    p := &RBACLocalProvider{path: path, logger: logger}
    return p, p.reload()
}

func (p *RBACLocalProvider) Authorize(ctx context.Context, principal *auth.Principal, action string, res auth.ResourceContext) (auth.Decision, error) {
    p.mu.RLock()
    policy := p.policy
    p.mu.RUnlock()

    // 1. For each role in principal.Roles, look up role in policy.Roles
    // 2. Check if action is in role.Allow
    // 3. Check if action matches any glob in role.AllowGlob
    // 4. Check role.DenyActions — explicit deny overrides allow
    // 5. Check policy.DefaultDeny
    // If any allow matches and no deny matches → OutcomeAllow
    // Otherwise → OutcomeDeny
    panic("not implemented")
}
```

**Policy YAML schema:**

```yaml
# policy.yaml — rbac-local policy file
# Version must be "v1".
version: v1

# roles maps role name → permissions
roles:
  admin:
    allow:
      - "*"                  # wildcard: all actions
    deny: []

  namespace-owner:
    allow:
      - namespace.read
      - namespace.update
      - namespace.delete.own  # only own namespaces
      - repository.read
      - repository.write
      - repository.create
      - repository.delete.own
    deny:
      - namespace.delete.any  # cannot delete other owners' namespaces

  developer:
    allow:
      - namespace.read
      - repository.read
      - repository.write
    deny: []

  anonymous:
    allow:
      - namespace.read
      - repository.read
    deny:
      - repository.write
      - namespace.create
      - namespace.delete.any

# default_deny applies when no role rule matches.
default_deny: true

# role_bindings maps subject → list of roles (used when UserDir is none)
role_bindings:
  "admin":
    - admin
```

### 2e. anonymous (AuthN)

```go
// File: gitstore-api/internal/auth/provider/anonymous/provider.go
package anonymous

// AnonymousProvider is always the last provider in the chain.
// It claims the request only when no credential signals are present
// (no Authorization header, no ForwardedSubject).
// If credentials were present but earlier providers all returned Challenge,
// this provider also returns Challenge — causing the chain to fall through
// to a Deny.

type AnonymousProvider struct{}

func New() *AnonymousProvider { return &AnonymousProvider{} }

func (p *AnonymousProvider) Name() string { return "anonymous" }

func (p *AnonymousProvider) Capabilities() auth.Capability { return auth.CapAuthenticate }

func (p *AnonymousProvider) Authenticate(_ context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
    hasCredentials := req.Header.Get("Authorization") != "" || req.ForwardedSubject != ""
    if hasCredentials {
        // Credentials were presented but no prior provider accepted them.
        // Return Challenge so the chain fallthrough becomes a Deny.
        return nil, auth.Challenge("anonymous", "credentials present but unrecognized"), nil
    }
    return auth.Anonymous(), auth.Allow("anonymous", "no credentials presented"), nil
}

func (p *AnonymousProvider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
    return auth.ErrNotSupported
}

func (p *AnonymousProvider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
    return "", time.Time{}, auth.ErrNotSupported
}
```

The `anonymous` provider must be the **last entry** in `GITSTORE_AUTH__AUTHN__CHAIN`.
Placing it earlier would shadow all subsequent providers.

---

### 2f. none (UserDir)

```go
// File: gitstore-api/internal/auth/provider/userdirnone/provider.go
package userdirnone

type NoneProvider struct{}

func New() *NoneProvider { return &NoneProvider{} }

func (p *NoneProvider) Name() string { return "none" }

func (p *NoneProvider) GetBySubject(_ context.Context, _ string) (*auth.UserProfile, error) {
    return nil, auth.ErrNotSupported
}
func (p *NoneProvider) ListGroups(_ context.Context, _ string) ([]string, error) {
    return nil, auth.ErrNotSupported
}
func (p *NoneProvider) SearchUsers(_ context.Context, _ string, _ int) ([]*auth.UserProfile, error) {
    return nil, auth.ErrNotSupported
}
func (p *NoneProvider) UpsertProfile(_ context.Context, _ *auth.UserProfile) error {
    return auth.ErrNotSupported
}
func (p *NoneProvider) Deactivate(_ context.Context, _ string) error {
    return auth.ErrNotSupported
}
```

---

## 3. Migration Path

### 3a. Replacing middleware.User with Principal

The current `middleware.User` struct and the new `auth.Principal` differ as follows:

```text
middleware.User (current)         auth.Principal (new)
─────────────────────────────     ─────────────────────────────────────
Username  string                → Subject    string    (json:"sub")
IsAdmin   bool                  → (removed)            derived via Principal.IsAdmin() method
                                  Issuer     string    (json:"iss")          NEW
                                  Tenant     string    (json:"tenant")       NEW optional
                                  Namespace  string    (json:"namespace")    NEW optional
                                  Groups     []string  (json:"groups")       NEW
                                  Roles      []string  (json:"roles")        NEW
                                  Scopes     []string  (json:"scopes")       NEW
                                  Claims     map[string]any                  NEW
                                  AuthMethod string    (json:"auth_method")  NEW
                                  ExpiresAt  time.Time (json:"exp")          NEW
```

The **context key** in `middleware/auth.go` changes from `userContextKey` (type `struct{}`) to `principalContextKey` (new package-private key in `internal/auth/context.go`). The `GetUserFromContext(ctx) *middleware.User` function is replaced by `auth.PrincipalFromContext(ctx) *auth.Principal`. A compatibility shim `middleware.GetUserFromContext` existed during the transition and has been deleted (T028) — all callers have been migrated to use `auth.PrincipalFromContext` directly.

### 3b. Migrating the Two Live isAdmin Checks

Both checks in `service.go` follow the same three-step pattern without breaking existing behaviour:

**createNamespace (tier validation):**
```text
Before:
  if tier == ORGANIZATION && !user.IsAdmin {
      return ErrForbidden("only admins can create ENTERPRISE namespaces")
  }

After:
  decision, err := authz.Authorize(ctx, principal, "namespace.create.organisation",
      auth.ResourceContext{Kind: "namespace", Attrs: map[string]any{"tier": tier}})
  if decision.Outcome != auth.OutcomeAllow {
      return ErrForbidden(decision.Reason)
  }
```
The `rbac-local` policy grants `namespace.create.organization` only to the `admin` role; `allow-all` permits it unconditionally; behaviour is identical to the current check in both cases.

**deleteNamespace (owner-or-admin):**
```text
Before:
  if ns.OwnerUsername != user.Username && !user.IsAdmin {
      return ErrForbidden("only owner or admin can delete namespace")
  }

After:
  action := "namespace.delete.own"
  if ns.OwnerUsername != principal.Subject {
      action = "namespace.delete.any"
  }
  decision, err := authz.Authorize(ctx, principal, action,
      auth.ResourceContext{Kind: "namespace", Name: ns.Name, OwnerSub: ns.OwnerUsername})
  if decision.Outcome != auth.OutcomeAllow {
      return ErrForbidden(decision.Reason)
  }
```
The `rbac-local` policy grants `namespace.delete.any` only to `admin`, and `namespace.delete.own` to `namespace-owner` and `admin`; semantically identical to the current check.

### 3c. Fixing callerUsernameOrAnon

```text
Before (helpers.go ~line 22):
  func callerUsernameOrAnon(ctx context.Context) string {
      user := middleware.GetUserFromContext(ctx)
      if user == nil {
          return "anon"
      }
      return user.Username
  }

After:
  func callerUsernameOrAnon(ctx context.Context) string {
      p := auth.PrincipalFromContext(ctx)
      if p == nil {
          return "anon"
      }
      return p.Subject   // "anon" for the Anonymous() principal, real subject otherwise
  }
```

### 3d. Implementing Logout and RefreshToken

Both mutations are provider-agnostic by delegating to `ChainedAuthN`:

**Logout:**
```text
mutation Logout(token: String!): Boolean
  1. Parse the JWT (no signature verify, just claims) to extract jti + exp
  2. Call registry.AuthN().RevokeSession(ctx, jti, expiresAt)
     - static-admin provider: adds jti to in-memory blacklist
     - oidc-jwt provider: returns ErrNotSupported (OIDC tokens expire naturally;
       phase 2 adds RFC 7009 call to IdP revocation endpoint)
  3. Return true if any provider accepted the revocation; false + log if ErrNotSupported
```

**RefreshToken:**
```text
mutation RefreshToken(token: String!): AuthPayload
  1. Call registry.AuthN().RefreshSession(ctx, oldToken)
     - Chains through providers; first non-ErrNotSupported result wins
     - static-admin: validates old token, blacklists its jti, issues new JWT
     - oidc-jwt: returns ErrNotSupported (use IdP refresh endpoint directly)
  2. Return new token + expiry
```

The **token blacklist** lives inside the `StaticAdminProvider.blacklist` (`sync.Map[jti → expiresAt]`). It is in-process memory, which is correct for single-instance deployment. Multi-instance deployment (phase 3+) requires extracting the blacklist into a Redis or ScyllaDB-backed store behind a `SessionStore` interface — this is an additive change that does not alter the `AuthNProvider` interface.

---

## 4. Git-Service Authentication

### 4a. gRPC Inter-Service Trust — Decision: Shared HMAC Token (Option ii)

**Decision:** Use a shared HMAC bearer token passed as gRPC metadata.

**Justification:** mTLS (option i) requires PKI infrastructure (CA, cert generation, rotation tooling) that does not exist today. Adding it from scratch in phase 1 would block progress on the auth framework itself. Network-only trust (option iii) is acceptable for development but unacceptable once the Git smart-HTTP layer carries real user identities. A shared HMAC secret is operationally simple, requires one new config key on each side, provides cryptographic identity of the caller (not just network adjacency), and can be rotated without cert infrastructure. mTLS is the phase-2 upgrade once a cert management solution is chosen.

**Rust Tonic interceptor:**

```rust
// File: gitstore-git-service/src/auth/interceptor.rs

use tonic::{service::Interceptor, Request, Status};

pub struct HmacInterceptor {
    secret: String,
}

impl HmacInterceptor {
    pub fn new(secret: impl Into<String>) -> Self {
        HmacInterceptor { secret: secret.into() }
    }
}

impl Interceptor for HmacInterceptor {
    // Tonic 0.14: interceptors are synchronous (fn call, not async fn).
    fn call(&mut self, req: Request<()>) -> Result<Request<()>, Status> {
        let token = req
            .metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "))
            .ok_or_else(|| Status::unauthenticated("missing inter-service token"))?;

        if token != self.secret.as_str() {
            return Err(Status::unauthenticated("invalid inter-service token"));
        }
        Ok(req)
    }
}

// Wired at server startup in main.rs:
// let interceptor = HmacInterceptor::new(config.auth.grpc.hmac_secret.clone());
// Server::builder()
//     .add_service(GitServiceServer::with_interceptor(svc, interceptor))
//     .serve(addr)
//     .await?;
```

**Go side** — pass the HMAC token as gRPC `PerRPCCredentials`:

```go
// File: gitstore-api/internal/gitclient/auth.go
type hmacCreds struct{ token string }

func (h hmacCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
    return map[string]string{"authorization": "Bearer " + h.token}, nil
}
func (h hmacCreds) RequireTransportSecurity() bool { return false }

// When building the gRPC connection:
// grpc.Dial(addr, grpc.WithPerRPCCredentials(hmacCreds{token: cfg.GetString("auth.grpc.hmac_secret")}))
```

### 4b. Git Smart-HTTP Authentication

**Design overview:** The existing `githttp.Handler` in Go is wrapped by provider-chain authentication before handing off to the Git backend. The API remains the AuthN/AuthZ decision point for Git Smart HTTP: it authenticates the caller, authorizes the requested repository action, resolves `(namespace, repository)` to the stable `repository_id`, and resolves any effective push-time policy before opening the gRPC call to gitstore-git-service.

The git-service validates the API as the inter-service caller via the HMAC metadata described in §4a. It does not run the pluggable AuthZ provider itself, but it is not a purely dumb storage layer: receive-pack must enforce the effective git-layer policy supplied by the API for that operation.

**Go layer — githttp/handler.go changes:**

```go
// AuthenticatedGitHandler wraps the existing githttp.NewMux handler.
// It calls ChainedAuthN and either serves the request or returns 401.
func AuthenticatedGitHandler(chain *auth.ChainedAuthN, inner http.Handler, logger *zap.Logger) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        req := auth.AuthRequest{
            Header:     r.Header,
            RemoteAddr: r.RemoteAddr,
        }
        principal, decision, err := chain.Authenticate(r.Context(), req)
        if err != nil || decision.Outcome == auth.OutcomeDeny {
            w.Header().Set("WWW-Authenticate", `Basic realm="GitStore"`)
            // Return 401 (not 403) so that Git credential helpers prompt for credentials.
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        // Store principal in context for downstream use.
        ctx := auth.ContextWithPrincipal(r.Context(), principal)
        inner.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

**Push-time operation context** — when the Go layer handles `git-receive-pack`, it resolves the effective policy for that repository and sends a typed context on the first `ReceivePackRequest` message. This context is immutable for the lifetime of the stream. Later chunks must not carry a different context.

The effective policy is resolved from trusted control-plane state, such as the Namespace and Repository resources defined by #170 and #249. It must not be read from untrusted content in the incoming push and applied to that same push. If repository-authored configuration changes in a push, those changes apply only after successful validation/admission and status reconciliation.

Operator-level service configuration remains the safety boundary. Repository policy may tighten limits, but it cannot relax global ceilings such as maximum pack size, maximum file size, required validation, or required admission unless an administrator-controlled policy explicitly allows it.

```proto
message ReceivePackRequest {
  string repository_id = 15;
  repeated RefCommand ref_commands = 1;
  bytes pack_data = 2;
  bool is_last = 3;

  // Set only on the first stream message.
  PushContext push_context = 16;
}

message PushContext {
  string namespace = 1;
  string repository_name = 2;
  string repository_id = 3;
  string config_resource_version = 4;

  AuthContext actor = 10;
  PushPolicy policy = 11;
}

message AuthContext {
  string subject = 1;
  string issuer = 2;
  string auth_method = 3;
  repeated string roles = 4;
  repeated string groups = 5;
  repeated string scopes = 6;
}

message PushPolicy {
  uint64 max_pack_size_bytes = 1;
  uint64 max_file_size_bytes = 2;
  ReceiveHookPolicy hooks = 3;
  PhasePolicy schema_validation = 4;
  AdmissionPolicy admission_control = 5;
}
```

`AuthContext` is a sanitized principal snapshot, not a bearer token or raw credential. If a future transport can resolve the end-user principal idiomatically from authenticated peer state, the server can construct the same `AuthContext` internally. With the current API → git-service HMAC model, the git-service can authenticate only the API caller from metadata, so the end-user actor must be supplied explicitly.

**Rust git-service** — the receive-pack implementation validates that the first stream message contains a `PushContext` whose `repository_id` matches the first chunk repository ID. It applies `PushPolicy` while staging the pack, running validation, and invoking admission. The in-process hook pipeline receives a typed `HookContext` derived from `PushContext`; it must not read authentication or policy from process environment variables.

```rust
pub struct HookContext {
    pub repository_id: String,
    pub namespace: String,
    pub repository_name: String,
    pub config_resource_version: String,
    pub actor: AuthContext,
    pub policy: PushPolicy,
}
```

---

## 5. Config Design

### 5a. Complete Viper Config Schema

```text
Key path (Viper)                         Env var                                    Type     Default
─────────────────────────────────────────────────────────────────────────────────────────────────────────
auth.authn.chain                         GITSTORE_AUTH__AUTHN__CHAIN                []string ["static-admin","anonymous"]
auth.authz.provider                      GITSTORE_AUTH__AUTHZ__PROVIDER             string   "allow-all"
auth.userdir.provider                    GITSTORE_AUTH__USERDIR__PROVIDER           string   "none"

# Existing keys — unchanged
auth.admin.username                      GITSTORE_AUTH__ADMIN__USERNAME             string   "admin"
auth.admin.password_hash                 GITSTORE_AUTH__ADMIN__PASSWORD_HASH        string   (required)
auth.jwt.secret                          GITSTORE_AUTH__JWT__SECRET                 string   (required)
auth.jwt.duration                        GITSTORE_AUTH__JWT__DURATION               duration "24h"
auth.jwt.issuer                          GITSTORE_AUTH__JWT__ISSUER                 string   "gitstore"
auth.jwt.refresh_grace                   GITSTORE_AUTH__JWT__REFRESH_GRACE          duration "60s"

# Future OIDC JWT provider (Phase 6)
auth.oidc.issuer_url                     GITSTORE_AUTH__OIDC__ISSUER_URL            string   ""
auth.oidc.client_id                      GITSTORE_AUTH__OIDC__CLIENT_ID             string   ""
auth.oidc.audience                       GITSTORE_AUTH__OIDC__AUDIENCE              string   "" (defaults to client_id)
auth.oidc.clock_skew                     GITSTORE_AUTH__OIDC__CLOCK_SKEW            duration "2m"

# RBAC local provider
auth.rbac.policy_file                    GITSTORE_AUTH__RBAC__POLICY_FILE           string   "policy.yaml"

# gRPC inter-service HMAC
auth.grpc.hmac_secret                    GITSTORE_AUTH__GRPC__HMAC_SECRET           string   (required unless grpc.disabled)
```

**gitstore-api/.env — local-fast profile:**
```text
# local-fast: no external services required
GITSTORE_AUTH__ADMIN__USERNAME=admin
GITSTORE_AUTH__ADMIN__PASSWORD_HASH=$2a$10$...
GITSTORE_AUTH__JWT__SECRET=dev-secret-change-me
GITSTORE_AUTH__JWT__ISSUER=gitstore
GITSTORE_AUTH__AUTHN__CHAIN=static-admin,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all
GITSTORE_AUTH__USERDIR__PROVIDER=none
GITSTORE_AUTH__GRPC__HMAC_SECRET=dev-grpc-secret
```

**gitstore-api/.env — future local-secure profile with OIDC (Phase 6):**
```text
GITSTORE_AUTH__ADMIN__USERNAME=admin
GITSTORE_AUTH__ADMIN__PASSWORD_HASH=$2a$10$...
GITSTORE_AUTH__JWT__SECRET=local-secure-secret
GITSTORE_AUTH__JWT__ISSUER=gitstore
GITSTORE_AUTH__AUTHN__CHAIN=oidc-jwt,static-admin,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local
GITSTORE_AUTH__USERDIR__PROVIDER=none
GITSTORE_AUTH__RBAC__POLICY_FILE=/etc/gitstore/policy.yaml
GITSTORE_AUTH__OIDC__ISSUER_URL=http://localhost:8080/realms/gitstore
GITSTORE_AUTH__OIDC__CLIENT_ID=gitstore-api
GITSTORE_AUTH__OIDC__CLOCK_SKEW=2m
GITSTORE_AUTH__GRPC__HMAC_SECRET=local-grpc-hmac-secret
```

**gitstore-api/.env — future production profile with OIDC/OPA (Phase 6+):**
```text
GITSTORE_AUTH__ADMIN__USERNAME=admin
GITSTORE_AUTH__ADMIN__PASSWORD_HASH=$2a$10$...
GITSTORE_AUTH__JWT__SECRET=${JWT_SECRET}
GITSTORE_AUTH__JWT__ISSUER=gitstore
GITSTORE_AUTH__JWT__DURATION=1h
GITSTORE_AUTH__JWT__REFRESH_GRACE=60s
GITSTORE_AUTH__AUTHN__CHAIN=oidc-jwt,static-admin,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=opa
GITSTORE_AUTH__USERDIR__PROVIDER=none
GITSTORE_AUTH__OIDC__ISSUER_URL=${OIDC_ISSUER_URL}
GITSTORE_AUTH__OIDC__CLIENT_ID=${OIDC_CLIENT_ID}
GITSTORE_AUTH__OIDC__AUDIENCE=${OIDC_AUDIENCE}
GITSTORE_AUTH__OIDC__CLOCK_SKEW=2m
GITSTORE_AUTH__RBAC__POLICY_FILE=/etc/gitstore/policy.yaml
GITSTORE_AUTH__GRPC__HMAC_SECRET=${GRPC_HMAC_SECRET}
```

### 5b. Rust config-rs additions

```rust
// File: gitstore-git-service/src/config.rs — additions to existing AppConfig struct

#[derive(Debug, Deserialize, Clone)]
pub struct AuthConfig {
    pub grpc: GrpcAuthConfig,
    pub http: HttpAuthConfig,
}

#[derive(Debug, Deserialize, Clone)]
pub struct GrpcAuthConfig {
    /// Shared HMAC secret validated by the Tonic interceptor.
    /// Env: GITSTORE_AUTH__GRPC__HMAC_SECRET
    pub hmac_secret: String,
}

#[derive(Debug, Deserialize, Clone)]
pub struct HttpAuthConfig {
    /// Mode for smart-HTTP authentication.
    /// "none"   — accept all requests (local-fast only)
    /// "basic"  — validate credentials by calling gitstore-api auth endpoint
    /// "header" — trust X-Gitstore-Principal-Sub header (set by Go auth proxy)
    /// Env: GITSTORE_AUTH__HTTP__MODE  (default: "header")
    #[serde(default = "default_http_auth_mode")]
    pub mode: String,
}

fn default_http_auth_mode() -> String { "header".into() }

// In AppConfig, add:
// pub auth: AuthConfig,

// Config source additions (in config.rs builder):
// .add_source(
//     config::Environment::with_prefix("GITSTORE")
//         .prefix_separator("_")
//         .separator("__")
//         .try_parsing(true),
// )
// — already present; new auth keys are picked up automatically.
```

---

## 6. Resolved Open Decisions

### Decision 1: In-process providers only first, or include external gRPC/webhook from phase 1?

**Decision: In-process only in phase 1.**

**Trade-off weighed:** External gRPC/webhook providers (e.g., OPA sidecar, OpenFGA server) offer greater policy flexibility and are production-grade. However, they introduce a mandatory external service dependency that violates the `local-fast` profile constraint (must run with zero external services). Implementing the HTTP client wrappers and circuit-breaker logic for external providers before the interface contracts are stable adds risk to an already broad phase-1 scope.

**Constraint on future providers:** Every external provider (OPA, OpenFGA, webhook) must implement the same `AuthZProvider` or `AuthNProvider` interface defined in §1a. The only addition is an `IsRemote() bool` method on the interface (for startup dependency health-checks) — this is additive and backward-compatible with in-process providers that return `false`.

### Decision 2: Policy decision logs — datastore or log stream only?

**Decision: Log stream only in phase 1.**

**Trade-off weighed:** Persisting every authz decision in ScyllaDB enables audit queries, compliance reporting, and anomaly detection dashboards. However, it requires a new datastore table schema migration, adds a write on every authorized request (hot path), and requires a decision about retention policy before any data is written. Structured log output via `zap` to stdout is immediately parseable by any log aggregation stack (Loki, Elasticsearch) without schema migrations.

**Constraint on future providers:** Each `AuthZProvider.Authorize` call must emit a structured log line with at minimum: `provider`, `subject`, `action`, `resource_kind`, `resource_name`, `outcome`, `reason`, `request_id`, `latency_ms`. A `DecisionLogger` middleware wrapping `AuthZProvider` provides this uniformly so individual providers do not need to log themselves. A phase-3 `AuditStore` interface can optionally persist these log records without changing the `AuthZProvider` interface.

### Decision 3: Default production AuthZ engine — OpenFGA or OPA?

**Decision: OPA (Open Policy Agent) for production.**

**Trade-off weighed:** OpenFGA is purpose-built for Relationship-Based Access Control (ReBAC) and excels at "does user X have permission Y on object Z via a chain of relationships?" — exactly the model needed if GitStore later implements org membership hierarchies, inherited repository permissions, or team-scoped access. OPA uses Rego, a general-purpose policy language, and its policy file model maps directly onto the `rbac-local` YAML schema used in phase 1, making the migration from `rbac-local` → OPA a matter of translating the YAML policy into Rego without changing the `AuthZProvider` interface or the action name vocabulary. OpenFGA requires a running relational store (PostgreSQL/MySQL) and a tuple-loading pipeline from GitStore's data — infrastructure that does not exist today.

**Constraint on future providers:** The action name vocabulary (`namespace.delete.any`, `repository.write`, etc.) and `ResourceContext` struct are the contract between resolvers and the AuthZ provider. OPA Rego policies must receive these as structured input; the OPA provider must serialize `(principal, action, ResourceContext)` into the OPA input document before calling the policy engine. If OpenFGA is added in phase 4, its tuple schema must be derived from the same `ResourceContext` fields — no action name changes.

---

## 7. Rollout Phases

### Phase 1 — Interface foundation and static-admin migration ✅ COMPLETE (031)
**Milestone:** `auth-framework-v1`
**Deliverable:** `internal/auth/types.go`, `ProviderRegistry`, `ChainedAuthN`, `StaticAdminProvider`, `AllowAllProvider`, `RBACLocalProvider`, `AnonymousProvider`, `NoneUserDirProvider`, `DecisionLogger`. Replace `middleware.User` context with `Principal`. Wire `allow-all` as default authz; default authn chain is `["static-admin","anonymous"]`. SIGHUP triggers `rbac-local` policy reload. `GetUserFromContext` shim deleted; all callers use `auth.PrincipalFromContext` directly.
**Affected packages:** `gitstore-api/internal/auth/`, `gitstore-api/internal/middleware/`, `gitstore-api/internal/graph/resolver/`
**Test strategy:** Unit tests for each provider; integration test that the static-admin path works end-to-end with existing `GITSTORE_AUTH__ADMIN__*` env vars unchanged; test that `allow-all` emits a zap warning on startup.
**Rollback trigger:** Any existing integration test (login, createNamespace, deleteNamespace) fails.

### Phase 2 — Live isAdmin checks migrated to AuthZ + callerUsernameOrAnon fix ✅ COMPLETE (031)
**Milestone:** `auth-framework-v1`
**Deliverable:** `service.go` isAdmin checks replaced with `authz.Authorize` calls (§3b); `callerUsernameOrAnon` uses `Principal.Subject` (§3c); compatibility shim removed.
**Affected packages:** `gitstore-api/internal/graph/resolver/service.go`, `gitstore-api/internal/graph/resolver/helpers.go`
**Test strategy:** Service-layer unit tests for createNamespace (ENTERPRISE tier rejection) and deleteNamespace (non-owner rejection) with both `allow-all` and `rbac-local` providers. Golden-path test: admin can create ENTERPRISE namespace; non-admin cannot.
**Rollback trigger:** Either of the two existing auth checks behaves differently from before.

### Phase 3 — Logout and RefreshToken mutations implemented ✅ COMPLETE (032)
**Milestone:** `auth-framework-v1`
**Deliverable:** `Logout` mutation calls `ChainedAuthN.RevokeSession`; `RefreshToken` mutation calls `ChainedAuthN.RefreshSession`; `StaticAdminProvider` blacklist is functional. `Login` resolver migrated away from legacy `authMiddleware` stubs — `user.isAdmin` and `user.username` now derived from `Principal`. `IssueSession` added to `AuthNProvider` interface. `Principal.TokenID` carries JWT `jti`. `ContextWithRawToken`/`RawTokenFromContext` store the raw Bearer string for refresh. `GITSTORE_AUTH__JWT__REFRESH_GRACE` (default `60s`) bounds the refresh window.
**Affected packages:** `gitstore-api/internal/auth/types.go`, `gitstore-api/internal/auth/context.go`, `gitstore-api/internal/auth/registry.go`, `gitstore-api/internal/auth/provider/staticadmin/`, `gitstore-api/internal/auth/provider/anonymous/`, `gitstore-api/internal/middleware/auth.go`, `gitstore-api/internal/graph/resolver/`
**Known limitation:** The in-process session blacklist (`sync.Map[jti → expiresAt]` inside `StaticAdminProvider`) is **lost on server restart**. Any token revoked via `logout` or `refreshToken` becomes valid again after a restart if it has not yet expired. This is acceptable for single-instance deployments. Persistent blacklist storage (Redis or ScyllaDB behind a `SessionStore` interface) is deferred to a future phase.
**Test strategy:** Unit tests for all three mutations (logout, refreshToken, login) in `tests/unit/resolver/auth_resolvers_test.go`; grace-window and TokenID population tests in `tests/unit/auth/staticadmin_test.go`.
**Rollback trigger:** Logout or RefreshToken returns 500 or leaves token valid after revocation; login returns wrong `isAdmin` or `username`.

### Phase 4 — gRPC HMAC inter-service authentication ✅ COMPLETE (033)
**Milestone:** `auth-framework-git-v1`
**Deliverable:** `HmacInterceptor` in Rust git-service; `hmacCreds` on Go gRPC client; `GITSTORE_AUTH__GRPC__HMAC_SECRET` wired on both sides; `cmd/gitctl` binary with `gen-hmac-secret` subcommand.
**Affected packages:** `gitstore-git-service/src/auth/`, `gitstore-api/internal/gitclient/`, `gitstore-api/cmd/gitctl/`, `gitstore-api/internal/config/`
**Test strategy:** Unit tests: `HmacInterceptor` rejects missing/wrong token, accepts correct token and previous token during rotation window; `hmacCreds.GetRequestMetadata` injects `Authorization: Bearer` header; config validation fails on empty secret.
**Rollback trigger:** Any gRPC call from API to git-service fails in CI.

### Phase 5 — Git smart-HTTP authentication
**Milestone:** `auth-framework-git-v1`
**Deliverable:** `AuthenticatedGitHandler` wrapping existing `githttp.Handler`; repository read/write authorization before invoking git-service; effective push policy resolved by API; `PushContext`/`AuthContext` sent on the first `ReceivePackRequest`; git-service enforces `PushPolicy` and passes a typed `HookContext` to the in-process hook pipeline.
**Affected packages:** `gitstore-api/internal/githttp/`, `gitstore-api/internal/gitclient/`, `shared/proto/gitstore/git/v1/`, `gitstore-git-service/src/grpc/`, `gitstore-git-service/src/git/hooks/`
**Test strategy:** Integration test: unauthenticated push to port 5000 returns 401; unauthorized push is rejected before git-service invocation; authenticated push includes `PushContext`; git-service rejects receive-pack streams missing first-chunk context; repository-tightened pack/file limits are enforced; hook/admission logs include the sanitized actor subject from `HookContext`.
**Rollback trigger:** Legitimate push/fetch from an authenticated client fails.

### Phase 6 — OIDC JWT provider
**Milestone:** `auth-framework-v2`
**Deliverable:** `OIDCJWTProvider`; `go-oidc/v3` added to `go.mod`; local-secure profile end-to-end tested.
**Affected packages:** `gitstore-api/internal/auth/provider/oidcjwt/`, `go.mod`
**Test strategy:** Unit test with a mock JWKS server; integration test with a real local IdP (e.g., Dex running in Docker Compose test profile); test key rotation forced-refresh behavior.
**Rollback trigger:** OIDC provider causes 500s on valid tokens, or breaks the static-admin fallback in the chain.

### Phase 7 — OPA production AuthZ provider
**Milestone:** `auth-framework-v3`
**Deliverable:** `OPAProvider` calling the OPA sidecar HTTP API; production `.env` profile wires `GITSTORE_AUTH__AUTHZ__PROVIDER=opa`; Rego policy equivalent to `policy.yaml` bundled.
**Affected packages:** `gitstore-api/internal/auth/provider/opa/`, production deployment config
**Test strategy:** Integration test with OPA running as a Docker Compose sidecar; verify all existing action names resolve correctly; verify deny path on unknown action.
**Rollback trigger:** Any authz decision returns unexpected outcome vs. rbac-local reference run.

---

## 8. Gaps and Risks

### Risk 1: JWT Clock Skew (exp validation)
**Description:** Distributed nodes with unsynchronized clocks can cause valid tokens to be rejected (or expired tokens to be accepted) at the boundary. The research verified that RFC 7519 MAY (not MUST) apply leeway — go-oidc v3 applies 1 minute by default, but the static-admin HS256 path (golang-jwt/v5) uses no leeway by default.
**Mitigation:** Configure `2m` clock skew leeway (`GITSTORE_AUTH__OIDC__CLOCK_SKEW=2m`) for the oidc-jwt provider. For the static-admin provider, pass `jwt.WithLeeway(2*time.Minute)` to the `jwt.ParseWithClaims` call. Enforce NTP synchronization in production deployment runbooks. Monitor token rejection rate as an alert signal for clock drift.

### Risk 2: JWKS Key Rotation Window
**Description:** `RemoteKeySet` (go-oidc v3) caches JWKS and does NOT immediately propagate rotated keys. During the rotation window, tokens signed with the new key are rejected as "key not found."
**Mitigation:** In the `OIDCJWTProvider.Authenticate` method, on any signature verification error matching "key not found" or "verification error," force a JWKS refresh via the provider's `RemoteKeySet` and retry the verification exactly once before returning 401. This closes the rotation window to a single request's latency. Log the forced-refresh event for observability.

### Risk 3: HMAC Secret Rotation (gRPC inter-service)
**Description:** Rotating `GITSTORE_AUTH__GRPC__HMAC_SECRET` requires a coordinated deployment of both API and git-service. A window exists where one service has the new secret and the other has the old, causing all gRPC calls to fail with `Status::unauthenticated`.
**Mitigation:** Implement a two-token validation window in `HmacInterceptor`: accept both `hmac_secret` and `hmac_secret_previous` during rotation. The Go client always sends `hmac_secret`. This allows a rolling deployment without a service outage. Remove `hmac_secret_previous` after both services confirm the new secret.

### Risk 4: Policy Hot-Reload Race Condition (rbac-local)
**Description:** The `RBACLocalProvider` holds the policy in memory behind a `sync.RWMutex`. If the policy YAML file is replaced during a concurrent authorization request (e.g., `mv policy.yaml.new policy.yaml`), a partial read could occur if the file is not atomically replaced.
**Mitigation:** Use `os.Rename` (atomic on POSIX filesystems) for all policy file updates — never write in-place. On startup, validate the policy file schema before accepting it. Do NOT add `fsnotify` for hot-reload in phase 1 (it is not in `go.mod` and adds race complexity); reload is triggered by a SIGHUP handler or API restart instead. Document this constraint clearly.

### Risk 5: Anonymous Principal Leaking into Authorization Checks
**Description:** The `anonymous` provider returns an `Anonymous()` principal only when no credential signals are present. However, if `RequireAuth` middleware is accidentally omitted from a mutation route, the anonymous principal could reach an `authz.Authorize` call.
**Mitigation:** `RequireAuth` middleware must be applied to all mutation resolvers. With the `anonymous` provider gating the only path to an `Anonymous()` principal, the scenario of "credentials present but no provider accepted them" now results in a hard `Deny` at the chain level — it never reaches authz. The `rbac-local` policy still explicitly grants the `anonymous` role only `read` actions as a defense-in-depth backstop. CI lint rule: ban direct calls to `GetUserFromContext` in resolver files after migration is complete (replaced by `auth.PrincipalFromContext` which is checked).

# Feature Specification: Pluggable AuthN/AuthZ — Phase 1 Interface Foundation

**Feature Branch**: `031-pluggable-authn-authz`  
**Created**: 2026-06-25  
**Status**: Closed  
**GH Issue**: #225  
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`

## Overview

Replace the hardcoded `middleware.User` / `isAdmin=true` stubs with a real, swappable
auth framework. Phase 1 ships the interface contracts and four in-process providers
(`static-admin`, `allow-all`, `rbac-local`, `none-userdir`) so that every mutation and
resolver uses real AuthN and AuthZ decisions from day 1. Later phases layer in logout,
refresh, gRPC HMAC, Git smart-HTTP auth, OIDC, and OPA on top of the same interfaces.

## User Scenarios & Testing

### User Story 1 — Admin logs in and is recognized as admin (Priority: P1)

An operator with the admin username+password authenticates via the existing Login mutation.
The resulting JWT token carries the admin role. Subsequent resolver calls
(e.g., `createNamespace`) succeed without any code change — the new `static-admin`
provider validates the token and populates a `Principal` with `Roles: ["admin"]`.

**Why this priority**: This is the golden path that ALL existing integration tests exercise.
If it breaks, nothing else can be tested.

**Independent Test**: Run the existing `make bootstrap` flow against a freshly started API;
it must succeed end-to-end (login → createNamespace → createRepository).

**Acceptance Scenarios**:

1. **Given** `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` is set, **When** `Login(username, password)` is called with correct credentials, **Then** the mutation returns a JWT and `expiresAt`.
2. **Given** a valid admin JWT, **When** `createNamespace(tier: USER)` is called, **Then** the namespace is created.
3. **Given** a valid admin JWT, **When** `createNamespace(tier: ORGANIZATION)` is called, **Then** the namespace is created (admin bypass confirmed).
4. **Given** no JWT (unauthenticated), **When** `createNamespace` is called, **Then** the request is rejected with an authentication error.

---

### User Story 2 — AuthZ enforces namespace create/delete rules (Priority: P2)

The two hard-coded `isAdmin` checks in `service.go` are replaced by `authz.Authorize`
calls. With `rbac-local` provider active, only the `admin` role may delete another
owner's namespace or create an `ORGANIZATION`-tier namespace.

**Why this priority**: Directly fixes the "isAdmin always true" gap identified in the
auth gap analysis.

**Independent Test**: Unit-test `createNamespace` and `deleteNamespace` in `service_test.go`
with both `allow-all` and `rbac-local` providers. No external service needed.

**Acceptance Scenarios**:

1. **Given** principal has role `admin`, **When** `deleteNamespace` on a namespace owned by a different user, **Then** deletion succeeds.
2. **Given** principal has role `developer`, **When** `deleteNamespace` on a namespace owned by another user, **Then** returns `ErrForbidden`.
3. **Given** `allow-all` AuthZ provider, **When** `deleteNamespace` by any principal, **Then** deletion always succeeds.
4. **Given** `rbac-local` AuthZ provider with `admin` role, **When** `createNamespace(tier: ORGANIZATION)`, **Then** succeeds.

---

### User Story 3 — allow-all AuthZ warns on startup (Priority: P2)

When `GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all` is configured (local-fast profile),
the API logs a structured warning at startup so operators don't accidentally run this
in a sensitive environment.

**Why this priority**: Observability/safety guardrail required by the design doc.

**Independent Test**: Start API with `allow-all` and verify the zap warning log line
`"SECURITY: authz provider is allow-all"` appears in structured output.

**Acceptance Scenarios**:

1. **Given** `GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all`, **When** API starts, **Then** a `warn`-level structured log line with message `"SECURITY: authz provider is allow-all"` is emitted.

---

### User Story 4 — callerUsernameOrAnon returns real subject (Priority: P2)

`helpers.go:callerUsernameOrAnon` currently returns `"anon"` in both branches (stub).
After Phase 1 it must return the authenticated principal's Subject.

**Why this priority**: Affects audit trails — `CreatedBy` and `UpdatedBy` fields on
resources will always say `"anon"` until this is fixed.

**Independent Test**: Integration test: create a namespace after authenticating; the
returned `namespace.createdBy` field must equal the admin username, not `"anon"`.

**Acceptance Scenarios**:

1. **Given** an authenticated request with subject `"admin"`, **When** `createNamespace` runs, **Then** `namespace.createdBy == "admin"`.
2. **Given** an unauthenticated request, **When** `callerUsernameOrAnon` is called, **Then** returns `"anon"`.

---

### Edge Cases

- What happens when `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` is absent? → API fails to start with a clear error.
- What happens when a JWT from the old `Claims{Username, IsAdmin}` format arrives after migration? → The `static-admin` provider must still validate it (backward-compat during rollout).
- What happens when the `policy.yaml` file for `rbac-local` is missing? → Provider fails to load; API fails to start with a clear error.
- What happens when an action is not in the `rbac-local` policy and `default_deny: true`? → Returns `OutcomeDeny`.
- What happens when a Bearer token is present but no provider accepts it? → Chain returns `Deny` — no fallthrough to anonymous.
- What happens when no credentials are presented and the `anonymous` provider is in the chain? → `AnonymousProvider` claims the request and returns `Anonymous()` with `OutcomeAllow`.
- What happens when no credentials are presented and the `anonymous` provider is NOT in the chain? → Chain returns `Deny` — operator has explicitly opted out of anonymous access.
- What happens when a provider returns an error (not just Challenge/Deny)? → Chain short-circuits, error propagated to caller.

## Requirements

### Functional Requirements

- **FR-001**: System MUST define `auth.Principal`, `auth.Decision`, `auth.Outcome`, `auth.AuthNProvider`, `auth.AuthZProvider`, `auth.UserDirProvider` interfaces in `gitstore-api/internal/auth/types.go`.
- **FR-002**: System MUST implement `ChainedAuthN` that walks providers in order; first `Allow` wins; explicit `Deny` short-circuits; `Challenge` continues to next provider; if all providers return `Challenge` the chain returns `Deny` (not `Anonymous()`).
- **FR-013**: System MUST implement `anonymous` AuthN provider that returns `Allow` with `Anonymous()` only when no credential signals are present (`Authorization` header absent and `ForwardedSubject` empty); returns `Challenge` otherwise. This provider MUST be last in the chain.
- **FR-003**: System MUST implement `ProviderRegistry` holding the active AuthN chain, AuthZ provider, and UserDir provider behind `sync.RWMutex`.
- **FR-004**: System MUST implement `static-admin` AuthN provider that validates Bearer JWT (HS256, existing `GITSTORE_AUTH__JWT__SECRET`) and Basic Auth credentials against `GITSTORE_AUTH__ADMIN__PASSWORD_HASH`.
- **FR-005**: System MUST implement `allow-all` AuthZ provider that unconditionally returns `OutcomeAllow` and emits a `warn`-level zap log on startup.
- **FR-006**: System MUST implement `rbac-local` AuthZ provider that loads `policy.yaml`, checks principal roles against action/resource rules, and supports `default_deny`.
- **FR-007**: System MUST implement `none` UserDir provider that returns `ErrNotSupported` for all operations.
- **FR-008**: System MUST replace `middleware.GetUserFromContext` usage in all resolver files with `auth.PrincipalFromContext`; a compatibility shim in `middleware/auth.go` wraps the new function for the transition period.
- **FR-009**: System MUST replace the two hardcoded `isAdmin` checks in `service.go` with `authz.Authorize` calls using the action vocabulary defined in the design doc.
- **FR-010**: System MUST fix `callerUsernameOrAnon` to use `auth.PrincipalFromContext(ctx).Subject`.
- **FR-011**: System MUST wire the provider chain from Viper config keys `auth.authn.chain`, `auth.authz.provider`, `auth.userdir.provider` at startup.
- **FR-012**: Existing env vars (`GITSTORE_AUTH__ADMIN__*`, `GITSTORE_AUTH__JWT__*`) MUST continue to work without changes.

### Key Entities

- **Principal**: Provider-agnostic identity — Subject, Issuer, Roles, Groups, Scopes, Claims, AuthMethod, ExpiresAt.
- **Decision**: Auth outcome — Outcome (Allow/Deny/Challenge), Reason, Provider, At.
- **AuthNProvider**: Interface — Name, Capabilities, Authenticate, RevokeSession, RefreshSession.
- **AuthZProvider**: Interface — Name, Authorize.
- **UserDirProvider**: Interface — Name, GetBySubject, ListGroups, SearchUsers, UpsertProfile, Deactivate.
- **ProviderRegistry**: Holds active chain + authz + userdir; thread-safe.
- **ChainedAuthN**: Ordered list of AuthNProviders; first-Allow-wins semantics.
- **Policy (rbac-local)**: YAML v1 file — roles map, role_bindings map, default_deny flag.

## Success Criteria

### Measurable Outcomes

- **SC-001**: All existing integration tests in `gitstore-api/tests/` pass unchanged.
- **SC-002**: `make bootstrap` completes end-to-end against the new auth framework without config changes.
- **SC-003**: `callerUsernameOrAnon` returns the authenticated subject (not `"anon"`) for all authenticated requests.
- **SC-004**: Unit tests for `static-admin`, `allow-all`, `rbac-local`, and `none` providers cover all `Authenticate`/`Authorize` branches.
- **SC-005**: `allow-all` startup warning appears in structured logs when that provider is active.
- **SC-006**: `rbac-local` correctly denies `namespace.delete.any` to the `developer` role and allows it to `admin`.

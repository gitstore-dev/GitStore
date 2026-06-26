# Feature Specification: Pluggable AuthN/AuthZ — Phase 3 Session Lifecycle

**Feature Branch**: `032-auth-phase-3`
**Created**: 2026-06-26
**Status**: Closed
**GH Issue**: #226
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`
**Input**: User description: "auth phase 3"

## Overview

Wire the `logout` and `refreshToken` GraphQL mutations to the pluggable auth framework
established in Phase 1, and migrate the `login` resolver away from hardcoded stubs so that
all three session-lifecycle operations flow through the `ProviderRegistry`. After Phase 3,
users can end their sessions explicitly and extend them without re-entering credentials, and
the system rejects any token that has been revoked.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — User logs out and their token is invalidated (Priority: P1)

An authenticated user calls the `logout` mutation. The system adds the token's unique
identifier to the session blacklist. Any subsequent request that presents the same token
is rejected with an authentication error, as if the token had never been issued.

**Why this priority**: This is a fundamental security operation. Without it, a stolen or
leaked token remains valid until it expires naturally — potentially for the full token
lifetime. The mutation already exists in the schema; it just returns an error today.

**Independent Test**: Authenticate via `login` → call `logout` → immediately call a
protected mutation (e.g., `createNamespace`) with the same token → the request must be
rejected.

**Acceptance Scenarios**:

1. **Given** a valid authenticated session, **When** `logout` is called, **Then** the mutation returns `{ success: true }`.
2. **Given** a token that was used to call `logout`, **When** the same token is used on any subsequent request, **Then** the request is rejected with an authentication error.
3. **Given** an unauthenticated request (no token), **When** `logout` is called, **Then** the mutation returns an authentication error (cannot log out without a session).
4. **Given** a token that is within minutes of expiry, **When** `logout` is called, **Then** the revocation still succeeds and the token is blacklisted for its remaining lifetime.

---

### User Story 2 — User refreshes their token to extend their session (Priority: P1)

An authenticated user calls the `refreshToken` mutation. The system issues a new token
with a fresh expiry, simultaneously invalidating the old token. The user continues their
session with the new token without re-entering their credentials.

**Why this priority**: Without refresh, users must re-authenticate when their token expires
mid-session. This degrades the experience and — if clients cache credentials to handle it
silently — creates a larger security surface.

**Independent Test**: Authenticate via `login` → call `refreshToken` → verify the response
carries a new token with a later expiry → confirm the original token is now rejected.

**Acceptance Scenarios**:

1. **Given** a valid authenticated session, **When** `refreshToken` is called, **Then** the mutation returns a new `AuthSession` with a future `expiresAt`.
2. **Given** a refreshed session, **When** the original token is used on a subsequent request, **Then** the request is rejected (old token is revoked as part of refresh).
3. **Given** a token that expired within the configured grace period, **When** `refreshToken` is called, **Then** the mutation still succeeds and returns a new session (users can refresh slightly-late without re-authenticating).
4. **Given** a token that is already on the blacklist (previously revoked), **When** `refreshToken` is called with it, **Then** the mutation returns an authentication error.
5. **Given** a token that expired beyond the grace period, **When** `refreshToken` is called, **Then** the mutation returns an authentication error.

---

### User Story 3 — Login reflects the real principal, not hardcoded values (Priority: P2)

The `login` mutation's response is derived from the configured AuthN provider chain rather
than from hardcoded logic. The returned `user.isAdmin` field reflects whether the
authenticated principal genuinely holds the admin role according to the provider.

**Why this priority**: The current hardcoded `isAdmin: true` response means every logged-in
user appears as an admin to API clients regardless of their actual role. Phase 1 introduced
real principal resolution; this story extends that to the login response itself.

**Independent Test**: Log in as the admin user — `user.isAdmin` must be `true`. Any future
non-admin user (once non-admin users exist) must receive `user.isAdmin: false`.

**Acceptance Scenarios**:

1. **Given** valid admin credentials, **When** `login` is called, **Then** the returned `session.user.isAdmin` is `true` (derived from the principal's roles, not hardcoded).
2. **Given** valid admin credentials, **When** `login` is called, **Then** the returned `session.user.username` matches the authenticated principal's subject.
3. **Given** invalid credentials, **When** `login` is called, **Then** the mutation returns an authentication error (unchanged from current behaviour).

---

### Edge Cases

- What happens when the blacklist entry for a revoked token is pruned before the token's natural expiry? → The pruning goroutine must not remove entries until after `expiresAt`; tokens with no expiry are pruned on a configurable TTL.
- What happens when `refreshToken` is called twice in rapid succession with the same old token? → The first call revokes the old token; the second call must fail (old token is now blacklisted).
- What happens when the server restarts while tokens are on the blacklist? → The in-process blacklist is lost on restart; any previously revoked tokens that have not yet expired become valid again. This is a known limitation of the in-memory implementation (documented; persistent storage deferred to a future phase).
- What happens when no AuthN provider in the chain supports `RevokeSession`? → The `logout` mutation returns an error indicating revocation is not supported by the active configuration.
- What happens when the `Authorization` header is absent from a `logout` or `refreshToken` request? → The request is rejected by the auth middleware before the resolver runs.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The `logout` mutation MUST require an authenticated caller; unauthenticated requests MUST be rejected before the resolver runs.
- **FR-002**: The `logout` mutation MUST extract the `jti` and `expiresAt` from the caller's current token and call `RevokeSession` on the first AuthN provider in the chain that supports revocation.
- **FR-003**: After a successful `logout`, any request bearing the revoked token MUST be rejected with an authentication error for the remainder of that token's natural lifetime.
- **FR-004**: The `refreshToken` mutation MUST call `RefreshSession` on the first AuthN provider that supports it, passing the caller's current raw token.
- **FR-005**: A successful `refreshToken` call MUST return a new `AuthSession` (new token + new `expiresAt`) and MUST cause the old token to be revoked (blacklisted).
- **FR-006**: `RefreshSession` MUST accept tokens that have expired within a configurable grace window (default: 60 seconds) so that slightly-late refresh calls succeed without forcing re-authentication.
- **FR-007**: `RefreshSession` MUST reject tokens that are already on the blacklist, returning an authentication error.
- **FR-008**: The `login` mutation MUST use the `ProviderRegistry`'s AuthN chain to authenticate rather than the legacy `authMiddleware.ValidateCredentials` / `authMiddleware.GenerateSessionToken` path.
- **FR-009**: The `login` resolver MUST derive the `user.isAdmin` field from `principal.IsAdmin()` rather than from a hardcoded boolean.
- **FR-010**: The `login` resolver MUST derive `user.username` from `principal.Subject`.
- **FR-011**: The session blacklist MUST prune expired entries in the background so that memory usage does not grow unboundedly over time.
- **FR-012**: All existing integration tests MUST continue to pass without configuration changes.

### Key Entities

- **Session Blacklist**: In-process store mapping token identifiers to their expiry times; entries older than their expiry are eligible for pruning. Populated by `logout` and `refreshToken`.
- **Revoked Token**: A token whose identifier is present in the blacklist; treated as invalid regardless of its cryptographic validity.
- **Refresh Grace Window**: The configurable duration after token expiry during which `refreshToken` is still accepted. Controlled by a configuration key (default: `60s`).
- **AuthSession** (GraphQL type): Returned by `login` and `refreshToken` — contains `token`, `expiresAt`, and `user` (username + isAdmin).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A token used to call `logout` is rejected on the very next request; zero requests with a revoked token succeed.
- **SC-002**: `refreshToken` returns a new session within the same response time as `login` (no additional network calls required).
- **SC-003**: The old token is invalid immediately after `refreshToken` completes; clients that retry with the old token receive an authentication error.
- **SC-004**: All existing integration tests in `gitstore-api/tests/` pass unchanged after the `login` resolver migration.
- **SC-005**: `login` response carries `user.isAdmin: true` for the admin user and the correct `username` (not a hardcoded value).
- **SC-006**: Unit tests cover `logout` (success, unauthenticated caller, already-revoked token) and `refreshToken` (success, expired-within-grace, expired-beyond-grace, already-revoked) branches.
- **SC-007**: The blacklist pruning process does not remove entries before their expiry; memory growth is bounded for long-running instances.

## Assumptions

- The `static-admin` provider's `RevokeSession` and `RefreshSession` implementations (added in Phase 1) are correct and require no functional changes; Phase 3 only wires the resolvers to call them.
- The `clientMutationId` fields in `LogoutInput` and `RefreshTokenInput` are intentionally left in the schema for this phase; removal is a separate schema-hygiene task.
- The in-memory blacklist loses its state on server restart — this is an accepted limitation for single-instance deployments. Persistent blacklist storage is deferred to a later phase.
- No new GraphQL schema types or fields are required; the `logout` and `refreshToken` mutations are already defined in `shared/schemas/auth.graphqls`.
- The `refreshToken` mutation uses the caller's current bearer token as the old-token input; no separate `oldToken` argument is needed in the schema.

## Dependencies

- **Requires**: Phase 1 (`031-pluggable-authn-authz`) merged — provides `ProviderRegistry`, `StaticAdminProvider.RevokeSession`, `StaticAdminProvider.RefreshSession`, and `auth.PrincipalFromContext`.
- **Blocks**: Nothing — Phases 4 and 5 (gRPC HMAC, Git smart-HTTP) are independent of Phase 3 and may proceed in parallel.
- **GH Issue**: #226 (partial — migration tasks T018/T023/T024 were completed in Phase 1; this spec covers the remaining logout/refresh/login-cleanup work).

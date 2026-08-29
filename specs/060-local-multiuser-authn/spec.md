# Feature Specification: Local Multi-User AuthN Provider (`static-users`)

**Feature Branch**: `060-local-multiuser-authn`

**Created**: 2026-08-29
**Status**: Draft
**Input**: User description: "GitStore's `rbac-local` AuthZ provider already supports mapping multiple subjects to roles via `role_bindings` in its policy YAML, but no real AuthN provider today can authenticate more than one distinct human identity — `static-admin` structurally supports exactly one hardcoded admin credential. The only way the codebase currently exercises 'two different users' is a test-only backdoor (`tests/integration`'s `test-user:` bearer-token bypass). Ship a new, additional AuthN provider that authenticates against a config-driven list of username + bcrypt password-hash pairs, following `rbac-local`'s own file-driven-policy convention, so that (a) `rbac-local`'s existing multi-subject `role_bindings` support becomes actually exercisable end-to-end for the first time, and (b) core-component test suites (`gitstore-api`/`gitstore-git-service`/`gitstore-controller-manager`, the project's three *mandatory* components) can satisfy the constitution's namespace-isolation/security-boundary testing requirement using real, distinct, credential-authenticated principals — with zero optional-component dependency (no Hydra/Kratos, no bring-your-own IdP). This is a smaller, more foundational, always-available complement to spec 059's optional first-party OIDC stack, not a competitor to it. Do not change or replace `static-admin`, which remains the single-admin path unchanged. Retire the `test-user:` backdoor once the new provider exists."

## Clarifications

### Session 2026-08-29

- Q: Does this spec change, extend, or replace `static-admin`? → A: No. `static-admin` (`gitstore-api/internal/auth/provider/staticadmin/provider.go`) is left completely unchanged — its single hardcoded `cfg.Admin.Username`/`PasswordHash` credential, its JWT issuance, and its blanket `Roles: []string{"admin"}` assignment on any bearer token it successfully verifies all remain exactly as they are today. This spec adds a new, additional, opt-into-the-chain provider (`static-users`) alongside it. Single-admin deployments that never add `static-users` to `auth.authn.chain` see zero behavior change.
- Q: Which JWT signing secret/issuer does `static-users` use for the bearer tokens it issues? → A: Its own, independent, dedicated secret/issuer (`auth.staticusers.jwt.*`), never `static-admin`'s `auth.jwt.*`. Traced directly from `staticadmin/provider.go`'s `authenticateBearer`: it grants `Roles: []string{"admin"}` to **any** bearer token that verifies against its own secret and issuer, regardless of the token's `sub` claim. If `static-users` shared that same secret/issuer, a token minted for a non-admin `static-users` identity would later be accepted by `static-admin`'s own Bearer path and be silently treated as an admin token — a privilege-escalation path this spec must not introduce. A distinct secret/issuer is a hard requirement, not a style preference.
- Q: Does `rbac-local` need any code change to support multiple real subjects? → A: No, confirmed by reading `gitstore-api/internal/auth/provider/rbaclocal/provider.go` and `policy.go` in full. `Policy.RoleBindings` is already `map[string][]string` (subject → roles), and `Authorize` already merges `policy.RoleBindings[principal.Subject]` into the effective role set for whatever `principal.Subject` string it is handed — it has no hardcoded assumption limiting it to one subject. It has simply never been exercised with more than one real, distinct, non-test subject because no real AuthN provider produced one. Zero `rbac-local` code changes are required; only its `policy.yaml`'s `role_bindings` needs entries for the new real subjects an operator configures.
- Q: Does the existing `none` UserDir provider need to become something richer? → A: No, confirmed by an exhaustive search: no resolver, service, or mutation anywhere in `gitstore-api/internal/graph` calls `registry.UserDir()`. The `UserDirProvider` plane is fully wired into `ProviderRegistry` but has zero live consumers today. `rbac-local`'s `role_bindings` key off the bare `Principal.Subject` string alone, which `static-users` already supplies. There is no existing consumer this spec would need to satisfy by adding a richer UserDir implementation. If a future spec adds a UserDir consumer that needs email/display name, `static-users`' `users.yaml` entries are a natural future source — but that is out of scope here.
- Q: How does this relate to spec 059 (optional reference OIDC provider)? → A: Complementary, not competing, and each spec explicitly says so. `gitstore-api`/`gitstore-git-service`/`gitstore-controller-manager` are GitStore's only *mandatory* components; everything else — `gitstore-admin`, the storefront, and spec 059's optional Hydra+Kratos OIDC stack — is bring-your-own-or-build-your-own. Spec 059 is the real, production-grade, standards-compliant path for operators who want or need actual OIDC. This spec is the always-available, zero-optional-dependency path: it exists primarily so the mandatory core components' own test suites can prove namespace-isolation/security-boundary behavior with genuine distinct principals without requiring any optional component to be running, and secondarily as a legitimate lightweight authentication choice for small deployments that do not need a full IdP. Neither spec changes the other's design; this spec makes zero changes to `oidc-jwt`/spec 059.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An operator configures multiple named local users who can each authenticate with their own credentials (Priority: P1)

An operator running GitStore without a full IdP wants more than one real, distinct human identity to be able to log in — for example, to test namespace isolation between two users, or to run a small deployment with a handful of named local accounts instead of a single shared admin credential. They list each user (a username and a bcrypt password hash) in a config-driven file, add `static-users` to the authentication provider chain, and each listed user can now log in with their own credentials and receive their own bearer token carrying their own subject.

**Why this priority**: This is the entire reason this spec exists — without it, there is no real AuthN provider capable of producing more than one distinct authenticated identity anywhere in the mandatory core components.

**Independent Test**: Can be fully tested by configuring two users in the new provider's user list, calling the `login` mutation with each user's own username/password, and confirming each returns a valid token whose subject matches that user and no other.

**Acceptance Scenarios**:

1. **Given** a `static-users` configuration listing users `alice` and `bob` with distinct bcrypt password hashes, **When** `alice` logs in with her own username and password, **Then** authentication succeeds and the issued token's subject is `alice`.
2. **Given** the same configuration, **When** `bob` logs in with `alice`'s password, **Then** authentication fails.
3. **Given** the same configuration, **When** a username not present in the list attempts to log in, **Then** authentication fails without revealing whether the username exists.
4. **Given** `static-users` is not present in `auth.authn.chain`, **When** any of the configured users attempts to log in, **Then** authentication fails exactly as it would if the provider did not exist — the provider has no effect unless explicitly chained in.

---

### User Story 2 - `rbac-local`'s existing multi-subject `role_bindings` become exercisable end-to-end for the first time (Priority: P1)

An operator (or a test suite) wants two distinct, really-authenticated users to receive different roles from `rbac-local`'s policy — for example, `alice` bound to `namespace-owner` and `bob` bound to `developer` — and have those roles actually determine what each can do, using nothing but the mandatory core components.

**Why this priority**: Equal in importance to User Story 1 — a provider that can authenticate multiple users but whose identities cannot be meaningfully differentiated by the existing AuthZ layer would not close the gap this spec exists to close. `rbac-local`'s `role_bindings` already supports this; this spec is what finally gives it real subjects to bind.

**Independent Test**: Can be fully tested by binding two `static-users` identities to two different roles in `policy.yaml`'s `role_bindings`, authenticating as each, and confirming their authorization outcomes differ according to their bound roles — using only the existing, unmodified `rbac-local` provider.

**Acceptance Scenarios**:

1. **Given** `role_bindings` mapping `alice` to `namespace-owner` and `bob` to `developer`, **When** each authenticates via `static-users` and attempts an action only `namespace-owner` is allowed, **Then** `alice`'s attempt is allowed and `bob`'s is denied.
2. **Given** the same configuration, **When** either user's bearer token is later presented to a resolver that calls `AuthZProvider.Authorize`, **Then** the effective role set used for the decision is derived solely from `principal.Subject` and `role_bindings` — no `rbac-local` code path exists (or is added) that special-cases `static-users` versus any other AuthN provider.

---

### User Story 3 - A `static-users`-issued token can never be misinterpreted as a `static-admin` identity, or vice versa (Priority: P1)

A security reviewer (or an automated test) needs assurance that adding `static-users` to the chain cannot let a non-admin local user's token be accepted as an admin token, and cannot let `static-admin`'s existing admin token be reinterpreted by `static-users`.

**Why this priority**: Equal in importance to User Stories 1 and 2 — this is a non-negotiable security property, not a nice-to-have. A multi-user provider that quietly created a privilege-escalation path would be strictly worse than not shipping it at all.

**Independent Test**: Can be fully tested by authenticating as a `static-users` identity, taking the resulting bearer token, and presenting it directly to verify it is rejected by `static-admin`'s own verification path (wrong signing secret/issuer) before falling through to and succeeding against `static-users`' own verification path — and the symmetric check for a `static-admin`-issued token against `static-users`.

**Acceptance Scenarios**:

1. **Given** a valid `static-users`-issued bearer token for `alice` (not an admin), **When** it is presented as a bearer credential, **Then** the resulting principal never carries the `admin` role unless `rbac-local`'s own `role_bindings` explicitly grants it to `alice` by name.
2. **Given** a valid `static-admin`-issued bearer token, **When** it is presented as a bearer credential, **Then** `static-users` never claims or alters that token's verification outcome — `static-admin` remains the sole verifier for its own tokens, unchanged.
3. **Given** a login as a `static-users` identity, **When** the login mutation issues the resulting session token, **Then** the token is minted by the same provider that authenticated the identity (`static-users`), never by `static-admin` picking it up first purely due to provider ordering in the chain.

---

### User Story 4 - The `test-user:` integration-test backdoor is retired in favor of real logins through the new provider (Priority: P2)

The two-user namespace-isolation integration test (`tests/integration/authz_repository_contract_test.go`'s `TestRepositoryAuthorization_TwoUserNamespaceIsolation`) currently authenticates `alice` and `bob` using a literal `"test-user:alice"`/`"test-user:bob"` bearer-token string recognized only by a throwaway, test-only server binary's own bespoke `AuthNProvider` (`testUserAuthN`, defined and embedded as source text inside `tests/integration/namespace_contract_test.go`, compiled into a helper binary that is never part of the real `gitstore-api` build). Once `static-users` exists, this test (and its shared harness) is migrated to configure two real `static-users` identities and authenticate through real credential-based logins instead.

**Why this priority**: Lower urgency than User Stories 1–3 because the backdoor's existence does not itself break anything in production — it is never compiled into the real service binary — but it is a lingering security smell (a fake-identity bypass sitting in the same codebase as production auth code, even if not shipped) and it is the single existing place this codebase pretends to exercise "two real users" without actually doing so.

**Independent Test**: Can be fully tested by running `TestRepositoryAuthorization_TwoUserNamespaceIsolation` (and any other test found to rely on the same mechanism) after migration and confirming it passes using two real, credential-authenticated `static-users` logins, with the `test-user:` string bypass mechanism and its `testUserAuthN` type removed from the test harness entirely.

**Acceptance Scenarios**:

1. **Given** the migrated test harness, **When** `TestRepositoryAuthorization_TwoUserNamespaceIsolation` runs, **Then** `alice` and `bob` are authenticated via real `login` calls against two `static-users`-configured identities, not via a `test-user:`-prefixed bearer string.
2. **Given** the migrated harness, **When** any test in `tests/integration/` is searched for the literal string `test-user:`, **Then** zero matches are found.
3. **Given** the migrated harness's synthetic server helper, **When** its `AuthNProvider` chain is inspected, **Then** it uses the real `static-users` provider (configured with test fixture credentials) in place of the removed `testUserAuthN` bespoke type.

---

### Edge Cases

- What happens when `users.yaml` (or whatever the configured file is named) is missing, empty, malformed YAML, or fails schema validation? (Provider construction fails fast at startup, exactly mirroring `rbac-local`'s existing `loadPolicy` fail-fast behavior for a missing/invalid `policy.yaml` — the server does not start with a partially-loaded or silently-empty user list.)
- What happens when two entries in the user list share the same username? (Rejected at load time as an invalid configuration, before the provider becomes active — usernames must be unique within the file.)
- What happens when an operator edits the user list to add, remove, or change a password for a user while the server is running? (Reloadable via the same SIGHUP-triggered mechanism `rbac-local` already uses for `policy.yaml`, using the same atomic-replace (`os.Rename`) file-update discipline — no new reload mechanism is invented.)
- What happens when a username in `users.yaml` collides with `static-admin`'s configured admin username? (Both providers evaluate independently in chain order for `Authenticate`; whichever is listed first in `auth.authn.chain` and recognizes the username wins for authentication purposes. This spec does not forbid the collision, but documents it as an operator misconfiguration risk to avoid, since the two providers grant different role semantics for the "same" username string.)
- What happens to a `static-users` identity that is removed from `users.yaml` while it holds an unexpired token? (Existing tokens remain valid until their natural expiry or until explicitly revoked via `logout` — removing a user from the file does not retroactively invalidate already-issued tokens, exactly mirroring how `static-admin`'s in-memory blacklist model already works for revocation-only invalidation.)
- What happens when a `static-users` identity has no entry in `rbac-local`'s `role_bindings`? (The principal carries no roles from `static-users` itself — unlike `static-admin`, this provider never hardcodes a role — so `rbac-local`'s `default_deny` behavior applies exactly as it would for any other unbound subject.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide a new AuthN provider, `static-users`, that authenticates HTTP Basic Auth credentials against a config-driven, file-loaded list of `{username, bcrypt password hash}` pairs, following the same load/validate/fail-fast pattern `rbac-local` already uses for `policy.yaml`.
- **FR-002**: `static-users` MUST support two or more configured users; it MUST NOT impose the single-credential limitation `static-admin` has today.
- **FR-003**: The system MUST leave `static-admin`'s existing behavior, configuration keys, and single-admin-credential model completely unchanged. `static-users` MUST be an additional, opt-in entry in `auth.authn.chain`, never a replacement.
- **FR-004**: `static-users` MUST use a JWT signing secret and issuer that are configured independently of, and distinct from, `static-admin`'s `auth.jwt.secret`/`auth.jwt.issuer`.
- **FR-005**: `static-users` MUST NOT assign any hardcoded role to a successfully authenticated principal. Role assignment for `static-users` identities MUST be determined entirely by `rbac-local`'s existing `role_bindings` mechanism (or whatever AuthZ provider is active), keyed on the principal's bare subject string.
- **FR-006**: A bearer token issued by `static-users` MUST fail verification against `static-admin`'s own bearer-verification path (distinct secret/issuer), and a bearer token issued by `static-admin` MUST fail verification against `static-users`' own bearer-verification path — neither provider may successfully verify a token it did not itself issue.
- **FR-007**: When a principal authenticated via `static-users` requests a new session token (the `login` mutation's issuance step), the token MUST be issued by the same provider that authenticated the principal — chain iteration order MUST NOT allow a different AuthN provider that also supports session issuance to mint the token instead. The reverse MUST also hold for `static-admin`.
- **FR-008**: `static-users` MUST reject two configured usernames that collide within its own user list at load time, before the provider becomes active.
- **FR-009**: `static-users`' configuration file MUST support a live reload triggered the same way `rbac-local`'s policy reload is triggered (SIGHUP), using the same atomic-replace file-update discipline, without requiring a server restart.
- **FR-010**: `static-users` MUST support `RevokeSession` (logout) and `RefreshSession` (refresh token) with the same session-lifecycle semantics `static-admin` already implements (in-memory blacklist, refresh grace window), scoped to sessions it itself issued.
- **FR-011**: The `rbac-local` AuthZ provider MUST require no source code change to support multiple real `static-users` subjects — its existing `role_bindings` mechanism is confirmed sufficient and MUST remain as-is.
- **FR-012**: The `none` UserDir provider MUST remain the active UserDir implementation; this spec introduces no new UserDir provider and no change to `UserDirProvider`, since no existing consumer requires one.
- **FR-013**: The `test-user:`-prefixed bearer-token bypass mechanism (the `testUserAuthN` type and its embedded-source-text duplicate inside the synthetic test-server helper, both in `tests/integration/namespace_contract_test.go`) MUST be removed and replaced with a `static-users` provider configured with test fixture credentials.
- **FR-014**: Every test found (by an exhaustive `grep -rn "test-user:" tests/integration/`) to depend on the `test-user:` bypass — confirmed today to be `tests/integration/authz_repository_contract_test.go`'s `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its helpers `createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser` — MUST be migrated to authenticate through real `static-users` logins, with identical assertions about namespace-isolation behavior.
- **FR-015**: The migration in FR-013/FR-014 MUST NOT alter or remove the unrelated `/__test/resource-body` test-only endpoint defined in the same synthetic helper server — it is a separate, non-identity-related test fixture and is out of scope for this spec's backdoor-retirement goal.
- **FR-016**: `gitctl`'s existing `hash-password` subcommand MUST remain the documented, supported way to generate a `static-users` entry's bcrypt password hash — no new hashing tool is introduced.

### Key Entities

- **`static-users` provider**: A new `AuthNProvider` implementation authenticating HTTP Basic Auth credentials against a config-driven list of local users, structurally sibling to `static-admin` but supporting many identities instead of one, and structurally borrowing `rbac-local`'s file-driven-policy convention for how its user list is loaded, validated, and reloaded.
- **User list file**: A YAML file (default `users.yaml`, mirroring `rbac-local`'s `policy.yaml` convention) listing `{username, password_hash}` entries, versioned (`version: v1`) and validated at load time.
- **Local user identity**: A single configured `{username, bcrypt password hash}` pair. Distinct from a `UserProfile` (no display name, email, or group data) — it is exactly as rich as `static-admin`'s single credential, just multiplied.
- **`role_bindings` (existing, unmodified)**: `rbac-local`'s existing `subject → []role` map in `policy.yaml`, which this spec is the first to exercise with more than one real, non-test subject.
- **`test-user:` backdoor (retired by this spec)**: The `testUserAuthN` AuthNProvider type and its duplicated embedded-source-text form, both scoped entirely to `tests/integration`, never part of the production `gitstore-api` binary, recognizing any `Authorization: Bearer test-user:<subject>` header as an automatically-authenticated principal with the hardcoded `developer` role.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator can configure two or more distinct local users in `static-users` and have each successfully log in with only their own credentials, with 100% of cross-user credential attempts (each user's password against another user's username) rejected.
- **SC-002**: 100% of `static-users`-issued bearer tokens are rejected by `static-admin`'s own bearer-verification path, and 100% of `static-admin`-issued bearer tokens are rejected by `static-users`' own bearer-verification path.
- **SC-003**: 100% of `static-users` logins result in a session token issued by `static-users` itself, with zero instances of `static-admin` (or any other chained provider) issuing the token instead due to chain ordering.
- **SC-004**: Two `static-users` identities bound to two different `rbac-local` roles via `role_bindings` produce different, correct authorization outcomes for at least one role-differentiated action, with zero `rbac-local` source changes required to achieve this.
- **SC-005**: After migration, zero occurrences of the literal string `test-user:` remain anywhere under `tests/integration/`, and `TestRepositoryAuthorization_TwoUserNamespaceIsolation` continues to pass with equivalent namespace-isolation assertions using real `static-users` logins.
- **SC-006**: `static-admin`'s existing behavior, configuration, and test suite pass unchanged with zero modifications required to any of its source files.

## Assumptions

- `static-users` is scoped to HTTP Basic Auth authentication (username + password) plus the resulting bearer-token session lifecycle (issue/refresh/revoke), exactly mirroring the subset of `static-admin`'s `Authenticate` method that handles Basic Auth and bearer verification for its own issued tokens. It is not an OIDC provider and makes no changes to `oidc-jwt`/spec 059.
- User self-registration, a management UI, or GraphQL mutations for creating/updating/removing users are explicitly out of scope for v1 — the user list is config-file-driven only, matching `rbac-local`'s own file-driven simplicity. Adding or removing a user is an operator file edit plus a reload (FR-009), not an API call.
- This spec does not introduce a persistent, cross-instance session store. `static-users`' revocation/refresh state is in-memory per instance, exactly matching `static-admin`'s existing, documented single-instance limitation (Phase 3 of `docs/implementation/020-pluggable_auth_architecture.md`).
- This spec is not a replacement for real production multi-tenant authentication at scale. For operators who need or want a standards-compliant, externally-federatable identity provider, spec 059 (optional Hydra+Kratos OIDC stack) or any bring-your-own OIDC issuer via the already-designed `oidc-jwt` provider remains the intended production path. `static-users` is explicitly the lightweight, always-available, testing-and-small-deployment complement to that path, not a competing alternative.
- `docs/runbooks/production-readiness-testing.md`'s Pattern 4 (namespace-isolation/security-boundary testing) worked-example citations currently point at the `test-user:`-based test. Once this spec ships and the migration in User Story 4 lands, that runbook's citations should be updated to describe the real-login-based test instead — tracked as a follow-up cross-reference, not performed by this spec itself.

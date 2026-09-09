# Feature Specification: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

**Feature Branch**: `059-optional-oidc-provider`

**Created**: 2026-08-29  
**Status**: Draft (replayed against `main` 2026-09-04 — see Clarifications Session 2026-09-04)  
**Input**: User description: "GitStore's AuthN/AuthZ architecture documents a Phase 7 OIDC JWT provider (`OIDCJWTProvider`) that is a generic, issuer-agnostic OIDC Relying Party — it already works against any standards-compliant OIDC issuer with zero code changes, and that design must not change. GitStore is 'bring your own' for everything optional (storefront, admin, and now identity), but for anyone who does not already have an OIDC IdP and wants something that 'just works,' ship an optional, separately-deployable first-party reference OIDC provider backed by Ory Kratos as the identity/session source of truth. A side-by-side experiment compared two architectures — Dex+Oathkeeper+Kratos vs. Ory Hydra+Kratos — and Hydra+Kratos was chosen. Kratos is the first-class supported identity directory for now; other directories are future work based on demand."

## Clarifications

### Session 2026-08-29

- Q: Does this spec change or re-scope Phase 7's `OIDCJWTProvider` (the OIDC Relying Party in `gitstore-api`)? → A: No. Phase 7 (`docs/implementation/020-pluggable_auth_architecture.md` §7) stays exactly as documented: a generic, issuer-agnostic Relying Party that verifies bearer JWTs via OIDC Discovery + JWKS (`go-oidc/v3`) against whatever `issuer_uri` an operator configures. This spec adds one additional, optional choice of issuer — it makes zero changes to Phase 7's RP-side code, interface, or config schema.
- Q: Which architecture does the optional reference provider use — Dex+Oathkeeper+Kratos ("Approach A") or Ory Hydra+Kratos ("Approach B")? → A: Approach B (Hydra + Kratos). Confirmed by a side-by-side experiment (`juliuskrah/experiments` PR #1, `oidc/specs/COMPARISON.md`): Dex's `authproxy` connector cannot issue a real OAuth2 `refresh_token` — a confirmed architectural limitation of the connector (it never implements `connector.RefreshConnector`), not a configuration gap — and compensates via a heavier "silent re-authorization" workaround. Hydra issues real, standards-compliant refresh tokens. This matters specifically for GitStore because `gitstore-api`'s own `refreshToken` mutation already returns `ErrNotSupported` for OIDC-authenticated sessions (Phase 3d) — GitStore has no independent session layer to compensate for a weak IdP refresh story, so the IdP's own refresh capability carries more weight here than in a system that manages its own sessions independently. Full rationale, including the counter-consideration explicitly weighed and deferred, is recorded in `research.md` Decision 1.
- Q: Where does the Hydra `/login` + `/consent` bridge code live — a new standalone service, or folded into `gitstore-admin`? → A: A new standalone minimal service, `gitstore-oidc-bridge`. `gitstore-admin` is itself an optional, bring-your-own reference component, and per project history is paused and drifted, with a framework rewrite still only a future consideration — coupling infrastructure the reference OIDC stack cannot function without to a frontend of uncertain near-term shape was rejected. Full rationale in `research.md` Decision 2.
- Q: Does this spec cover federating identity from directories other than Kratos (LDAP, GitHub, Google, a homegrown directory, etc.)? → A: No. Kratos is the first-class supported directory for this spec; other directories are explicitly deferred to future specs based on user demand. Dex's connector-federation model was considered and is noted as the point at which this architecture choice should be revisited, not as an oversight — see `research.md` Decision 1's counter-consideration.

### Session 2026-09-09 (replay against current `main`, scope change)

- Q: Is Phase 7's `OIDCJWTProvider` implementation in scope now? → A: **Yes** (user directive, superseding the 2026-08-29 "existing (or forthcoming)" stance). This spec now also delivers the Relying-Party implementation in `gitstore-api` — exactly per `docs/implementation/020-pluggable_auth_architecture.md` §7's pre-existing design (Discovery + JWKS via `go-oidc/v3`, `auth.oidc.*` config keys, `oidc-jwt` chain entry) — so the reference stack has a real consumer to verify against. The *design* is unchanged (FR-012 stands); only the implementation timing moved into this spec. UserInfo enrichment (RFC 7662 introspection stays out) covers access tokens that lack profile claims.
- Q: Where do the stack's config files live? → A: `config/oidc/` (the `config/` tree is the repo's config home; the earlier `deploy/oidc/` layout predated that convention and was removed).
- Q: The bridge's health route? → A: `/health`, matching every other GitStore service (`/healthz` dropped).
- Q: How are the stack's `make` targets organized? → A: Consolidated under `make compose`, mirroring how `scylla-compose` folded in: an `IDENTITY=oidc` selector layers `compose.oidc.yml` onto the core stack (see plan.md "Replay notes" and tasks.md Phase 7).
- Q: Which claim becomes `Principal.Subject` for OIDC principals? → A: Operator-configurable via `auth.oidc.username_claim` (default `sub`), following the Kubernetes `--oidc-username-claim` / Spring Security `user-name-attribute` / oauth2-proxy `--user-id-claim` pattern — every major consumer keeps `sub` as default and lets the operator pick a human-readable claim. The raw `sub` is always preserved in `Principal.Claims["sub"]`, and userinfo is consulted (fill-only-missing) when the configured claim is absent from the token, so Hydra access tokens resolve it too. Caveat recorded for the reference stack: Kratos enforces uniqueness of `email` (a credential identifier) but NOT `username`, so `email` is the safe human-readable binding key; `preferred_username` is suitable for display/logs when collision risk is acceptable locally.

### Session 2026-09-04 (replay against current `main`)

- Q: Does the landed spec 060 static-users migration (#405) change this spec? → A: No — spec 060 removed the `static-admin` provider in favor of `static-users`, which only affects example AuthN chains in this spec's supporting docs (updated to `oidc-jwt,static-users,anonymous`); the reference OIDC stack itself is independent of which local AuthN providers exist.
- Q: Does the shared local compose profile (#410) change the operator workflow? → A: No re-scoping — the stack still gets its own overlay (`compose.oidc.yml`) and `make` targets; the targets now wrap the same `docker compose --profile local -f compose.yml -f compose.local.yml` base the `admin-*` targets already use, and inherit the `_check-local-config` validation prerequisite.
- Q: Does spec 061's service-account authentication (#409) interact with this stack? → A: No — controller/machine identity is a separate, GitStore-issued identity plane (ADR 0009); the reference OIDC provider concerns human identity only, and 061's addendum already cross-references this spec's.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An operator without their own OIDC IdP can stand up a working identity provider in one step (Priority: P1)

An operator evaluating or running GitStore has no existing OIDC identity provider and does not want to stand up and operate Keycloak, Auth0, Okta, or a homegrown IdP just to exercise `gitstore-api`'s OIDC authentication path. They deploy GitStore's optional reference OIDC provider stack, point `gitstore-api`'s existing (or forthcoming) `OIDCJWTProvider` `issuer_uri` configuration at it, and authentication works — with no different treatment than pointing that same configuration at any other standards-compliant issuer.

**Why this priority**: This is the entire reason this spec exists — the "just works" default for anyone who cannot or does not want to bring their own IdP. Without this, GitStore's OIDC story is theoretically complete (Phase 7) but practically unusable for anyone starting from zero.

**Independent Test**: Can be fully tested by bringing up the optional reference stack alone, obtaining a token through the stack's standard OIDC Authorization Code + PKCE flow, and confirming that a client presenting that token as a bearer credential to `gitstore-api` is authenticated exactly as the Phase 7 design already specifies for any OIDC issuer — without needing any `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` source change.

**Acceptance Scenarios**:

1. **Given** a fresh checkout with no existing identity infrastructure, **When** the operator brings up the optional reference OIDC provider stack, **Then** a standards-compliant OIDC discovery document and JWKS endpoint become reachable at a well-known issuer URL.
2. **Given** the reference stack is running, **When** `gitstore-api` is configured with `issuer_uri` pointed at that stack's issuer, **Then** no code change is required in `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` for token verification to work.
3. **Given** the reference stack is running and a caller has completed its standard login flow, **When** the resulting token is presented as a bearer credential, **Then** it is accepted on the same terms Phase 7 already defines for any compliant issuer (valid signature, issuer, audience, expiry, with configured clock-skew leeway).

---

### User Story 2 - A new user can self-register and log in through Kratos without any custom UI beyond what the reference stack provides (Priority: P1)

A user with no existing account uses the reference stack's Kratos self-service registration and login flows to create an identity and authenticate, without the operator having built any custom registration or login screen.

**Why this priority**: Equal in importance to User Story 1 — a reference IdP that requires a client application to build its own registration/login UI before anyone can use it defeats the "just works" goal.

**Independent Test**: Can be fully tested by completing Kratos's self-service registration flow for a new identity, then completing a login for that identity, without writing any custom registration or login UI.

**Acceptance Scenarios**:

1. **Given** no existing identity, **When** a user completes Kratos's self-service registration flow with an email and username, **Then** a Kratos identity is created carrying those traits.
2. **Given** an existing Kratos identity, **When** the user completes Kratos's self-service login flow, **Then** a Kratos session is established.

---

### User Story 3 - Hydra's login and consent challenges are resolved automatically against the current Kratos session (Priority: P1)

When a client application drives the standard OIDC Authorization Code flow against the reference stack's OIDC provider, Hydra issues a login challenge and (once resolved) a consent challenge. These must be resolved by checking the browser's current Kratos session — automatically accepting when a valid session exists, and sending the browser to Kratos's login UI first when it does not — with no separate, user-facing consent screen, since the reference stack's registered OAuth2 client(s) are first-party by construction.

**Why this priority**: Equal in importance to User Stories 1 and 2 — without this bridging logic, Hydra (which has no user store of its own) cannot complete any login at all; this is the piece that makes the "Kratos is the identity source of truth" design real rather than aspirational.

**Independent Test**: Can be fully tested by driving an Authorization Code request against Hydra twice — once with a valid Kratos session already present (expect an immediate redirect back to the client with an authorization code and no consent screen), and once with no Kratos session present (expect a redirect to Kratos's login UI before any login challenge is resolved).

**Acceptance Scenarios**:

1. **Given** a valid, current Kratos session, **When** Hydra issues a login challenge for that browser, **Then** the challenge is accepted automatically, using the Kratos identity's own identifier as the resulting subject.
2. **Given** no Kratos session (or an expired one), **When** Hydra issues a login challenge, **Then** the browser is redirected to Kratos's self-service login UI, and the login challenge is only resolved after that login completes.
3. **Given** an accepted login challenge, **When** Hydra issues the corresponding consent challenge, **Then** it is accepted automatically with no user-facing consent screen, granting exactly the scopes the client requested among those the registered client is permitted.
4. **Given** an accepted consent challenge, **When** the resulting ID token is issued, **Then** its claims are populated from the Kratos identity's traits (at minimum email and a stable subject identifier), not from placeholder or default values.

---

### User Story 4 - Kratos identity traits map cleanly onto what `gitstore-api` already expects from an OIDC principal (Priority: P2)

An operator (or `gitstore-api` itself, via its already-documented `OIDCJWTProvider`) needs a predictable way to go from a Kratos identity to the fields `gitstore-api`'s `Principal` type expects from an OIDC-authenticated caller: a stable subject, an issuer, and claims such as email — without inventing new mapping infrastructure inside `gitstore-api`.

**Why this priority**: Lower urgency than User Stories 1–3 because the reference stack is already functional without this being formally documented, but it is what makes the reference stack's output trustworthy and consistent with the existing `Principal` contract rather than something each operator has to reverse-engineer.

**Independent Test**: Can be fully tested by inspecting a token issued by the reference stack for a known Kratos identity and confirming its claims map deterministically to `Principal.Subject`, `Principal.Issuer`, and `Principal.Claims` as documented, with no ambiguity about which Kratos trait produced which claim.

**Acceptance Scenarios**:

1. **Given** a Kratos identity with known traits, **When** that identity completes a login and a token is issued, **Then** the token's `sub` claim is the Kratos identity's own stable identifier (not its mutable email), and its `email` claim matches the Kratos identity's `email` trait.
2. **Given** a token issued by the reference stack, **When** it is documented against `gitstore-api`'s `Principal` type, **Then** the mapping from each OIDC claim to each `Principal` field is explicit and requires no additional, undocumented transformation.

---

### User Story 5 - The reference stack is deployed, run, and torn down the same way as GitStore's other optional stacks (Priority: P2)

An operator manages the reference OIDC provider stack's lifecycle (start, view logs, stop, remove) using the same `make`-target-driven workflow already established for the other optional stacks (`make scylla`, `make admin-compose`), rather than learning a bespoke set of `docker compose` invocations.

**Why this priority**: Lower urgency than User Stories 1–4 because the stack is usable without this convenience, but consistency with the existing optional-stack operator experience is an explicit design goal (this pattern is already established for `gitstore-admin`).

**Independent Test**: Can be fully tested by running the new stack's `make` targets end-to-end (bring up, view status/logs, stop, tear down) and confirming the same idempotency and layering behavior already exhibited by `make scylla`/`make admin-compose`.

**Acceptance Scenarios**:

1. **Given** a fresh checkout, **When** the operator runs the reference stack's bring-up target, **Then** every component of the stack (identity provider, session store, bridge, and their backing databases) starts, and the stack's OAuth2 client registration completes idempotently with no manual step.
2. **Given** the stack is already running, **When** the operator re-runs the bring-up target, **Then** it is a no-op with respect to already-provisioned state (no duplicate client registration, no destructive restart of existing data).
3. **Given** the stack is running, **When** the operator runs the stack's tear-down target, **Then** every component the target owns stops and is removed without affecting the core `api`/`git-service`/`controller-manager` stack.

---

### User Story 6 - `gitstore-api` verifies tokens from the reference stack through the generic `oidc-jwt` provider (Priority: P1)

A caller holding a token issued by the reference stack (or any standards-compliant OIDC issuer) presents it as a bearer credential to `gitstore-api`, and the `oidc-jwt` AuthN provider — this spec's implementation of Phase 7's pre-existing `OIDCJWTProvider` design — authenticates the caller by verifying the token via OIDC Discovery + JWKS, with no issuer-specific code.

**Why this priority**: Without the RP-side provider, the reference stack's tokens have no consumer inside GitStore — the "just works" story ends at token issuance. Implementing Phase 7's already-documented design here gives the stack a real verifier and closes the loop end-to-end.

**Independent Test**: Can be fully tested by configuring `auth.authn.chain` with `oidc-jwt` and `auth.oidc.issuer_uri` pointed at any compliant issuer, then presenting a token from that issuer: valid tokens authenticate with `Principal.Subject`/`Issuer`/`Claims`/`Scopes` populated per the claims-mapping contract; expired tokens are denied; foreign-issuer tokens fall through to the next provider in the chain.

**Acceptance Scenarios**:

1. **Given** `oidc-jwt` in the AuthN chain and a token from the configured issuer, **When** the token is presented as a bearer credential, **Then** it is verified via discovery + JWKS and the resulting principal carries `sub` as `Subject`, the issuer URL as `Issuer`, granted scopes as `Scopes`, and `email`/`preferred_username` claims when present.
2. **Given** a token whose access-token claims lack profile information, **When** the issuer exposes a userinfo endpoint, **Then** the provider enriches the principal from userinfo on a fill-only-missing basis.
3. **Given** a token issued by a different issuer (e.g. `static-users`' sessions), **When** it reaches `oidc-jwt`, **Then** the provider challenges and the token falls through to the next chained provider.
4. **Given** an expired token from the configured issuer, **When** it is presented, **Then** authentication is denied (within the configured clock-skew leeway it is still accepted).
5. **Given** the issuer rotates its signing keys, **When** a token signed by the new key arrives, **Then** verification still succeeds via JWKS refresh.

---

### Edge Cases

- What happens when a client application requests an OAuth2 scope the registered client is not permitted to request? (The consent challenge is resolved by granting only the subset of requested scopes the client is registered for; anything outside that set is not silently granted.)
- What happens when Hydra's refresh-token grant succeeds but the underlying Kratos session has since ended? (Out of scope for `gitstore-api` to detect or care about directly — Phase 3d already establishes that `gitstore-api` never attempts to refresh OIDC sessions itself. Any refresh-time re-validation against Kratos is the responsibility of whichever client application performs the token refresh, consistent with the reference experiment's own finding that a Hydra refresh grant does not, by itself, prove the Kratos session is still valid.)
- What happens if the reference stack's Hydra or Kratos Admin API were reachable from outside the deployment? (This must never be the default configuration — Admin APIs are reachable only by `gitstore-oidc-bridge` and other in-network components, never by a browser or the public network.)
- What happens on a completely fresh deployment (empty Hydra/Kratos databases, no OAuth2 client registered yet)? (Stack startup registers the necessary OAuth2 client(s) automatically and idempotently before the stack is considered ready; no manual `hydra create oauth2-client`-equivalent step is required.)
- What happens when an operator deploys this stack alongside their own separate, unrelated OIDC IdP? (Unsupported combination for a single `gitstore-api` deployment — `OIDCJWTProvider` is configured with exactly one `issuer_uri` at a time, per Phase 7's existing design; this spec does not change that.)
- What happens to a user's Kratos identity if the reference stack is decommissioned in favor of a different OIDC IdP later? (Out of scope — migrating identities between IdPs, including to a different bring-your-own IdP, is not addressed by this spec.)

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide an optional, separately-deployable reference OIDC provider stack (Ory Hydra as the OAuth2/OIDC provider, Ory Kratos as the identity/session source of truth) that can be deployed without any issuer-specific change to `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` source code — the generic `oidc-jwt` provider (FR-016) consumes it by configuration alone.
- **FR-002**: The reference stack MUST expose a standards-compliant OIDC issuer — discovery document and JWKS — reachable at a configurable issuer URL, suitable for `gitstore-api`'s Phase 7 `OIDCJWTProvider` `issuer_uri` configuration with no Relying-Party-side code change.
- **FR-003**: The system MUST provide a minimal, standalone bridge service (`gitstore-oidc-bridge`) that resolves Hydra's login and consent challenges by checking the current Kratos session.
- **FR-004**: The bridge MUST accept a Hydra login challenge automatically, using the Kratos identity's stable identifier as the resulting subject, whenever a valid, current Kratos session exists for the requesting browser.
- **FR-005**: The bridge MUST redirect to Kratos's self-service login UI, deferring resolution of the corresponding login challenge, whenever no valid Kratos session exists for the requesting browser.
- **FR-006**: The bridge MUST accept a Hydra consent challenge automatically, with no user-facing consent screen, granting only the subset of the requested scopes the registered OAuth2 client is permitted to request.
- **FR-007**: The reference stack MUST support issuing real, standards-compliant OAuth2 refresh tokens (the `offline_access` scope) for authenticated sessions, rotating each refresh token on use.
- **FR-008**: Both Hydra's and Kratos's Admin APIs MUST NOT be reachable from outside the deployment's internal network boundary — only `gitstore-oidc-bridge` and other in-network components may reach them.
- **FR-009**: The system MUST define a Kratos identity schema (traits) for GitStore user identities covering at minimum a unique email and a username, and MUST document a deterministic mapping from Kratos identity traits to the OIDC ID token claims consumed by `gitstore-api`'s `Principal` type (`Subject`, `Issuer`, `Claims`).
- **FR-010**: The reference stack's required OAuth2 client registration(s) with Hydra MUST be automated and idempotent on stack startup, requiring no manual registration step on a fresh deployment and no duplicate registration on a repeated startup.
- **FR-011**: Operators MUST be able to start, inspect, stop, and tear down the reference stack using `make` targets that follow the same naming and layering conventions already established for the other optional stacks (`make scylla`/`make compose DATASTORE=scylla`, `make admin-compose`/`make admin-down`).
- **FR-012**: This spec MUST NOT modify the RP-side design, interface, or scope of `gitstore-api`'s Phase 7 `OIDCJWTProvider` as documented in `docs/implementation/020-pluggable_auth_architecture.md` §7.
- **FR-013**: This spec MUST NOT implement federation to any identity directory other than Kratos; other directories remain explicitly deferred to future specs based on demand.
- **FR-014**: `docs/implementation/020-pluggable_auth_architecture.md` §7 MUST gain a short addendum cross-referencing this spec as the optional first-party issuer choice, without altering Phase 7's own Relying-Party description.
- **FR-015**: The reference stack MUST remain entirely optional — no default `make` target, compose invocation, or startup path for `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` MUST depend on this stack being present.
- **FR-016**: The system MUST provide an `oidc-jwt` AuthN provider in `gitstore-api` implementing Phase 7's documented `OIDCJWTProvider` design: bearer JWTs verified via OIDC Discovery + JWKS, enforcing issuer, audience, and expiry with configurable clock-skew leeway, and chained via `auth.authn.chain` like every other provider.
- **FR-017**: The `oidc-jwt` provider MUST map token claims onto `Principal` per `data-model.md`'s claims-mapping table (`sub`→`Subject`, `iss`→`Issuer`, `scope`→`Scopes`, `jti`→`TokenID`, `email`/`preferred_username`→`Claims`), and MUST enrich from the issuer's userinfo endpoint — on a fill-only-missing basis — when the presented access token lacks profile claims; it MUST NOT use token introspection (RFC 7662) on the hot path.
- **FR-018**: The `oidc-jwt` provider MUST return `ErrNotSupported` for session issuance, refresh, and revocation (Phase 3d boundary: GitStore has no independent session layer for OIDC principals; refresh is between the client application and the issuer).
- **FR-019**: `auth.oidc.issuer_uri` and at least one of `auth.oidc.audience`/`auth.oidc.client_id` MUST be required at startup when and only when `oidc-jwt` is present in `auth.authn.chain`, with an actionable error message otherwise (same conditional-requirement pattern as `static-users`' JWT secret). `client_id` alone MUST NOT be required — gitstore-api is a resource server, and operators who set an explicit `audience` need no client registration identity.

### Key Entities

- **Reference OIDC provider stack**: The optional, separately-deployable set of components (Hydra, Kratos, `gitstore-oidc-bridge`, and their backing datastores) that together act as one possible `issuer_uri` choice for `gitstore-api`'s Phase 7 `OIDCJWTProvider` — one option among any standards-compliant issuer an operator could bring instead.
- **`gitstore-oidc-bridge`**: The new standalone service implementing Hydra's login/consent challenge-resolution contract against the current Kratos session; it renders no end-user UI of its own beyond redirects.
- **Kratos identity**: A GitStore user's identity and traits (email, username) as held by Kratos, the sole identity/session source of truth for the reference stack.
- **Login challenge / consent challenge**: Hydra's own OAuth2/OIDC state objects representing "who is this?" and "does this identity authorize this client for these scopes?", resolved exclusively by `gitstore-oidc-bridge` against Kratos.
- **Registered OAuth2 client(s)**: The Hydra-side client registration(s) that a bring-your-own client application (a storefront, `gitstore-admin`, a CLI, or anything else) uses to drive the Authorization Code + PKCE flow against the reference stack; which application actually performs that flow is each application's own concern and is out of scope for this spec.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of tokens issued by the reference stack for a valid login are accepted by `gitstore-api`'s `oidc-jwt` provider with zero issuer-specific code — configuration (`issuer_uri`, `client_id`) only.
- **SC-002**: 100% of login attempts presenting a valid, current Kratos session complete without ever displaying a user-facing consent screen.
- **SC-003**: 100% of login attempts with no Kratos session are redirected to Kratos's login UI before any login challenge is resolved, with zero instances of a login challenge being accepted without a validated session.
- **SC-004**: 100% of default deployments leave both Hydra's and Kratos's Admin APIs unreachable from outside the deployment's internal network boundary.
- **SC-005**: A fresh checkout can bring the full reference stack from nothing to a completed end-to-end login using the same single-make-target operator workflow shape as the existing optional stacks, with zero manual OAuth2 client registration steps.
- **SC-006**: Zero source changes are required in `gitstore-api`, `gitstore-git-service`, or `gitstore-controller-manager` to adopt, or to stop using, the reference stack.

## Assumptions

- Which client application (a storefront, `gitstore-admin`, a CLI, an automated test harness) actually performs the browser-based Authorization Code + PKCE flow against the reference stack is out of scope for this spec; `gitstore-api` itself is a Resource Server / Relying Party (per Phase 7) and does not perform that flow. This spec is scoped to standing up the issuer and its login/consent bridge, not to wiring any specific client application to use it.
- Role/group derivation for OIDC-authenticated principals (i.e., how `Principal.Roles`/`Principal.Groups` get populated for a Kratos-backed identity, for `rbac-local` or future `opa` authorization decisions) is not defined by this spec. Kratos has no built-in roles concept; `Principal.Claims` carries through whatever Kratos traits this spec maps, and any role-mapping layer on top of that remains a separate, future concern.
- This spec adopts the architecture decision recorded in the Clarifications above and detailed in `research.md` (Ory Hydra + Ory Kratos, standalone `gitstore-oidc-bridge`) as binding; it does not re-run that comparison.
- Multi-instance/HA topologies for Hydra and Kratos themselves (beyond what a single-node reference deployment needs) are out of scope; this is a reference stack for "just works," not a production-hardened, horizontally-scaled identity platform.
- Secret rotation procedures for Hydra's/Kratos's own secrets (system, cookie, cipher, database credentials) follow the same `.env`-driven, never-committed convention already used by the existing optional stacks' secrets, and do not require new tooling beyond what this spec's `quickstart.md` documents.

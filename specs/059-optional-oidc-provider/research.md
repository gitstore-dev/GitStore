# Research: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

## 1. Architecture: Ory Hydra + Kratos ("Approach B") vs. Dex + Oathkeeper + Kratos ("Approach A")

**Decision**: Approach B — Ory Hydra as the OAuth2/OIDC provider, Ory Kratos as the identity/session source of truth, bridged by a custom app-owned login/consent integration.

**Rationale**, in the order these considerations were weighed:

1. **Refresh-token capability is not equivalent between the two providers, and this asymmetry matters more for GitStore than it would elsewhere.** The side-by-side experiment (`juliuskrah/experiments` PR #1, `oidc/specs/COMPARISON.md`) found by direct source inspection of `dexidp/dex` v2.44.0 (`connector/authproxy/authproxy.go`, `server/oauth2.go`) that Dex's server only issues a refresh token when the active connector implements `connector.RefreshConnector` — and the `authproxy` connector (the one Approach A needs to bridge a pre-verified Kratos session into Dex) never implements that interface. This is not a configuration flag; it is confirmed further by live testing (a real `/token` response from Dex with no `refresh_token` field at all). Approach A compensates with **silent re-authorization**: replaying the full Authorization Code + PKCE flow server-to-server, forwarding the browser's Kratos session cookie, on every renewal. Hydra (Approach B) has no such limitation — it is a general-purpose, standards-compliant OAuth2/OIDC provider (via `ory/fosite`), not a connector-based identity broker, and issues a real, rotating `refresh_token` for the `offline_access` scope.
2. **GitStore has no independent session layer to absorb a weak IdP refresh story.** `gitstore-api`'s own `refreshToken` GraphQL mutation already returns `ErrNotSupported` for OIDC-authenticated sessions (`docs/implementation/020-pluggable_auth_architecture.md` §7 Phase 3d) — by design, GitStore does not attempt to compensate for whatever refresh behavior the configured IdP does or does not offer. In a system that *did* manage its own session/refresh layer independently of the IdP, Approach A's silent-reauthorization workaround might be an acceptable implementation detail hidden behind that layer. GitStore has no such layer, so the IdP's own refresh capability is directly load-bearing on whatever a client application built against `gitstore-api` can do — making this the deciding factor, not a secondary one.
3. **Fewer moving parts.** Approach B needs two services (Hydra, Kratos); Approach A needs three (Dex, Oathkeeper, Kratos), since Dex's `authproxy` connector cannot itself turn a Kratos session into a trusted identity — it requires Oathkeeper in front of it to translate a verified Kratos session into the pre-authenticated headers `authproxy` trusts. For something explicitly framed as the "optional, bring-your-own-alternative for anyone who doesn't want to bring their own IdP," minimizing operational surface area is itself a design goal — the audience choosing this path is, by construction, the audience least interested in operating more infrastructure.
4. **Hydra's login/consent bridge is Hydra's officially designed integration model, not a workaround.** Every Hydra deployment implements app-owned `/login` and `/consent` routes — this is how Hydra is meant to be integrated, since it has no user store of its own by design. Approach A's silent-reauthorization mechanism, by contrast, exists specifically to route around a hard limitation of Dex's `authproxy` connector; it is a workaround for a gap, not a designed integration point.

**Counter-consideration explicitly weighed and deferred, not overlooked**: Dex's connector model is the stronger choice if GitStore anticipates needing to federate multiple upstream identity sources (LDAP, GitHub, Google, SAML, etc.) *beyond* Kratos — each connector plugs in with no bridge-route code of its own. The user's explicit direction is that Kratos is the first-class supported directory now, with "other user directory stores... supported in the future based on user demand" — i.e., multi-directory federation is a real *possible* future requirement, not a current one. It does not outweigh points 1–4 today. **If and when multi-directory federation becomes an actual requirement, this decision should be revisited** — Dex's connector model would become the more attractive choice at that point, and Approach A's refresh-token limitation would need to be re-evaluated against whatever federation need drove the reconsideration (e.g., it may no longer be Dex-vs-Hydra at all, but a different broker entirely).

**Alternatives considered**:
- *Approach A (Dex + Oathkeeper + Kratos)*. Rejected for the reasons above — real, standards-compliant refresh tokens matter more given GitStore's `ErrNotSupported` refresh design, and the reference stack should minimize operational surface area for its target audience.
- *Neither — require every operator to bring their own IdP, no reference implementation at all*. Rejected by the project owner in the brainstorm that produced this spec: the explicit goal is a "bring your own, but we also ship a usable default" pattern, already established for `gitstore-admin`.

## 2. Login/consent bridge placement: standalone service vs. folded into `gitstore-admin`

**Decision**: A new, standalone, minimal service — `gitstore-oidc-bridge` — implementing Hydra's `/login` and `/consent` routes. Not folded into `gitstore-admin`.

**Rationale**:

1. **The bridge is required infrastructure, not an admin-console feature.** Without it, Hydra cannot complete any login at all — it has no user store and must be told, on every login and consent decision, who the user is and whether to proceed. This is fundamentally different in kind from `gitstore-admin`'s own purpose (a reference UI for managing namespaces/repositories/catalog resources); the bridge renders no end-user-facing screens of its own beyond redirects, and has no reason to share `gitstore-admin`'s UI framework, build pipeline, or deployment lifecycle.
2. **`gitstore-admin`'s own future is explicitly uncertain.** Per project history, `gitstore-admin` (Astro-based) is paused and has drifted from the backend, with a full framework rewrite under consideration before further investment — not scheduled, not designed, not committed to a target stack. Coupling infrastructure the reference OIDC stack cannot function without to a component in that state would make the OIDC stack's own stability hostage to `gitstore-admin`'s unresolved rewrite question.
3. **Both are separately optional, and must stay independently so.** `gitstore-admin` is itself bring-your-own — an operator may deploy no admin UI at all, a different one entirely, or `gitstore-admin` unmodified. If the login/consent bridge lived inside `gitstore-admin`, choosing not to deploy `gitstore-admin` (a legitimate, expected choice) would silently break the reference OIDC stack for anyone still using it, which contradicts both components' "optional and orthogonal" design intent.
4. **Consistency with the existing top-level service convention.** Every deployable GitStore component is its own top-level `gitstore-<name>/` directory with its own compose service and (where relevant) its own optional compose overlay — `gitstore-api`, `gitstore-git-service`, `gitstore-controller-manager`, `gitstore-admin`. A new `gitstore-oidc-bridge/` directory, its own `compose.oidc.yml` entry, and its own `docker/oidc-bridge.Dockerfile` follow that convention directly, mirroring `gitstore-admin`'s own `compose.admin.yml`/`docker/admin.Dockerfile` precedent for "an optional component gets its own everything."

**Alternatives considered**:
- *Fold `/login`/`/consent` routes into `gitstore-admin`*. Rejected for the reasons above — it would make required OIDC-stack infrastructure depend on an optional, currently-paused frontend's deployment and future rewrite timeline.
- *Fold the bridge into `gitstore-api` itself*. Rejected — `gitstore-api` is a core, non-optional component (per the project's stated constraint that "everything is optional except api, git-service, and controller-manager"); the bridge is specific to one optional identity-provider choice among several an operator could bring, and does not belong inside a core service's surface area. It would also make `gitstore-api` depend on Hydra/Kratos client libraries it has no other reason to carry.

## 3. Bridge implementation language and HTTP framework

**Decision**: Go, using the same minimal-dependency HTTP approach already established in this codebase (the existing services use `net/http`/`gin-gonic/gin`, `go.uber.org/zap`, and Viper-driven config); no new language is introduced.

**Rationale**: The reference experiment's bridge routes were implemented in Next.js/TypeScript because that experiment's actual subject was a full browser session UX (cross-tab sync, encrypted session cookies, `iron-session`) that GitStore does not need here — `gitstore-oidc-bridge` renders no UI of its own; it only issues redirects and calls two admin APIs (Hydra's, Kratos's). Introducing a second backend language into GitStore's deployable surface for a service this narrow would be inconsistent with the codebase's existing Go/Rust split (Go for control-plane services, Rust for the git data plane) for no corresponding benefit.

**Alternatives considered**:
- *Reuse the experiment's Next.js implementation directly, ported into `gitstore-oidc-bridge`*. Rejected — pulls in a Node/React toolchain for a component that needs no rendering, and would be the only Node-based backend service in the deployable stack.

## 4. Client library for the Hydra Admin API and Kratos API

**Decision**: Use the official `github.com/ory/client-go` generated SDK for both Hydra's Admin API (`/admin/oauth2/auth/requests/login`, `/admin/oauth2/auth/requests/consent`) and Kratos's public (`/sessions/whoami`) and admin (`/admin/identities/{id}`) APIs, rather than hand-rolled JSON request/response structs.

**Rationale**: This mirrors the precedent already set in spec 039 (`gitstore-controller-manager`'s GraphQL client), where a well-defined external API surface got a proper client dependency rather than bespoke HTTP+JSON plumbing, specifically to avoid drift against that API's own versioned contract. Hydra's and Kratos's Admin APIs are versioned alongside their respective releases; a maintained SDK tracks that automatically.

**Alternatives considered**:
- *Hand-rolled `net/http` + `encoding/json` calls against the two Admin APIs*. Rejected — for a service this narrow it would be less code, but it re-implements request/response typing the official SDK already provides, and is more exposed to silent breakage on a Hydra/Kratos version bump.

## 5. Kratos identity schema and claims mapping onto `gitstore-api`'s `Principal`

**Decision**: The Kratos identity schema's `traits` carry, at minimum, `email` (unique, used as the Kratos self-service login identifier) and `username`. The bridge sets the Hydra login challenge's `subject` to the Kratos identity's own stable `id` (a UUID), not to the mutable `email` trait — so the OIDC `sub` claim, and therefore `Principal.Subject`, is a stable identifier. `email`/`username` traits are surfaced as ID token claims and land in `Principal.Claims`.

**Rationale**: `gitstore-api`'s `Principal.Subject` is documented and used elsewhere (the JWT `sub` claim, spec 031) as a stable, non-reassignable identifier — using a Kratos identity's own UUID for it is the direct analogue for an OIDC-authenticated principal, and avoids the well-known pitfall of keying identity off a mutable email address that a user could later change.

**Alternatives considered**:
- *Use the `email` trait as the subject*. Rejected — email is mutable in Kratos's self-service flows; using it as `sub` would break the stability `Principal.Subject` is expected to have.

## 6. `offline_access` / refresh-token scope handling

**Decision**: The reference stack's registered OAuth2 client(s) support the `offline_access` scope and Hydra's standard refresh-token grant, rotating the refresh token on each use, matching Approach B's confirmed capability (Decision 1). `gitstore-api` itself never calls this grant — consistent with Phase 3d's existing `ErrNotSupported` behavior for OIDC sessions — so any refresh-time re-validation of the underlying Kratos session (per the experiment's finding that a successful Hydra refresh does not by itself prove the Kratos session is still valid) is the responsibility of whichever client application performs the refresh, not of `gitstore-api` or `gitstore-oidc-bridge`.

**Rationale**: This is the direct, intentional consequence of choosing Approach B and of Phase 3d's already-established scope boundary; documenting it here prevents anyone from mistaking the Kratos-session-re-check nuance for a gap this spec introduces, when it is in fact an existing, explicitly out-of-scope boundary this spec simply inherits.

**Alternatives considered**:
- *Have `gitstore-oidc-bridge` intercept and re-validate every refresh-token grant against Kratos*. Rejected — Hydra's token endpoint, not the bridge, handles the refresh grant directly (the bridge is only in the login/consent path, not the token path); inserting the bridge into the token path would require proxying Hydra's public token endpoint, which is a materially larger change with no requirement driving it in this spec.

## 7. Network exposure of Hydra's and Kratos's Admin APIs

**Decision**: Both Admin APIs are published only on the deployment's internal Docker network, reachable by `gitstore-oidc-bridge` (itself a Docker Compose service in GitStore's deployment, unlike the reference experiment's host-run Next.js app) and other in-network components — never on the network(s) the browser or any external caller can reach.

**Rationale**: This is the same trust boundary the reference experiment already established (Admin APIs published loopback-only, not `0.0.0.0`, reachable only by the app performing the bridge role) — reused unchanged because the underlying threat model (an Admin API can approve arbitrary logins/consents and read identity traits) is identical. GitStore's bridge runs inside the Compose network rather than on the host, so the concrete mechanism is an internal-only Docker network rather than the experiment's loopback-only port publish, but the boundary it enforces is the same.

**Alternatives considered**:
- *Publish Admin APIs to the host loopback only, matching the experiment exactly, and run the bridge on the host too*. Rejected — the experiment's host-run app existed because a Next.js dev server and Hydra's issuer URL both needed to resolve as `localhost` identically for the browser and for server-side OIDC discovery. `gitstore-oidc-bridge` has no such constraint (it does no browser-facing OIDC discovery of its own), so it belongs inside the Compose network like every other GitStore service, with only its own `/login`/`/consent` HTTP port published for Hydra to reach.

## 8. Datastore for Hydra and Kratos

**Decision**: Hydra and Kratos each run their own dedicated Postgres instance within `compose.oidc.yml`, exactly as in the reference experiment. Neither uses GitStore's own `datastore.Datastore` abstraction (`go-memdb`/ScyllaDB).

**Rationale**: Hydra and Kratos are foreign, self-contained systems with their own first-party SQL migrations and schema ownership; there is no benefit to routing their storage through GitStore's own catalog-oriented datastore abstraction, which is designed around GitStore's own resource kinds (Namespace, Repository, Product, etc.), not around an external identity provider's internal schema.

**Alternatives considered**:
- *Point Hydra/Kratos at the same Postgres/Scylla instance GitStore itself might use in some deployment*. Rejected — GitStore's production datastore target is ScyllaDB, which neither Hydra nor Kratos support; each requires its own supported database regardless.

## 9. Federation beyond Kratos

**Decision**: Out of scope for this spec. Kratos is the only identity directory this spec integrates.

**Rationale**: Per the user's explicit direction recorded in this spec's Clarifications, other directories (LDAP, GitHub, Google, etc.) are future work driven by actual demand, not a requirement today. Building federation support speculatively would be exactly the kind of premature complexity the project's Simplicity/YAGNI principle exists to prevent.

**Alternatives considered**:
- *Design the bridge/Hydra client registration to be federation-ready now (e.g., adopting Dex as a broker in front of Hydra)*. Rejected — this would reintroduce Dex (and its own refresh-token limitation, Decision 1) for a capability nothing currently requires; if federation becomes a real requirement, Decision 1 itself should be revisited from scratch at that time, not preemptively hedged against now.

## 10. No `NEEDS CLARIFICATION` remains

All architectural unknowns for this spec were resolved either in the pre-spec brainstorming session (recorded in `spec.md`'s Clarifications) or in this research pass.

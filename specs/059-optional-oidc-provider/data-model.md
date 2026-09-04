# Data Model: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

None of this spec's entities live in `gitstore-api`'s `datastore.Datastore` abstraction — they are owned entirely by Hydra and Kratos, foreign systems with their own schemas. This document models them only to the depth needed to define the bridge's behavior and the claims-mapping contract onto `gitstore-api`'s existing `Principal` type.

## Kratos identity schema (`traits`)

New file: `deploy/oidc/kratos/identity.schema.json` (a Kratos identity JSON Schema, referenced from `kratos.yml`'s `identity.schemas` list).

| Trait | Type | Constraints | Purpose |
|---|---|---|---|
| `email` | `string`, `format: email` | Required; unique; Kratos self-service login/recovery identifier | Primary account identifier for Kratos's own flows; surfaced as the OIDC `email` claim |
| `username` | `string` | Required; `minLength: 3` | Display/handle identifier; surfaced as the OIDC `preferred_username` claim |

Kratos's own identity `id` (a server-generated UUID, not a trait) is the value the bridge uses as the Hydra login challenge's `subject` — see "Claims mapping" below.

Deliberately excluded from `traits` in this spec: roles, groups, tenant/namespace affiliation. Kratos has no built-in roles concept, and `Principal.Roles`/`Principal.Groups` derivation for OIDC-authenticated principals is out of scope (per `spec.md`'s Assumptions) — a future spec may add a `gitstore` metadata namespace to the schema (Kratos supports `metadata_public`/`metadata_admin` alongside `traits`) if role-mapping from Kratos becomes a requirement.

## Claims mapping: Kratos identity → OIDC ID token claim → `gitstore-api` `Principal` field

`Principal` (`gitstore-api/internal/auth/types.go`) is unchanged by this spec; this table is the contract the future `OIDCJWTProvider` (Phase 7) already needs regardless of which issuer is configured, made concrete for this specific issuer choice.

| Kratos source | OIDC ID token claim | `Principal` field | Notes |
|---|---|---|---|
| Identity `id` (UUID) | `sub` | `Subject` | Set by the bridge as the Hydra login challenge's `subject`; Hydra places it in `sub` verbatim. Stable — never the mutable `email` trait. |
| Reference stack's Hydra issuer URL | `iss` | `Issuer` | Standard OIDC discovery-derived value; identical in shape to any other issuer Phase 7 already supports. |
| `traits.email` | `email` | `Claims["email"]` | Populated by the bridge's consent-challenge `session.id_token` payload from a Kratos Admin API identity lookup. |
| `traits.username` | `preferred_username` | `Claims["preferred_username"]` | Same source as above. |
| Granted OAuth2 scopes | `scope` | `Scopes` | Standard OAuth2 token introspection/claims behavior; unchanged from how Phase 7 already treats `scope` for any issuer. |
| — | — | `Groups`, `Roles`, `Tenant`, `Namespace` | Not populated by this spec; remain empty for a Kratos-backed principal unless a future spec adds them. |
| — | `jti` | `TokenID` | Hydra-issued tokens carry their own `jti`; unchanged handling from Phase 7's existing `TokenID` extraction. |

## Login challenge state machine (`GET /login`)

```
Hydra issues login_challenge (client began Authorization Code flow)
                    │
                    ▼
   gitstore-oidc-bridge: GET /admin/oauth2/auth/requests/login?login_challenge=...
                    │
                    ▼
   forward browser's Kratos session cookie to Kratos's /sessions/whoami
                    │
        ┌───────────┴───────────┐
        │                       │
   valid session            no/expired session
        │                       │
        ▼                       ▼
  accept login challenge   redirect browser to Kratos's self-service
  (subject = Kratos         login UI, with return_to = this /login URL
   identity id)                   │
        │                         ▼
        ▼                (user completes Kratos login,
  Hydra redirects browser  browser returns here with the
  to its own consent flow  same login_challenge)
                                   │
                                   └──────────────► (loop back to the top;
                                                      session now valid)
```

## Consent challenge state machine (`GET /consent`)

```
Hydra issues consent_challenge (login challenge already accepted)
                    │
                    ▼
   gitstore-oidc-bridge: GET /admin/oauth2/auth/requests/consent?consent_challenge=...
                    │
                    ▼
   look up the authenticated identity's traits via Kratos's Admin API
   (/admin/identities/{id}, id = the subject from the login challenge)
                    │
                    ▼
   intersect requested scopes with the registered OAuth2 client's
   permitted scopes (openid, profile, email, offline_access)
                    │
                    ▼
   accept consent challenge, no user-facing screen:
     grant_scope   = the intersected scope set
     session.id_token.email               = traits.email
     session.id_token.preferred_username  = traits.username
                    │
                    ▼
   Hydra redirects browser back to the client with an authorization code
```

## Registered OAuth2 client(s)

| Field | Value |
|---|---|
| `client_id` | Configurable per deployment (e.g. `gitstore`); not hardcoded to any specific bring-your-own client application |
| `grant_types` | `authorization_code`, `refresh_token` |
| `response_types` | `code` |
| `scope` | `openid profile email offline_access` |
| `token_endpoint_auth_method` | `client_secret_post` (matches the reference experiment; revisit if a public/PKCE-only client is needed later) |
| `redirect_uris` | Configurable per deployment — whichever bring-your-own client application drives the flow supplies its own callback URL(s) |

Registration is performed by a one-shot, idempotent startup step (mirroring the reference experiment's `hydra-client-setup` service): check whether the client already exists via Hydra's Admin API before attempting to create it, so a repeated `compose up` against an already-provisioned Hydra is a no-op.

## Compose network topology

Mirrors the reference experiment's `public`/`internal` network split, adapted to GitStore's existing `gitstore-network` (all core services already share one bridge network per `compose.yml`):

| Component | Reachable from browser/public | Reachable from `gitstore-network` (internal) |
|---|---|---|
| Hydra public API (`/oauth2/*`, `/.well-known/*`) | Yes (published port) | Yes |
| Hydra Admin API (`/admin/*`) | **No** | Yes — `gitstore-oidc-bridge` only |
| Kratos public API (`/self-service/*`, `/sessions/whoami`) | Yes (published port) | Yes |
| Kratos Admin API (`/admin/identities/*`) | **No** | Yes — `gitstore-oidc-bridge` only |
| `gitstore-oidc-bridge` (`/login`, `/consent`, `/healthz`) | Only via Hydra's redirects (browser is redirected *to* it, but it makes outbound Admin API calls itself — see `contracts/oidc-bridge-routes.md`) | Yes |
| Hydra/Kratos Postgres | **No** | Yes — Hydra/Kratos and their own migration jobs only |

## Relationship to Phase 7 (`docs/implementation/020-pluggable_auth_architecture.md` §7)

- Phase 7's `OIDCJWTProvider` config schema (§5a) already reserves `auth.oidc.issuer_url`, `auth.oidc.client_id`, `auth.oidc.audience`, `auth.oidc.clock_skew` — unchanged by this spec. This spec's Hydra issuer is simply one concrete value an operator may set `GITSTORE_AUTH__OIDC__ISSUER_URL` to.
- Phase 7's Risk 1 (clock skew) and Risk 2 (JWKS rotation window) mitigations are unchanged and apply identically regardless of which issuer is configured, including this one.
- This spec introduces no new fields, methods, or config keys on the `gitstore-api` side — every entity in this document is external to `gitstore-api`'s own data model.

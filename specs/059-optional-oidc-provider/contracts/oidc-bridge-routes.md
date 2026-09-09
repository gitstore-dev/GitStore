# Contract: `gitstore-oidc-bridge` HTTP Routes

`gitstore-oidc-bridge` is a new, standalone, minimal Go service. It implements exactly the routes Hydra's login/consent integration model requires, plus a health check. It renders no end-user-facing UI beyond HTTP redirects.

## `GET /login`

Hydra redirects the browser here with a `login_challenge` query parameter whenever a client begins the Authorization Code flow and no existing Hydra login session covers it.

| Step | Action |
|---|---|
| 1 | Fetch the login request from Hydra Admin API: `GET {HYDRA_ADMIN_URI}/admin/oauth2/auth/requests/login?login_challenge={login_challenge}` |
| 2 | Forward the browser's Kratos session cookie to `GET {KRATOS_PUBLIC_URI}/sessions/whoami` |
| 3a | **Session valid**: `PUT {HYDRA_ADMIN_URI}/admin/oauth2/auth/requests/login/accept` with `subject` = the Kratos identity's `id`, `remember=true`. Redirect the browser to the `redirect_to` URL Hydra returns. |
| 3b | **Session invalid/absent**: redirect the browser to Kratos's self-service login UI (`{KRATOS_PUBLIC_URI}/self-service/login/browser?return_to={this /login URL with the same login_challenge}`). |

**Error handling**: any Hydra Admin API or Kratos API failure in step 1 or 2 results in `PUT .../login/reject` with an `error`/`error_description`, and the browser is redirected to Hydra's returned `redirect_to` (which carries the client back to its own error-handling path) — the bridge never surfaces a raw 500 to the browser once a `login_challenge` has been accepted for processing.

## `GET /consent`

Hydra redirects the browser here with a `consent_challenge` query parameter after a login challenge has been accepted.

| Step | Action |
|---|---|
| 1 | Fetch the consent request from Hydra Admin API: `GET {HYDRA_ADMIN_URI}/admin/oauth2/auth/requests/consent?consent_challenge={consent_challenge}` |
| 2 | Read `subject` from the consent request (the Kratos identity `id` set during `/login`) and look up its traits: `GET {KRATOS_ADMIN_URI}/admin/identities/{subject}` |
| 3 | Intersect `requested_scope` with the registered client's permitted scopes (`openid profile email offline_access`) |
| 4 | `PUT {HYDRA_ADMIN_URI}/admin/oauth2/auth/requests/consent/accept` with `grant_scope` = the intersected set, `grant_access_token_audience` = the request's requested audience, `remember=true`, and `session.id_token` populated per `data-model.md`'s claims mapping (`email`, `preferred_username`) |
| 5 | Redirect the browser to the `redirect_to` URL Hydra returns |

**No user-facing consent screen**: this route never renders an HTML page asking the user to approve scopes — the registered OAuth2 client(s) in this reference stack are first-party by construction (FR-006), so consent is granted automatically once the requested scopes are validated against the client's registration.

**Error handling**: same shape as `/login` — any failure results in `PUT .../consent/reject`, never a raw 500 once a `consent_challenge` has been accepted for processing.

## `GET /health`

Returns `200 OK` once the bridge can reach both Hydra's and Kratos's Admin APIs (a lightweight upstream check, not a full request replay). Used by `compose.oidc.yml`'s `healthcheck` for the `oidc-bridge` service, mirroring the `wget --spider` healthcheck pattern already used by `api`, `controller-manager`, and `admin` in the existing compose files.

## Network exposure

Only the bridge's own listen port (`/login`, `/consent`, `/health`) is reachable from outside `gitstore-network` (needed because Hydra issues browser redirects to it). The bridge's *outbound* calls (to Hydra's and Kratos's Admin APIs) stay entirely within `gitstore-network` — see `data-model.md`'s network topology table.

## Configuration (Viper, mirroring `gitstore-api`'s config pattern)

| Key path                           | Env var                                            | Required                                                                                                                                                                                 |
|------------------------------------|----------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `bridge.listen_port`               | `GITSTORE_OIDC_BRIDGE__LISTEN_PORT`                | No (default `4445`, matching the reference experiment's Hydra Admin port for familiarity — not a collision since it's the bridge's own listen port, not Hydra's)                         |
| `bridge.hydra.admin_uri`           | `GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URI`           | Yes                                                                                                                                                                                      |
| `bridge.hydra.oauth2_client_scope` | `GITSTORE_OIDC_BRIDGE__HYDRA__OAUTH2_CLIENT_SCOPE` | No (default `openid profile email offline_access`)                                                                                                                                       |
| `bridge.hydra.default_audience` | `GITSTORE_OIDC_BRIDGE__HYDRA__DEFAULT_AUDIENCE` | No (default empty = requested-audience-only; set to the client_id so generic clients that send no `audience` authorize param still receive an `aud`-bearing access token — gitstore-api's `oidc-jwt` provider enforces `aud`) |
| `bridge.kratos.public_uri`         | `GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_URI`         | Yes                                                                                                                                                                                      |
| `bridge.kratos.public_browser_uri` | `GITSTORE_OIDC_BRIDGE__KRATOS__PUBLIC_BROWSER_URI` | No (default: `bridge.kratos.public_uri`; set it when the in-network Kratos URL differs from the browser-reachable one, e.g. `http://kratos:4433` vs. `http://localhost:4433` in Compose) |
| `bridge.kratos.admin_uri`          | `GITSTORE_OIDC_BRIDGE__KRATOS__ADMIN_URI`          | Yes                                                                                                                                                                                      |

This config namespace (`GITSTORE_OIDC_BRIDGE__...`) is entirely new and additive — it does not touch `gitstore-api`'s existing `GITSTORE_AUTH__...` schema (§5a of the auth architecture doc), since the bridge is not part of `gitstore-api`.

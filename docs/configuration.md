# Operator Configuration Reference

This guide covers settings used to deploy and operate `gitstore-api`,
`gitstore-git-service`, and `gitstore-controller-manager`. Use the shipped
service configuration examples as starting points and inject secrets through
your deployment platform. Configuration is grouped by owning service:
`[git_service]` is owned by `gitstore-git-service`, `[api]` by `gitstore-api`,
and `[controller]` by `gitstore-controller-manager`. `[log]`, `[grpc_auth]`,
and `[push_limits]` are shared by the services that use them.

## Telemetry export

Telemetry is opt-in for the three core services. Set
`observability.otlp.uri` (environment: `GITSTORE_OBSERVABILITY__OTLP__URI`) to
the **collector's OTLP/HTTP base URI**, for example `http://otel-collector:4318`.
An empty URI disables all exporters and leaves local logging and existing API/
controller diagnostic metrics endpoints unchanged. Credentials, query strings
and fragments are rejected in the URI. Do not configure applications with
Prometheus, Loki or Tempo addresses.

For local evaluation, `make compose OBSERVABILITY=otel` starts the collector
and local backends. In production, deploy and secure your own collector and
backends; applications should send telemetry only to the collector. The Git
service exposes metrics through its gRPC telemetry export, not an HTTP metrics
listener. Do not configure a Git-service metrics port or scrape target.

Export uses bounded in-memory queues and may drop telemetry during prolonged
collector or backend outages. Monitor collector export failures and dropped
records. The local Compose stack is for development; it does not provide
production dashboards, alerts, or host/database monitoring.

`observability.otlp.trace_sample_ratio`
(`GITSTORE_OBSERVABILITY__OTLP__TRACE_SAMPLE_RATIO`) defaults to `0.05` and
accepts values from `0` to `1`.

---

## Grammar: durations and sizes

Types in the tables use language-neutral labels: `boolean`, `integer`,
`number`, `string`, `string list`, `duration`, and `IEC size string`.

Every duration-valued key accepts a string restricted to a single magnitude with
one of the units `ms`, `s`, `m`, or `h` (for example `"30s"`, `"500ms"`,
`"24h"`). Compound forms (`"1h30m"`) and other units (`ns`, `us`, `d`, `w`) are
rejected at startup. This grammar is identical across `gitstore-api`,
`gitstore-controller-manager`, and `gitstore-git-service`.

Every size-valued key (`push_limits.max_pack_size`, `push_limits.max_file_size`)
accepts a string restricted to IEC binary units: `B`, `KiB`, `MiB`, `GiB`, or
`TiB` (for example `"512MiB"`, `"100MiB"`). Decimal units (`KB`, `MB`, `GB`)
and bare numbers are rejected at startup — size values are never raw byte
integers.

---

## Source Precedence

Services load configuration from multiple sources in a fixed order. A higher-priority source overrides a lower-priority one:

```
1. Hard-coded defaults          (lowest priority)
2. Config file                  (optional)
3. `.env` file                  (optional)
4. Environment variables
5. CLI value overrides such as `--log-level` (highest priority, where supported)
```

Use `.env` files only for local development; inject environment variables
through your deployment platform in production.

All three services accept `--config-file <path>`. The flag selects the file
source; values from the environment still override values in that file. An
explicit path is required to exist and be readable. Without the flag, the Go
services retain optional `config.toml` discovery and the Git service retains
optional `gitstore.toml` discovery in the working directory.

**`gitstore-api` and `gitstore-controller-manager` only**: `--config-file` may
be repeated to layer additive overlays on top of a base file — e.g.
`--config-file base.toml --config-file overlay.toml`. The first occurrence is
read as the base; each one after it is merged on top (later file wins, key by
key), so an overlay only needs to declare the keys it changes rather than
duplicating the whole base file. `gitstore-git-service` does not support this
— it only accepts a single `--config-file`.

Use the maintained per-service examples as starting points:

- [Git service](../gitstore-git-service/config.toml.example)
- [API](../gitstore-api/config.toml.example)
- [Controller manager](../gitstore-controller-manager/config.toml.example)

Mount the selected files through your deployment platform and tune them per
environment.

## Sensitive values

Keys marked **Sensitive** are always logged as `<redacted>` (when set) or
`<unset>` (when absent), regardless of log level. Supply production secrets
through your deployment platform rather than committing them to configuration.

An empty string (`KEY=`) for a **Required** key is treated identically to an absent key and causes a startup failure listing all failing keys.

---

## gitstore-api

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-api --config-file /path/to/config.toml` (required when selected). Repeat the flag to layer overlays: `--config-file base.toml --config-file overlay.toml` (later file wins per key).

**Env var prefix**: `GITSTORE_`

### API Server

| Key                          | Env Var                                 | Type    | Default | Required | Sensitive | Description                                                                 |
|-------------------------------|------------------------------------------|---------|---------|----------|-----------|-------------------------------------------------------------------------------|
| `api.admin_port`                   | `GITSTORE_API__ADMIN_PORT`                    | integer | `4000`  | No       | No        | Admin listener port (1–65535)                       |
| `api.storefront_port` | `GITSTORE_API__STOREFRONT_PORT` | integer | `4001` | No | No | Storefront listener port, distinct from Admin/Git/catalog |
| `api.git_port`               | `GITSTORE_API__GIT_PORT`                | integer | `9000`  | No       | No        | Git Smart HTTP port the API server listens on (1–65535)                     |
| `api.grpc_port`              | `GITSTORE_API__GRPC_PORT`               | integer | `6000`  | No       | No        | CatalogService gRPC port called by gitstore-git-service                     |
| `api.rate_limit.per_second`  | `GITSTORE_API__RATE_LIMIT__PER_SECOND`  | number  | `50`    | No       | No        | Sustained per-client-IP request rate on the Admin listener                   |
| `api.rate_limit.burst`       | `GITSTORE_API__RATE_LIMIT__BURST`       | integer | `100`   | No       | No        | Per-client-IP token-bucket burst size on top of `api.rate_limit.per_second` |

### Endpoint policies

Admin always serves `/admin/graphql` on `api.admin_port`; Storefront always
serves `/storefront/graphql` on `api.storefront_port`. `/graphql` is removed.
Health, readiness and compatibility metrics stay at the Admin listener root.
All four listener ports must be distinct and between 1 and 65535. Remove
`api.port` and `GITSTORE_API__PORT`; their presence now fails startup.

Each endpoint has an independent CORS policy. Empty origin lists allow
same-origin browser use and deny cross-origin browser requests. CORS does not
authenticate non-browser clients. For a playground on the other listener:

```toml
[api.admin.cors]
allowed_origins = ["http://localhost:4001"]
allow_credentials = false
allowed_headers = ["Authorization", "Content-Type"]
exposed_headers = ["X-Request-Id"]
max_age = "10m"

[api.storefront.cors]
allowed_origins = ["http://localhost:4000"]
allow_credentials = false
```

Both policies support the same fields. Environment names follow the nested
configuration rule, for example `GITSTORE_API__ADMIN__CORS__ALLOWED_ORIGINS`.
Origins must be HTTP(S) origins; wildcard origins cannot allow credentials.
Admin WebSocket origins use the same policy. The playground switcher requires
an explicit peer URL, keeps separate editor sessions, clears headers and closes
subscriptions when switching, and omits browser credentials on fetches.

Endpoint bounds live under `api.admin.limits` and `api.storefront.limits`.
Defaults: `body_bytes=1048576`, `document_bytes=65536`, `parser_tokens=15000`,
`depth=15`, `aliases=30`, `root_fields=20`, `directives=100`,
`fragment_visits=1000`, `validation_errors=50`, `page_size=250`, `node_ids=50`,
`complexity=1000`, `limiter_keys=10000`, `idle_ttl="10m"`, `deadline="10s"`.
Omitted, null or zero pagination uses the existing 100-item datastore default.
When `page_size` is below 100, request an explicit positive page within that
bound; otherwise validation rejects the operation before datastore access.
`in_flight` defaults to 128 for Admin and 32 for Storefront; admission has no
waiting queue. Admin additionally bounds `ws_connections=1000` and
`ws_operations=20` per principal. Long-lived subscriptions are outside the
per-operation deadline.

Admin's `principal_per_second=50` and `principal_burst=1000` charge calculated
operation cost before execution. Only the authenticated ServiceAccount JWT
identity matching `api.admin.controller_subject` (default
`system:serviceaccount:gitstore-controller-manager`) is exempt from that budget.
Other bounds still apply. `api.rate_limit` is the Admin IP policy (50/s, burst
100); `api.storefront_rate_limit` is independent (200/s, burst 400). Limits are
finite per replica. `api.trusted_proxies` defaults to empty; forwarded client
addresses are trusted only through explicitly configured proxies.

Storefront always returns HTTP 503 with `STOREFRONT_UNAVAILABLE`, no `data`,
and `Cache-Control: no-store` until real publication and target binding exist.
There is no configuration switch to enable it.

### Git Service Connection

| Key                 | Env Var                       | Type   | Default                  | Required | Sensitive | Description                          |
|----------------------|---------------------------------|--------|---------------------------|----------|-----------|---------------------------------------|
| `api.git_service.uri` | `GITSTORE_API__GIT_SERVICE__URI` | string | `dns:///localhost:50051` | Yes      | No        | gRPC address of gitstore-git-service |

### Authentication

| Key                             | Env Var                                     | Type     | Default    | Required | Sensitive | Description                                                 |
|-----------------------------------|------------------------------------------------|----------|------------|----------|-----------|---------------------------------------------------------------|
| `api.auth.static_users.users_file` | `GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE` | string | users.yaml | When selected | No | YAML file containing local users |
| `api.auth.jwt.secret`          | `GITSTORE_API__AUTH__JWT__SECRET`          | string   | —          | When `static-users` is selected | **Yes** | JWT signing key (minimum 32 characters)                  |
| `api.auth.jwt.ttl`             | `GITSTORE_API__AUTH__JWT__TTL`             | duration | `24h`      | No       | No        | JWT token validity                                           |
| `api.auth.jwt.issuer`          | `GITSTORE_API__AUTH__JWT__ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                                       |
| `api.auth.jwt.refresh_grace`   | `GITSTORE_API__AUTH__JWT__REFRESH_GRACE`   | duration | `60s`      | No       | No        | Window after expiry during which `refreshToken` is accepted |

For config files, local users are selected with `[api.auth.static_users]` and `users_file = "users.yaml"`; JWT keys remain nested under `[api.auth.jwt]`.

`static-users` appends `/static-users` to the configured issuer base when
minting tokens. Production logout and refresh rotation require the shared
ScyllaDB revocation table, which is created by schema migration.

### Service-account authentication

Service-account authentication is enabled only when
`api.auth.authn.chain` contains `serviceaccount-assertion` and
`serviceaccount-jwt`. The API issues and verifies the controller's
short-lived access tokens; the controller proves possession of a separately
mounted private key to obtain them.

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `api.auth.authn.chain` | `GITSTORE_API__AUTH__AUTHN__CHAIN` | string list | `["static-users","anonymous"]` | No | No | Ordered AuthN providers; include both service-account providers to enable this flow |
| `api.auth.serviceaccount.issuer` | `GITSTORE_API__AUTH__SERVICEACCOUNT__ISSUER` | string | `gitstore` | No | No | Issuer for service-account access tokens |
| `api.auth.serviceaccount.audience` | `GITSTORE_API__AUTH__SERVICEACCOUNT__AUDIENCE` | string | `gitstore-api` | No | No | Required audience for service-account access tokens |
| `api.auth.serviceaccount.assertion_audience` | `GITSTORE_API__AUTH__SERVICEACCOUNT__ASSERTION_AUDIENCE` | string | `gitstore-api/serviceaccount-token` | No | No | Required audience for controller assertions at the token-exchange endpoint |
| `api.auth.serviceaccount.signing_key` | `GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY` | string | (empty) | **Yes, when a service-account provider is chained** | **Yes** | API-only PEM-encoded Ed25519 or ECDSA P-256 access-token signing key |
| `api.auth.serviceaccount.default_ttl` | `GITSTORE_API__AUTH__SERVICEACCOUNT__DEFAULT_TTL` | duration | `10m` | No | No | Requested default access-token lifetime |
| `api.auth.serviceaccount.max_ttl` | `GITSTORE_API__AUTH__SERVICEACCOUNT__MAX_TTL` | duration | `1h` | No | No | Maximum permitted access-token lifetime |
| `api.auth.serviceaccount.clock_skew` | `GITSTORE_API__AUTH__SERVICEACCOUNT__CLOCK_SKEW` | duration | `2m` | No | No | Allowed JWT clock skew during validation |

Never put `api.auth.serviceaccount.signing_key` in a configuration file mounted by
multiple services. Supply it through an API-only secret mount or environment
injection instead. It is the API issuer key, not the controller's enrollment
key; the controller private key is configured through its `SecretRef` below.

### OIDC authentication (optional)

`api.auth.oidc_jwt` configures a generic, issuer-agnostic OIDC Relying Party
that verifies bearer JWTs via OIDC Discovery + JWKS. It is enabled only when
`oidc-jwt` is present in `api.auth.authn.chain`.

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `api.auth.oidc_jwt.issuer_uri` | `GITSTORE_API__AUTH__OIDC_JWT__ISSUER_URI` | string | (empty) | When `oidc-jwt` is chained | No | OIDC issuer to run discovery against |
| `api.auth.oidc_jwt.client_id` | `GITSTORE_API__AUTH__OIDC_JWT__CLIENT_ID` | string | (empty) | No | No | Registered client id; defaults `audience` to this value |
| `api.auth.oidc_jwt.audience` | `GITSTORE_API__AUTH__OIDC_JWT__AUDIENCE` | string | (empty) | At least one of `audience`/`client_id` required when chained | No | Expected `aud` claim |
| `api.auth.oidc_jwt.clock_skew` | `GITSTORE_API__AUTH__OIDC_JWT__CLOCK_SKEW` | duration | `2m` | No | No | Allowed JWT clock skew during validation |
| `api.auth.oidc_jwt.username_claim` | `GITSTORE_API__AUTH__OIDC_JWT__USERNAME_CLAIM` | string | `sub` | No | No | Claim that becomes `Principal.Subject` for role bindings and audit logs |

### Authorization and user directory

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `api.auth.authz.provider` | `GITSTORE_API__AUTH__AUTHZ__PROVIDER` | string | `rbac-local` | No | No | Active AuthZ provider |
| `api.auth.rbac_local.policy_file` | `GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE` | string | `policy.yaml` | No | No | YAML RBAC policy path, used when `authz.provider = "rbac-local"` |
| `api.auth.userdir.provider` | `GITSTORE_API__AUTH__USERDIR__PROVIDER` | string | `none` | No | No | Active user-directory provider |
| `api.auth.kratos.admin_uri` | `GITSTORE_API__AUTH__KRATOS__ADMIN_URI` | string | (empty) | When `kratos` is selected | No | Private Kratos Admin endpoint |
| `api.auth.kratos.webhook_uri` | `GITSTORE_API__AUTH__KRATOS__WEBHOOK_URI` | string | (empty) | When callbacks are configured | No | Optional private HTTP listener; for example `http://127.0.0.1:8085` |
| `api.auth.kratos.admin_token` | `GITSTORE_API__AUTH__KRATOS__ADMIN_TOKEN` | string | (empty) | Only for an authenticating Admin gateway | **Yes** | Bearer credential; stock self-hosted Kratos does not validate it |
| `api.auth.kratos.max_response_size` | `GITSTORE_API__AUTH__KRATOS__MAX_RESPONSE_SIZE` | IEC size string | `1MiB` | No | No | Positive directory response and webhook body limit, at most `4MiB`; bare numbers and decimal units are rejected |

#### Production identity guidance

Avoid `none` as the UserDir provider in production: it performs no directory
lookup and provides no directory profiles or groups. `static-users` is
intended for local and development deployments; its UserDir support is limited
to basic profile lookup and does not provide directory-managed groups or
account changes. Do not use `static-users` as a production human AuthN or
UserDir provider, either by itself or alongside the OIDC/Kratos identity
setup. In particular, do not chain it alongside `oidc-jwt`, use it as UserDir
for OIDC logins, or use `static-users` AuthN with Kratos UserDir. A mixed
configuration can leave credentials and profile state under different
authorities. Service-account authentication providers are separate and may
still be configured as needed.

For human login with the reference OIDC stack, use `oidc-jwt` for AuthN so
login and directory profiles use the same identity store. The API accepts
`none`, `static-users` and `kratos` as UserDir providers. Kratos currently
provides bounded profile lookup, provider mutations and internal callbacks.
Authenticated Admin/Git HTTP and GraphQL requests resolve trusted directory
profiles before authorization. With Kratos, a human principal with no matching
provider/issuer mapping remains unlinked. If a mapping applies but its identity
is missing or deleted, admission fails closed rather than retaining direct
subject permissions. Missing ServiceAccount mirrors remain unlinked; GitStore
still enforces their authoritative UID and enablement. Directory failures fail
closed without treating valid credentials as invalid. Inactive human profiles
are denied. WebSocket initialization applies the same checks
and caps profile leases below five minutes, never extending the cached
profile's freshness or a shorter token lifetime.

Selecting `static-users` for directory lookup does not require static AuthN
or a JWT signing key; that lookup-only provider cannot authenticate or issue
sessions. Human-administration GraphQL uses the configured directory's supported
capabilities and rejects unsupported operations explicitly.
Successful local directory writes and protected
callbacks evict the process cache and cancel all its authenticated WebSocket
connections, including workloads. Other replicas retain bounded cache/profile
leases; callbacks are not commit notifications.
Successful auth-provider reloads retain invitation delivery on the replacement
directory. The private callback listener dispatches to the active directory,
including its cache invalidation; it returns 404 while Kratos is not selected.

The sample namespace-creator policy binds `system:staff` and requires both
USER tier and `subjectUsername`, which matches the resource name against the
authenticated subject, not the directory username or identity UID. A valid
directory username and a trusted Staff or Customer profile are still required
by the condition; the sample binding grants creation only to Staff. Namespace
creation remains policy-controlled, with immutable USER namespace ownership
validated independently.

See [Configure local authorization](authorization.md) for Role and ClusterRole
bindings, namespace scope, subject matching and `resourceNames` examples.

#### Reference identity schemas and metadata

The optional reference stack uses Kratos, Hydra and the Ory self-service UI
at v26.2.0. New public registrations use the Customer schema. Staff and
ServiceAccount schemas cannot be selected through public registration.
Human username and email are both password identifiers and are unique across
human schemas when enrolled as credentials. The reference directory registers
only the Staff, Customer and ServiceAccount schemas.

The integration uses supported blocking registration/settings hooks and the
Kratos Admin JSON Patch API, without modifying Kratos's database tables.
Registration stamps `metadata_admin.creation_actor` and `update_actor` with
the registering username in the registration write. Profile settings reject
username/schema changes before persistence and preserve protected metadata.
GitStore mutations preserve the creator and stamp the initiating actor using
the normal resource-owner subject encoding. Missing creator metadata falls
back to the represented identity's canonical username, not the reader.

Enable callbacks only together with `api.auth.userdir.provider = "kratos"`
and the private `api.auth.kratos.webhook_uri` listener. For the container
reference, use `http://0.0.0.0:8085` and set
`KRATOS_CONFIG_FILES=/etc/config/kratos/kratos.yml,/etc/config/kratos/gitstore-hooks.yml`
as shown in [the environment example](../config/oidc/oidc.env.example).
The additional Kratos configuration retains the password-session hook and
selects HTTP mail delivery. Enable the development notification adapter and
inject its shared credential at the same time, as described below.
Standalone `make oidc` sends no GitStore callbacks by default.

Do not publish the callback port or route it through public ingress.
Wildcard binding is intended only for an isolated container network; use a
private or loopback address on a host and restrict network access to trusted
Kratos callers. The receiver trusts that network boundary, not public session
credentials. A configured listener returns **404** when Kratos UserDir is
disabled; remove the sender configuration when disabling the provider.

**Limitations**

| Area | Supported boundary and limitation |
|---|---|
| Staff revisions | GitStore/profile writes advance generation for author-owned changes and resourceVersion for integrated writes. These are best-effort: concurrent replicas can lose increments and overwrite changes. Neither the observed-version check nor Admin PATCH is database CAS; v26.2.0 rejects PATCH `test`. |
| Customer revisions | Generation is `-1` and resourceVersion is `"-1"`. Neither is a concurrency token. |
| Customer groups | `customer_groups` defines mandatory memberships for every Customer, not an allowlist for individual edits. Protected identity metadata cannot override them. Individual removal and assignment outside the configured groups are rejected. |
| Coverage and setup | Counters do not track every credential, verification-address or out-of-band Admin change. The reference callback covers profile settings and completed password settings, not every self-service method. New Staff remain unfinished until the blocking password-settings callback records setup completion; verification or administrative reactivation alone cannot bypass setup. |
| Identifier enrollment | Credentialless Staff creation and password-identifier reservation are separate native writes. Failed reservation attempts bounded cleanup of the newly created identity; failed cleanup can leave an unreserved identity. There is no cross-operation transaction. |
| Invitations | A configured delivery provider is required before creating an invitation. Identity creation, recovery-link creation and delivery are not atomic: delivery failure can leave an identity or changed revision. Resend is limited to Staff awaiting setup/verification. Private setup links are delivered only in mail, never returned in directory profiles. |
| Lifecycle and sessions | Suspension/deactivation writes state and then revokes native sessions. Revocation failure can leave the state change applied. Reactivation preserves unfinished setup/verification and does not restore revoked sessions. Other API replicas still enforce their bounded profile/connection freshness rather than immediate global revocation. |
| Privileged Admin access | Direct Kratos Admin writes bypass hooks and can change usernames, schemas or protected metadata. The workload schema alone does not prohibit Admin-created credentials. Keep Admin access private; GitStore does not create workload credentials and remains authoritative for ServiceAccount UID, enablement and keys. |
| Availability | Callback failures abort the protected registration/profile-settings flow. API or directory outages can therefore prevent those flows; callbacks are not a durable event consumer. |
| Ownership and freshness | Username fallback does not reconstruct a missing creator audit record. Hook responses precede persistence; local cache invalidation is not a commit notification, and other replicas retain their bounded cache TTL. |
| Directory cursors | Actor cursors are bound to the configured Admin endpoint. Replicas serving one logical directory must use the same stable Admin URI; changing it invalidates existing cursors. |

### Workload directory mirrors

With Kratos UserDir selected, each API consumes live ServiceAccount events from
the shared internal resource journal. Scylla publishes full-image transitions
only after enrollment completes; MemDB publishes equivalent committed events.
Native ServiceAccount writes do not wait for Kratos.

Mirrors are linked by immutable GitStore UID, contain only source traits and
protected metadata, and are created without login credentials or
verification/recovery addresses. Updates preserve directory groups. GitStore
remains authoritative for UID, keys and enablement; name reuse never transfers
the previous account's groups.

Delivery uses a 64-event buffer, three attempts per event and bounded request
deadlines, with 100 ms and 200 ms retry delays. Overflow and exhausted events
are discarded, not stored in another queue. Startup and provider reload begin
at the current journal boundary; retention/epoch discontinuities also resume
at the current boundary. There is no persisted consumer position, replay,
inventory scan or automatic repair. Missing, stale or orphaned mirrors can
remain indefinitely, and unified actor lists can be incomplete.

On a fresh Scylla deployment, consumer initialization waits for the producer to
initialize the journal, using bounded reads and cancellable polling. It then
captures the current boundary, not a historical catch-up position. Directory
delivery does not prevent API startup or become a journal readiness guarantee.

SDK metrics report application attempts, delivery outcomes, duration and buffer
depth without identity labels. These are best-effort consumer signals, not
durable producer progress or acknowledgements. ServiceAccount synchronization
does not add a public watch endpoint.

#### Identity rollout and rollback

Keep a stable private directory endpoint and shared identity database across API
replicas. Roll out schema-dependent clients only after every routed API exposes
the required concrete human types, `User` interface and mutations. During a
mixed deployment, use only the schema and configuration supported by all
serving versions.

Enabling ServiceAccount CDC is a forward database migration. Preserve applied
migration history and source UIDs; an older binary whose migration bundle omits
an applied migration cannot restart against that database. Do not remove the
migration or rewrite its checksum to force rollback.

The reference stack no longer registers the earlier generic identity schema.
Existing identities using an unregistered schema need an explicitly reviewed
directory transition; changing configuration is not an identity migration.
Preserve identity storage, backups and UIDs rather than deleting or recreating
users. If disabling the UserDir callbacks, remove the Kratos hook overlay at the
same time so blocking self-service hooks do not target a disabled receiver.

### Identity mail

The optional identity integration delivers native verification/recovery mail
through `POST /internal/notifications/mail` on the Admin listener. This route
is absent when notification delivery is disabled and is never mounted on
Storefront. Restrict ingress to trusted internal senders; requests require
`GitStore-Mail-Key` before the body is read.

| Key | Env Var | Default | Description |
|---|---|---|---|
| `api.notification.provider` | `GITSTORE_API__NOTIFICATION__PROVIDER` | `none` | `none` or `development`; no production backend is shipped |
| `api.notification.api_key` | `GITSTORE_API__NOTIFICATION__API_KEY` | empty | Required sensitive 32+ character credential when enabled |
| `api.notification.max_body_size` | `GITSTORE_API__NOTIFICATION__MAX_BODY_SIZE` | `64KiB` | Positive IEC size string, at most `1MiB`; bare numbers and decimal units are rejected. The smaller Admin body limit still applies |
| `api.notification.max_concurrent` | `GITSTORE_API__NOTIFICATION__MAX_CONCURRENT` | `16` | At most 128 in-flight parsing/delivery operations; saturation returns 503 |
| `api.notification.timeout` | `GITSTORE_API__NOTIFICATION__TIMEOUT` | `2s` | Delivery deadline, at most 10s; SMTP additionally caps work at 2s |
| `api.notification.development.smtp_uri` | `GITSTORE_API__NOTIFICATION__DEVELOPMENT__SMTP_URI` | empty | Private mail-catcher `smtp://` or `smtps://` endpoint |
| `api.notification.development.sender` | `GITSTORE_API__NOTIFICATION__DEVELOPMENT__SENDER` | `noreply@gitstore.local` | Development envelope/header sender |

The reference catcher uses implicit, self-signed TLS:
`smtps://mailslurper:1025/?skip_ssl_verify=true`. That verification bypass is
explicitly development-only; certificate verification is on by default.
Configure the hook overlay, `development` provider and privately injected key
together using [the environment example](../config/oidc/oidc.env.example).
Compose supplies the same key to native Kratos courier authentication.
Override `COURIER_HTTP_REQUEST_CONFIG_URL` if the internal Admin address differs
from `http://api:4000/internal/notifications/mail`.

Delivery succeeds only after the catcher accepts the message. Provider outage,
timeout or saturation returns a retryable 503; GitStore keeps no retry queue.
Kratos owns retries, and ambiguous responses can produce duplicate messages.
This at-least-once mail boundary is distinct from best-effort workload-mirror
events; it does not promise exactly-once delivery or a production mail service.

GitStore records only finite outcome/template-class metrics, duration and a
trace correlation ID, never recipients, subjects, bodies or codes. **Upstream
Kratos logs have a separate privacy boundary:** native courier failure logs
can include the native subject, and default code-flow subjects can contain
verification/recovery codes. Restrict those logs and configure non-sensitive
native subject templates where required.

Directory contact resolution uses the immutable subject and a fresh native
identity read, never an email-based identity lookup. When the primary address
changes, the previous verified address remains the contact snapshot until the
replacement is verified. A bounce observation for the old address does not
block a newly verified replacement. Without a trusted deliverability
observation, deliverability defaults to false; initial verification alone does
not invent one.

Consent boolean changes receive a server-generated timestamp; caller-supplied
timestamps are ignored. Bounce and timestamp-only changes do not advance
generation. These protected metadata updates are best-effort native writes,
not remote compare-and-swap: concurrent or privileged changes can be lost or
remain untracked. ServiceAccounts do not have human contact operations.
The delivery adapter is development-only and provides no consent policy engine,
production mail backend or durable retry queue.

### Logging

| Key          | Env Var                | Type   | Default | Required | Sensitive | Description                            |
|--------------|------------------------|--------|---------|----------|-----------|-----------------------------------------|
| `log.level`  | `GITSTORE_LOG__LEVEL`  | string | `info`  | No       | No        | `debug` \| `info` \| `warn` \| `error` |
| `log.format` | `GITSTORE_LOG__FORMAT` | string | `json`  | No       | No        | `json` \| `text`                       |

### Shared gRPC authentication

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `grpc_auth.hmac_secret` | `GITSTORE_GRPC_AUTH__HMAC_SECRET` | string | — | **Yes** | **Yes** | HMAC secret shared between `gitstore-api` and `gitstore-git-service` for inter-service gRPC authentication |

### Shared push-size ceiling

`push_limits` sets shared platform ceilings for repository policies and
individual pushes. A missing or zero repository limit uses the platform
ceiling; it does not mean unlimited. See
[push-size limits](products/push-validation.md#push-size-limits).

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `push_limits.max_pack_size` | `GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE` | IEC size string | `512MiB` | No | No | Platform ceiling for a single push's total pack size |
| `push_limits.max_file_size` | `GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE` | IEC size string | `100MiB` | No | No | Platform ceiling for a single file/blob within a push |

### Datastore

Automatic startup migration defaults to enabled. To prepare schemas using an
init container or a separate operator step, set
`api.datastore.scylla.auto_migrate = false` (environment:
`GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false`) on the API and run the matching
image's `gitctl migrate --hosts scylla:9042 --keyspace gitstore --timeout 5m`
before starting it. The command also accepts the existing Scylla environment
variables, including password, so private connection material need not appear
in arguments.

The operator must create the keyspace first. `gitctl migrate` uses the same
distributed migration lock as API startup, exits nonzero on failure and starts
no API listeners or CDC readers. Both startup modes require a complete,
unchanged migration history for the current binary. Disabled mode checks that
history with read-only queries and does not create even the migration ledger
or lock table. It is not a bypass for a breaking schema baseline or a
partially-applied migration.

| Key                                             | Env Var                                                       | Type            | Default          | Required | Sensitive | Description                                    |
|---------------------------------------------------|-------------------------------------------------------------------|-----------------|------------------|----------|-----------|--------------------------------------------------|
| `api.datastore.backend`                         | `GITSTORE_API__DATASTORE__BACKEND`                             | string          | `memdb`          | No       | No        | Active datastore backend: `memdb` or `scylla`  |
| `api.datastore.scylla.hosts`                    | `GITSTORE_API__DATASTORE__SCYLLA__HOSTS`                       | string list | `localhost:9042` | No       | No        | Scylla endpoints (`host:port`) |
| `api.datastore.scylla.keyspace`                 | `GITSTORE_API__DATASTORE__SCYLLA__KEYSPACE`                    | string          | `gitstore`       | No       | No        | Scylla keyspace name                           |
| `api.datastore.scylla.username`                 | `GITSTORE_API__DATASTORE__SCYLLA__USERNAME`                    | string          | —                | No       | No        | Scylla username (optional)                     |
| `api.datastore.scylla.password`                 | `GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`                    | string          | —                | No       | **Yes**   | Scylla password (optional, redacted in logs)   |
| `api.datastore.scylla.tls`                      | `GITSTORE_API__DATASTORE__SCYLLA__TLS`                         | boolean         | `false`          | No       | No        | Enable TLS for Scylla connections              |
| `api.datastore.scylla.disable_shard_aware_port` | `GITSTORE_API__DATASTORE__SCYLLA__DISABLE_SHARD_AWARE_PORT`    | boolean         | `false`          | No       | No        | Disable shard-aware Scylla port discovery      |
| `api.datastore.scylla.auto_migrate`              | `GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE`                | boolean         | `true`           | No       | No        | Run startup schema migration automatically     |
| `api.datastore.scylla.ignore_peer_addr`          | `GITSTORE_API__DATASTORE__SCYLLA__IGNORE_PEER_ADDR`            | boolean         | `false`          | No       | No        | Ignore peer-advertised addresses (set when Scylla runs behind NAT, e.g. Docker) |

See [Namespace admission operations](runbooks/namespace-admission.md) for the
repository lifecycle fence, which is always enabled and not configurable.

### Durable watch journal

The CDC-backed durable watch journal and its materializer are always on for
every watched kind (Namespace, Repository, Product, File, CategoryTaxonomy) —
there is no other watch mechanism and no reader/materializer toggle. Every key
below bounds journal behavior; it does not enable or disable it.

| Key                                              | Env Var                                                     | Type     | Default | Description                                                                                                                                  |
|----------------------------------------------------|------------------------------------------------------------------|----------|---------|-------------------------------------------------------------------------------------------------------------------------------------------------|
| `api.watch.journal.retention`                   | `GITSTORE_API__WATCH__JOURNAL__RETENTION`                     | duration | `168h`  | Journal TTL; must not exceed the fixed 14-day CDC retention (336h).                                                                           |
| `api.watch.journal.cdc.confidence_window`       | `GITSTORE_API__WATCH__JOURNAL__CDC__CONFIDENCE_WINDOW`        | duration | `500ms` | Scylla CDC consistency window before changes become eligible for ordered materialization; must remain below `materializer.max_lag`.           |
| `api.watch.journal.read.batch_size`             | `GITSTORE_API__WATCH__JOURNAL__READ__BATCH_SIZE`              | integer | `256`   | Replay/poll page size; must not exceed the fixed 4,096-sequence partition bucket.                                                             |
| `api.watch.journal.read.max_replay_events`      | `GITSTORE_API__WATCH__JOURNAL__READ__MAX_REPLAY_EVENTS`       | integer | `100000`| Resume ceiling before `WATCH_EXPIRED`; accepted range is 1–100,000.                                                                           |
| `api.watch.journal.poll.min`                    | `GITSTORE_API__WATCH__JOURNAL__POLL__MIN`                     | duration | `100ms` | Minimum journal poll delay; must not exceed `poll.max`.                                                                                       |
| `api.watch.journal.poll.max`                    | `GITSTORE_API__WATCH__JOURNAL__POLL__MAX`                     | duration | `2s`    | Maximum adaptive poll delay; must be at least `poll.min` and strictly below `retention`.                                                      |
| `api.watch.journal.subscriber.buffer`           | `GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BUFFER`            | integer | `64`    | Per-hop, per-subscription delivery buffer; accepted range is 1–256 and sustained overflow fails closed.                                        |
| `api.watch.journal.subscriber.backpressure`     | `GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BACKPRESSURE`      | duration | `30s`   | Maximum bounded wait for a slow subscriber before terminal `SUBSCRIBER_OVERFLOW`.                                                             |
| `api.watch.journal.bookmark_interval`           | `GITSTORE_API__WATCH__JOURNAL__BOOKMARK_INTERVAL`             | duration | `30s`   | Durable idle BOOKMARK interval.                                                                                                               |
| `api.watch.journal.materializer.lease_ttl`      | `GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__LEASE_TTL`       | duration | `30s`   | Materializer lease TTL; must exceed `materializer.lease_renew_interval`.                                                                      |
| `api.watch.journal.materializer.lease_renew_interval` | `GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__LEASE_RENEW_INTERVAL` | duration | `10s` | Renewal interval; must be less than `materializer.lease_ttl`.                                                                                 |
| `api.watch.journal.materializer.max_lag`        | `GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__MAX_LAG`         | duration | `60s`   | Reader readiness freshness ceiling; must remain below the fixed 14-day CDC retention.                                                         |

CDC retention and the journal sequence layout are fixed. Tune these settings
only when addressing replay volume, subscriber backpressure, or materializer
freshness. For rollout and recovery procedures, see the
[catalog lifecycle runbook](runbooks/catalog-lifecycle.md) and
[controller watch/status runbook](runbooks/controller-watch-status.md).

### Scylla projection operations

For projection audits, repairs, and their operational safeguards, see the
[Scylla projection repair runbook](runbooks/scylla-projection-repair.md).

### Example `config.toml`

See the maintained [API configuration example](../gitstore-api/config.toml.example).
Keep secrets, including the user-file credentials and `api.auth.jwt.secret`,
outside committed configuration and inject them through your deployment platform.

## gitstore-git-service

**Config file**: `gitstore.toml` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

### Core

| Key                              | Env Var                                       | Type   | Default                     | Required | Sensitive | Description                                       |
|------------------------------------|--------------------------------------------------|--------|-------------------------------|----------|-----------|-----------------------------------------------------|
| `git_service.grpc_port`          | `GITSTORE_GIT_SERVICE__GRPC_PORT`             | integer | `50051`                      | No       | No        | GitService gRPC server port (1–65535)             |
| `git_service.data_dir`           | `GITSTORE_GIT_SERVICE__DATA_DIR`              | string | `/var/lib/gitstore/repos`    | No       | No        | Bare repository storage directory                 |
| `git_service.catalog.uri`        | `GITSTORE_GIT_SERVICE__CATALOG__URI`          | string | `dns:///localhost:6000`      | No       | No        | gitstore-api CatalogService gRPC endpoint         |
| `git_service.validation.timeout` | `GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT`   | duration | `10s`                       | No       | No        | CatalogService validation RPC timeout             |
| `git_service.admission.branch_pattern` | `GITSTORE_GIT_SERVICE__ADMISSION__BRANCH_PATTERN` | string | `^refs/heads/main$`     | No       | No        | Ref pattern admitted into catalogue storage       |
| `log.level`                      | `GITSTORE_LOG__LEVEL`                         | string | `info`                       | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `log.format`                     | `GITSTORE_LOG__FORMAT`                        | string | `json`                       | No       | No        | `json` \| `text`                                  |
| `grpc_auth.hmac_secret`          | `GITSTORE_GRPC_AUTH__HMAC_SECRET`             | string | —                            | **Yes**  | **Yes**   | HMAC secret shared with `gitstore-api`            |
| `grpc_auth.hmac_secret_previous` | `GITSTORE_GRPC_AUTH__HMAC_SECRET_PREVIOUS`    | string | (unset)                      | No       | **Yes**   | Previous RPC signing key accepted by both API and Git during rotation; remove after all callers use the primary key |
| `push_limits.max_pack_size`      | `GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE`         | IEC size string | `512MiB`             | No       | No        | Platform ceiling for a single push's total pack size |
| `push_limits.max_file_size`      | `GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE`         | IEC size string | `100MiB`             | No       | No        | Platform ceiling for a single file/blob within a push |

`git_service.catalog.uri` must use the `dns:///host:port` scheme. Configure DNS
for service discovery; a different URI scheme is rejected at startup.

### Validation and admission are always on

Schema validation and admission run on every push and cannot be disabled.
`git_service.validation.timeout` bounds validation time, while
`git_service.admission.branch_pattern` selects refs admitted to the catalog.

### Push-size enforcement

The effective push limit is the lower of the repository policy and
`push_limits` ceiling. A missing or zero repository limit uses the platform
ceiling; it does not mean unlimited.

### CLI Flags

| Flag                   | Type   | Description                                           |
|------------------------|--------|---------------------------------------------------------|
| `--config-file <path>` | string | Load config from this path instead of `gitstore.toml` |
| `--log-level <level>`  | string | Override log level (highest priority)                 |

### Example `gitstore.toml`

See the maintained [Git service configuration example](../gitstore-git-service/config.toml.example).

---

## gitstore-controller-manager

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-controller-manager --config-file /path/to/config.toml` (required when selected). Repeat the flag to layer overlays: `--config-file base.toml --config-file overlay.toml` (later file wins per key).

**Env var prefix**: `GITSTORE_`

| Key                                                | Env Var                                                        | Type     | Default                             | Required         | Sensitive | Description                                                         |
|----------------------------------------------------|------------------------------------------------------------------|----------|---------------------------------------|------------------|-----------|------------------------------------------------------------------------|
| `controller.port`                                  | `GITSTORE_CONTROLLER__PORT`                                    | integer  | `5001`                              | No               | No        | HTTP port for `/health`, `/metrics`, and `/controller/v1/*`         |
| `controller.api_uri`                               | `GITSTORE_CONTROLLER__API_URI`                                 | string   | `http://localhost:4000/admin/graphql`     | No               | No        | GraphQL API URI used by reconcilers                                 |
| `controller.api_client.rate_limit.per_second` | `GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__PER_SECOND` | integer | `40` | No | No | Positive shared request rate across all kinds, queries, mutations and WebSocket upgrades |
| `controller.api_client.rate_limit.burst` | `GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__BURST` | integer | `10` | No | No | Positive shared request burst; credential renewal uses its separate singleflight budget |
| `controller.serviceaccount.name`                   | `GITSTORE_CONTROLLER__SERVICEACCOUNT__NAME`                    | string   | `gitstore-controller-manager`       | **Yes**          | No        | Enrolled ServiceAccount name                                        |
| `controller.serviceaccount.uid`                    | `GITSTORE_CONTROLLER__SERVICEACCOUNT__UID`                     | string   | (empty)                             | **Yes**          | No        | Enrolled ServiceAccount UID; prevents identity reuse after deletion |
| `controller.serviceaccount.key_ref.kind`           | `GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KIND`           | string   | (empty)                             | **Yes**          | No        | Must be `SecretRef`                                                 |
| `controller.serviceaccount.key_ref.name`           | `GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME`           | string   | (empty)                             | **Yes**          | No        | Logical bootstrap-secret name, not a filesystem path                |
| `controller.serviceaccount.assertion_audience`     | `GITSTORE_CONTROLLER__SERVICEACCOUNT__ASSERTION_AUDIENCE`      | string   | `gitstore-api/serviceaccount-token` | **Yes**          | No        | Audience for the signed assertion used to exchange a token          |
| `controller.serviceaccount.access_token_audience`  | `GITSTORE_CONTROLLER__SERVICEACCOUNT__ACCESS_TOKEN_AUDIENCE`   | string   | `gitstore-api`                      | **Yes**          | No        | Audience requested for the exchanged access token                   |
| `controller.secret_providers.bootstrap.type`       | `GITSTORE_CONTROLLER__SECRET_PROVIDERS__BOOTSTRAP__TYPE`       | string   | `file`                              | No               | No        | Bootstrap resolver type: `file` or `env`                            |
| `controller.secret_providers.bootstrap.base_path`  | `GITSTORE_CONTROLLER__SECRET_PROVIDERS__BOOTSTRAP__BASE_PATH`  | string   | `/run/secrets`                      | With `type=file` | No        | Directory containing controller-only `<key_ref.name>.json` signing records |
| `controller.secret_providers.bootstrap.env_variable` | `GITSTORE_CONTROLLER__SECRET_PROVIDERS__BOOTSTRAP__ENV_VARIABLE` | string | (empty) | With `type=env` | No | Explicit name of the environment variable containing the complete JSON signing record; not the record itself |
| `controller.reconcile.max_attempts`                | `GITSTORE_CONTROLLER__RECONCILE__MAX_ATTEMPTS`                 | integer  | `5`                                 | No               | No        | Retry limit before quarantine                                       |
| `controller.reconcile.stall_threshold`             | `GITSTORE_CONTROLLER__RECONCILE__STALL_THRESHOLD`              | duration | `5m`                                 | No               | No        | Worker stall threshold                                              |
| `controller.checkpoint.dir`                        | `GITSTORE_CONTROLLER__CHECKPOINT__DIR`                         | string   | `/var/lib/gitstore/checkpoints`     | No               | No        | Directory for the filesystem checkpoint store (one file per kind)   |
| `controller.checkpoint.flush_interval_events`      | `GITSTORE_CONTROLLER__CHECKPOINT__FLUSH_INTERVAL_EVENTS`       | integer  | `100`                                | No               | No        | Watch events between checkpoint persists                            |
| `controller.watch.max_backoff`                     | `GITSTORE_CONTROLLER__WATCH__MAX_BACKOFF`                      | duration | `30s`                                | No               | No        | Cap on exponential backoff between watch-stream reconnect attempts  |
| `controller.watch.resync_interval`                 | `GITSTORE_CONTROLLER__WATCH__RESYNC_INTERVAL`                  | duration | `10m`                                | No               | No        | Interval between full reconciliation resyncs of cached state        |
| `log.level`                                        | `GITSTORE_LOG__LEVEL`                                          | string   | `info`                              | No               | No        | `debug` \| `info` \| `warn` \| `error`                              |
| `log.format`                                       | `GITSTORE_LOG__FORMAT`                                         | string   | `json`                              | No               | No        | `json` \| `text`                                                    |

See the maintained [controller configuration example](../gitstore-controller-manager/config.toml.example).

Both bootstrap providers require a whole-record `SecretRef` and an atomic JSON
signing record containing the private key and its corresponding enrolled key ID:

```json
{
  "format": "serviceaccount-signing-key/v1",
  "values": {
    "privateKey": "<base64 of one PKCS#8 Ed25519 or ECDSA P-256 PEM key>",
    "keyID": "<base64 of the corresponding enrolled key ID>"
  }
}
```

For the `file` provider, the example resolves
`/run/secrets/controller-manager.json`. Provision the record privately with mode
`0600` and mount its directory read-only into `controller-manager` only. Replace
the entire record atomically when rotating, keeping the key and enrolled ID
together; see [Signing-material rotation](runbooks/secret-material-rotation.md).

For a deployment platform that injects secret environment variables, replace
the bootstrap table in the example with:

```toml
[controller.secret_providers.bootstrap]
type = "env"
env_variable = "CONTROLLER_SIGNING_RECORD"
```

Inject the complete JSON record into `CONTROLLER_SIGNING_RECORD` for the
controller process only. `env_variable` names that variable explicitly; no
prefix or variable name is derived from `key_ref.name`. Environment changes
require process replacement. A controller-only read-only file mount is preferred
where available.

The JSON record's `format` field is required and is not a TOML bootstrap
setting. Do not place real signing records or private-key values in TOML, the
shared `/config/gitstore.toml` file, Git, command-line arguments, or logs.

Static controller API tokens are not supported. See [Controller
authentication](runbooks/controller-auth.md) for enrollment, rotation,
readiness, and recovery procedures.

Controller checkpoints are stored under `controller.checkpoint.dir`. Keep this
directory on persistent storage across process restarts; see the
[controller watch/status runbook](runbooks/controller-watch-status.md) for
recovery and monitoring guidance.

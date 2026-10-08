# Configuration Reference

This document is the operator reference for configuring `gitstore-api`, `gitstore-git-service`, and `gitstore-controller-manager`.

Configuration is grouped by owning service: `[git_service]` is owned by `gitstore-git-service`, `[api]` is owned by `gitstore-api`, `[controller]` is owned by `gitstore-controller-manager`, and `[log]`, `[grpc_auth]`, and `[push_limits]` are shared across the services that read them.

---

## Grammar: durations and sizes

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
3. .env file                    (optional)
4. Environment variables        (highest priority)
5. CLI value overrides such as `--log-level` (where supported)
```

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

For production-friendly local templates, copy the per-service example files in
this repo:

- `../gitstore-git-service/config.toml.example`
- `../gitstore-api/config.toml.example`
- `../gitstore-controller-manager/config.toml.example`

These are intended to be copied to `/etc/gitstore/...` or another deployment
managed config directory and tuned per environment.

## Shared Local Compose Configuration

`make compose` activates the Compose `local` profile and mounts
`config/config.toml` read-only into all three core containers at
`/etc/gitstore/gitstore.toml`. The API also receives the development RBAC
policy at `/etc/gitstore/policy.yaml`. Select another host-side file explicitly
with:

```bash
make compose CONFIG_FILE=./config/config.stage.toml
```

The tracked file contains only development credentials (`admin` / `admin123`),
a long-lived development controller token, and a development HMAC/JWT secret.
Never deploy it to production. Production deployments should mount a reviewed
configuration/policy revision on every replica and inject secrets through the
deployment platform. Configuration is startup-only, so replicas remain
stateless and rolling upgrades can mix binaries with and without the additive
flag while existing environment/default discovery remains supported.

### Sensitive values

Keys marked **Sensitive** are always logged as `<redacted>` (when set) or `<unset>` (when absent), regardless of log level. Production secrets should be supplied externally rather than committed to config files; the tracked local fixture is intentionally development-only.

An empty string (`KEY=`) for a **Required** key is treated identically to an absent key and causes a startup failure listing all failing keys.

### Legacy keys fail closed

Every TOML key and environment variable a service used to own before its
current grouping is checked independently at startup. Setting a removed or
renamed key (in either the config file or the environment) makes the owning
service refuse to start with an error naming its replacement, instead of
silently falling back to a default. See [Upgrading configuration](#upgrading-configuration)
for the full old-key → new-key mapping.

---

## gitstore-api

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-api --config-file /path/to/config.toml` (required when selected). Repeat the flag to layer overlays: `--config-file base.toml --config-file overlay.toml` (later file wins per key).

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

### API Server

| Key                          | Env Var                                 | Type    | Default | Required | Sensitive | Description                                                                 |
|-------------------------------|------------------------------------------|---------|---------|----------|-----------|-------------------------------------------------------------------------------|
| `api.port`                   | `GITSTORE_API__PORT`                    | integer | `4000`  | No       | No        | HTTP port the GraphQL API server listens on (1–65535)                       |
| `api.git_port`               | `GITSTORE_API__GIT_PORT`                | integer | `9000`  | No       | No        | Git Smart HTTP port the API server listens on (1–65535)                     |
| `api.grpc_port`              | `GITSTORE_API__GRPC_PORT`               | integer | `6000`  | No       | No        | CatalogService gRPC port called by gitstore-git-service                     |
| `api.rate_limit.per_second`  | `GITSTORE_API__RATE_LIMIT__PER_SECOND`  | float   | `50`    | No       | No        | Sustained per-client-IP request rate allowed on `/graphql`                   |
| `api.rate_limit.burst`       | `GITSTORE_API__RATE_LIMIT__BURST`       | integer | `100`   | No       | No        | Per-client-IP token-bucket burst size on top of `api.rate_limit.per_second` |

### Git Service Connection

| Key                 | Env Var                       | Type   | Default                  | Required | Sensitive | Description                          |
|----------------------|---------------------------------|--------|---------------------------|----------|-----------|---------------------------------------|
| `api.git_service.uri` | `GITSTORE_API__GIT_SERVICE__URI` | string | `dns:///localhost:50051` | Yes      | No        | gRPC address of gitstore-git-service |

### Git Smart HTTP Endpoints

The following endpoints are served on port `api.git_port` (default `9000`):

| Method | Path                                                              | Description                                       |
|--------|-------------------------------------------------------------------|---------------------------------------------------|
| `GET`  | `/{namespace}/{repo}.git/info/refs?service=git-upload-pack`       | Advertise refs for fetch/clone                    |
| `GET`  | `/{namespace}/{repo}.git/info/refs?service=git-receive-pack`      | Advertise refs for push                           |
| `POST` | `/{namespace}/{repo}.git/git-upload-pack`                         | Upload pack (fetch/clone data transfer)           |
| `POST` | `/{namespace}/{repo}.git/git-receive-pack`                        | Receive pack (push data transfer)                 |
| `GET`  | `/health`                                                         | Health probe — returns `{"status":"ok"}`          |

### Authentication

| Key                             | Env Var                                     | Type     | Default    | Required | Sensitive | Description                                                 |
|-----------------------------------|------------------------------------------------|----------|------------|----------|-----------|---------------------------------------------------------------|
| `api.auth.static_users.users_file` | `GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE` | string | users.yaml | When selected | No | YAML file containing local users |
| `api.auth.jwt.secret`          | `GITSTORE_API__AUTH__JWT__SECRET`          | string   | —          | When `static-users` is selected | **Yes** | JWT signing key (minimum 32 characters)                  |
| `api.auth.jwt.ttl`             | `GITSTORE_API__AUTH__JWT__TTL`             | duration | `24h`      | No       | No        | JWT token validity                                           |
| `api.auth.jwt.issuer`          | `GITSTORE_API__AUTH__JWT__ISSUER`          | string   | `gitstore` | No       | No        | JWT `iss` claim value                                       |
| `api.auth.jwt.refresh_grace`   | `GITSTORE_API__AUTH__JWT__REFRESH_GRACE`   | duration | `60s`      | No       | No        | Window after expiry during which `refreshToken` is accepted |

For config files, local users are selected with `[api.auth.static_users]` and `users_file = "users.yaml"`; JWT keys remain nested under `[api.auth.jwt]`.

The root operator helpers target the local Compose files in `config/` by default:

```bash
make add-user USERNAME=alice PASSWORD='secret' EMAIL=alice@example.com DISPLAY_NAME='Alice Doe'
make add-role ROLE=developer ALLOW='repository.read.own,repository.write.own'
make assign-role SUBJECT=alice ROLE=developer
```

Background API work uses the explicit `system:api` subject. Production RBAC
policies must bind that subject only to the repository actions required by the
deployment; the development policy provides the `api-internal` role as the
minimal example. It is carried to GitService as an authorization envelope and
is not a substitute for the API-to-Git-service HMAC.

Set `AUTH_CONFIG_DIR=gitstore-api` for native-development files, or override
`USERS_FILE` and `POLICY_FILE` separately. These commands validate and atomically
replace one YAML file at a time; `add-user` never changes authorization policy.

`static-users` always appends `/static-users` to the configured issuer base when minting tokens, while accepting the exact configured base for legacy sessions during rolling upgrades. Logout and refresh rotation require the shared ScyllaDB revocation table in production; migration 007 creates it automatically.

### Service-account authentication

Service-account authentication is enabled only when
`api.auth.authn.chain` contains `serviceaccount-assertion` and
`serviceaccount-jwt`. The API issues and verifies the controller's
short-lived access tokens; the controller proves possession of a separately
mounted private key to obtain them.

| Key | Env Var | Type | Default | Required | Sensitive | Description |
|-----|---------|------|---------|----------|-----------|-------------|
| `api.auth.authn.chain` | `GITSTORE_API__AUTH__AUTHN__CHAIN` | list of strings | `["static-users","anonymous"]` | No | No | Ordered AuthN providers; include both service-account providers to enable this flow |
| `api.auth.serviceaccount.issuer` | `GITSTORE_API__AUTH__SERVICEACCOUNT__ISSUER` | string | `gitstore` | No | No | Issuer for service-account access tokens |
| `api.auth.serviceaccount.audience` | `GITSTORE_API__AUTH__SERVICEACCOUNT__AUDIENCE` | string | `gitstore-api` | No | No | Required audience for service-account access tokens |
| `api.auth.serviceaccount.assertion_audience` | `GITSTORE_API__AUTH__SERVICEACCOUNT__ASSERTION_AUDIENCE` | string | `gitstore-api/serviceaccount-token` | No | No | Required audience for controller assertions at the token-exchange endpoint |
| `api.auth.serviceaccount.signing_key` | `GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY` | PEM private key | (empty) | **Yes, when a service-account provider is chained** | **Yes** | API-only Ed25519 or ECDSA P-256 access-token signing key |
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

`push_limits` is a static platform ceiling shared by `gitstore-api` and
`gitstore-git-service`. At admission, the API rejects any Namespace
`spec.pushPolicyDefaults` value above this ceiling. At push time,
`gitstore-git-service` clamps the effective per-push limit to
`min(repository policy, ceiling)`, treating a `0`/absent repository policy as
the ceiling itself — there is no "0 = unlimited" sentinel anymore. See
[Push Validation and Admission Pipeline](products/push-validation.md#push-size-limits)
for the full enforcement path.

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
| `api.datastore.scylla.hosts`                    | `GITSTORE_API__DATASTORE__SCYLLA__HOSTS`                       | list of strings | `localhost:9042` | No       | No        | Comma-separated Scylla endpoints (`host:port`) |
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

The CDC retention window (14 days) and the journal sequence bucket width
(4,096) are fixed by the baseline Scylla schema and are not configurable.

Scylla journal writes use logged conditional batches capped at 32 statements
and an estimated 32 KiB of encoded event data. The caps are intentionally
independent of the 4,096-sequence partition bucket so a lagging CDC stream
cannot create an oversized catch-up batch.

Product durable-watch rollout follows the same fenced reader/materializer
configuration family as Namespace and Repository. Enable it only after the
Product CDC migration and Product-aware API/controller rollout; see
[`product-lifecycle.md`](runbooks/product-lifecycle.md) for the mixed-version
deny and rollback sequence.

See [Namespace watch contract](namespace/namespace-watch.md) and the
[controller watch/status runbook](runbooks/controller-watch-status.md).

### Scylla projection operations

`gitctl` reads the same Scylla environment variables for offline projection
audit and repair. The password is read only from
`GITSTORE_API__DATASTORE__SCYLLA__PASSWORD`; do not pass it as a CLI argument.

```bash
cd gitstore-api
go run ./cmd/gitctl scylla-projection-audit
go run ./cmd/gitctl scylla-projection-repair --dry-run
go run ./cmd/gitctl scylla-projection-repair --confirm
```

Optional command flags override non-secret connection settings:
`--hosts`, `--keyspace`, `--username`, `--tls`, and
`--disable-shard-aware-port`. Repair requires either `--dry-run` or explicit
`--confirm`; conditional misses and post-repair findings return an error.

Operational invariants:

- partition hard ceiling: 100 MiB;
- hot-partition target: 10 MiB;
- `gc_grace_seconds`: 10 days;
- completed anti-entropy repair interval: at most 7 days;
- no TWCS on tables with updates or explicit deletes.

Datastore metrics use bounded labels only:

- `gitstore_datastore_projection_write_failures_total`
  (`operation`, `backend`, `resource_kind`, `projection`);
- `gitstore_datastore_compensation_attempts_total` and
  `gitstore_datastore_compensation_failures_total` (same bounded labels);
- `gitstore_datastore_projection_findings_total` (adds `finding_type`);
- `gitstore_datastore_operation_duration_seconds` (`operation`, `backend`).

Alert on every compensation failure, any partition above 100 MiB, repair older
than seven days, and sustained growth in projection findings or repair backlog.
Resource UIDs and names belong in structured logs, never metric labels. See
[`scylla-projection-repair.md`](runbooks/scylla-projection-repair.md).

### Example `config.toml`

```toml
[api]
port = 4000
git_port = 9000
grpc_port = 6000

[api.rate_limit]
per_second = 50
burst = 100

[api.git_service]
uri = "dns:///localhost:50051"

[api.auth.jwt]
secret = "replace-with-strong-random-secret-at-least-32-chars"
ttl = "24h"
issuer = "gitstore"
refresh_grace = "60s"

[api.datastore]
backend = "memdb"

[api.datastore.scylla]
hosts = ["localhost:9042"]
keyspace = "gitstore"
tls = false

[grpc_auth]
hmac_secret = "replace-with-shared-hmac-secret"

[push_limits]
max_pack_size = "512MiB"
max_file_size = "100MiB"

[log]
level = "debug"
format = "json"
```

Secrets in the users file and `api.auth.jwt.secret` must remain outside committed operator configuration, never in `config.toml`.

## gitstore-git-service

**Config file**: `gitstore.toml` (optional, current working directory)
**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

### Core

| Key                              | Env Var                                       | Type   | Default                     | Required | Sensitive | Description                                       |
|------------------------------------|--------------------------------------------------|--------|-------------------------------|----------|-----------|-----------------------------------------------------|
| `git_service.grpc_port`          | `GITSTORE_GIT_SERVICE__GRPC_PORT`             | u16    | `50051`                      | No       | No        | GitService gRPC server port                       |
| `git_service.data_dir`           | `GITSTORE_GIT_SERVICE__DATA_DIR`              | string | `/var/lib/gitstore/repos`    | No       | No        | Bare repository storage directory                 |
| `git_service.catalog.uri`        | `GITSTORE_GIT_SERVICE__CATALOG__URI`          | string | `dns:///localhost:6000`      | No       | No        | gitstore-api CatalogService gRPC endpoint         |
| `git_service.validation.timeout` | `GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT`   | duration | `10s`                       | No       | No        | CatalogService validation RPC timeout             |
| `git_service.admission.branch_pattern` | `GITSTORE_GIT_SERVICE__ADMISSION__BRANCH_PATTERN` | string | `^refs/heads/main$`     | No       | No        | Ref pattern admitted into catalogue storage       |
| `log.level`                      | `GITSTORE_LOG__LEVEL`                         | string | `info`                       | No       | No        | `trace` \| `debug` \| `info` \| `warn` \| `error` |
| `log.format`                     | `GITSTORE_LOG__FORMAT`                        | string | `json`                       | No       | No        | `json` \| `text`                                  |
| `grpc_auth.hmac_secret`          | `GITSTORE_GRPC_AUTH__HMAC_SECRET`             | string | —                            | **Yes**  | **Yes**   | HMAC secret shared with `gitstore-api`            |
| `grpc_auth.hmac_secret_previous` | `GITSTORE_GRPC_AUTH__HMAC_SECRET_PREVIOUS`    | string | (unset)                      | No       | **Yes**   | Previous HMAC secret accepted during rotation; must not be set to an empty string |
| `push_limits.max_pack_size`      | `GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE`         | IEC size string | `512MiB`             | No       | No        | Platform ceiling for a single push's total pack size |
| `push_limits.max_file_size`      | `GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE`         | IEC size string | `100MiB`             | No       | No        | Platform ceiling for a single file/blob within a push |

`git_service.catalog.uri` is configured with the `dns:///host:port` scheme.
`gitstore-git-service` resolves it once at startup into a plain
`http://host:port` URI it can dial directly — tonic has no DNS resolver, so
unlike `gitstore-api`'s Go gRPC client this does not round-robin across
resolved addresses; it connects to the first result once. A non-`dns:///`
scheme is rejected at startup.

### Validation and admission are always on

Pre-receive schema validation and post-receive admission control run
unconditionally on every push — there is no phase toggle and no way to
disable or reorder them. `git_service.validation.timeout` bounds the blocking
pre-receive `ValidateResources` call; `git_service.admission.branch_pattern`
selects which refs trigger post-receive catalog storage via `AdmitResources`.
See [Push Validation and Admission Pipeline](products/push-validation.md) for
the full pipeline.

### Push-size enforcement

`gitstore-git-service` clamps every push to
`effective = min(repository policy, push_limits ceiling)`, treating a
`0`/absent repository policy (received from the API's `PushPolicyContext` on
each `ReceivePack` call) as the ceiling itself. The ceiling is always
positive, so the effective limit is always positive and enforceable — there
is no "0 = unlimited" push size anymore. See
[Push Validation and Admission Pipeline](products/push-validation.md#push-size-limits).

### CLI Flags

| Flag                   | Type   | Description                                           |
|------------------------|--------|---------------------------------------------------------|
| `--config-file <path>` | string | Load config from this path instead of `gitstore.toml` |
| `--log-level <level>`  | string | Override log level (highest priority)                 |

### Example `gitstore.toml`

```toml
[git_service]
grpc_port = 50051
data_dir = "/var/lib/gitstore/repos"

[git_service.catalog]
uri = "dns:///localhost:6000"

[git_service.validation]
timeout = "10s"

[git_service.admission]
branch_pattern = "^refs/heads/main$"

[grpc_auth]
hmac_secret = "replace-with-shared-hmac-secret"

[push_limits]
max_pack_size = "512MiB"
max_file_size = "100MiB"

[log]
level = "info"
format = "json"
```

---

## gitstore-controller-manager

**Config file**: `config.toml` (optional, current working directory)

**Explicit file**: `gitstore-controller-manager --config-file /path/to/config.toml` (required when selected). Repeat the flag to layer overlays: `--config-file base.toml --config-file overlay.toml` (later file wins per key).

**`.env` file**: `.env` (optional, current working directory)
**Env var prefix**: `GITSTORE_`

| Key                                                | Env Var                                                        | Type     | Default                             | Required         | Sensitive | Description                                                         |
|----------------------------------------------------|------------------------------------------------------------------|----------|---------------------------------------|------------------|-----------|------------------------------------------------------------------------|
| `controller.port`                                  | `GITSTORE_CONTROLLER__PORT`                                    | integer  | `5001`                              | No               | No        | HTTP port for `/health`, `/metrics`, and `/controller/v1/*`         |
| `controller.api_uri`                               | `GITSTORE_CONTROLLER__API_URI`                                 | string   | `http://localhost:4000/graphql`     | No               | No        | GraphQL API URI used by reconcilers                                 |
| `controller.api_client.rate_limit.per_second` | `GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__PER_SECOND` | integer | `40` | No | No | Positive shared request rate across all kinds, queries, mutations and WebSocket upgrades |
| `controller.api_client.rate_limit.burst` | `GITSTORE_CONTROLLER__API_CLIENT__RATE_LIMIT__BURST` | integer | `10` | No | No | Positive shared request burst; credential renewal uses its separate singleflight budget |
| `controller.serviceaccount.namespace`              | `GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE`               | string   | (empty)                             | **Yes**          | No        | Enrolled ServiceAccount namespace                                   |
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

Example:

```toml
[controller]
port = 5001
api_uri = "http://localhost:4000/graphql"

[controller.api_client.rate_limit]
per_second = 40
burst = 10

[controller.serviceaccount]
namespace = "controllers"
name = "gitstore-controller-manager"
uid = "<enrolled-service-account-uid>"
assertion_audience = "gitstore-api/serviceaccount-token"
access_token_audience = "gitstore-api"

[controller.reconcile]
max_attempts = 5
stall_threshold = "5m"

[controller.checkpoint]
dir = "/var/lib/gitstore/checkpoints"
flush_interval_events = 100

[controller.watch]
max_backoff = "30s"
resync_interval = "10m"

[controller.serviceaccount.key_ref]
kind = "SecretRef"
name = "controller-manager"

[controller.secret_providers.bootstrap]
type = "file"
base_path = "/run/secrets"

[log]
level = "info"
format = "json"
```

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

The removed `controller.secret_providers.bootstrap.format`,
`controller.secret_providers.bootstrap.env_prefix`,
`controller.serviceaccount.key_id`, and `controller.serviceaccount.key_ref.key`
settings are rejected, not compatibility options. The JSON record's `format`
field above is required and is not a TOML bootstrap setting. Do not place real
signing records or private-key values in TOML, the shared `/config/gitstore.toml`
file, Git, command-line arguments, or logs.

Static controller API tokens are not supported. See [Controller
authentication](runbooks/controller-auth.md) for enrollment, rotation,
readiness, and recovery procedures.

List-then-watch bootstrap, restart resume, and expired-watch-cursor recovery for registered
resource kinds persist a per-kind restart checkpoint under `controller.checkpoint.dir`. Each
checkpoint contains the `resourceVersion`, cache snapshot, and deletion replay keys needed to
restore volatile controller state without losing queued reconciliation work.
Checkpoint health — last successful write time, replay backlog, and write-failure count — is
exposed on the existing `/metrics` endpoint as `gitstore_controller_checkpoint_last_write_timestamp_seconds`,
`gitstore_controller_checkpoint_replay_backlog`, and `gitstore_controller_checkpoint_write_failures_total`
(all labeled by `kind`).

---

## Local Development with `.env`

All Go services automatically load a `.env` file from the current working directory at startup. The Git service loads `.env` in its binary entrypoint before resolving layered configuration. Shell environment variables always override `.env` values.

For the shared gRPC HMAC secret, `make secret TARGET=grpc-hmac` writes the same `GITSTORE_GRPC_AUTH__HMAC_SECRET` value to both `gitstore-api/.env` and `gitstore-git-service/.env` so local API and git-service runs stay in sync. Use `make secret TARGET=jwt` for the API session-signing secret; `make generate` is reserved for generated source and schema artifacts.

Copy the example file and fill in the required values:

```bash
# gitstore-api
cp gitstore-api/.env.example gitstore-api/.env

# gitstore-git-service
cp gitstore-git-service/.env.example gitstore-git-service/.env

# gitstore-controller-manager
cp gitstore-controller-manager/.env.example gitstore-controller-manager/.env
```

See `.env.example` in each service directory for the full list of supported variables with their types, defaults, and required/optional status.

---

## Upgrading configuration

Configuration moved from a mix of flat and service-ambiguous keys to a layout
grouped by owning service (`[git_service]`, `[api]`, `[controller]`, plus
shared `[log]`, `[grpc_auth]`, `[push_limits]`). Every key below fails startup
with an explicit error naming its replacement if the old form (TOML key or
environment variable) is still set — there is no silent fallback.

### gitstore-git-service

| Old key                                   | New key                                      | Notes |
|---------------------------------------------|-------------------------------------------------|-------|
| `grpc.port` (`GITSTORE_GRPC__PORT`)       | `git_service.grpc_port` (`GITSTORE_GIT_SERVICE__GRPC_PORT`) | |
| `git.data_dir` (`GITSTORE_GIT__DATA_DIR`) | `git_service.data_dir` (`GITSTORE_GIT_SERVICE__DATA_DIR`) | |
| `git.repo.max_file_size`                  | `push_limits.max_file_size`                   | Now a shared IEC size string, not a raw byte integer |
| `git.repo.max_pack_size_bytes`            | `push_limits.max_pack_size`                   | Now a shared IEC size string, not a raw byte integer |
| `hooks.git_receive_pack.*`                | removed                                         | Pre-receive validation and post-receive admission are unconditionally on; there is no phase toggle |
| `schema_validation.phase`                 | removed                                         | Schema validation is fixed to pre-receive |
| `schema_validation.timeout_secs`          | `git_service.validation.timeout`              | Now a duration string (e.g. `"10s"`), not integer seconds |
| `admission_control.phase`                 | removed                                         | Admission is fixed to post-receive |
| `admission_control.branch_pattern`        | `git_service.admission.branch_pattern`        | Default changed from `refs/heads/.*` to `^refs/heads/main$` |
| `catalog_service.uri`                     | `git_service.catalog.uri`                     | Scheme changed from `http://host:port` to `dns:///host:port` |
| `auth.grpc.hmac_secret`                   | `grpc_auth.hmac_secret`                       | Moved to the top-level shared table |
| `auth.grpc.hmac_secret_previous`          | `grpc_auth.hmac_secret_previous`              | Moved to the top-level shared table |

### gitstore-api

| Old key                                              | New key                                        | Notes |
|---------------------------------------------------------|---------------------------------------------------|-------|
| `api.rate_limit_per_second`                           | `api.rate_limit.per_second`                      | |
| `api.rate_limit_burst`                                | `api.rate_limit.burst`                           | |
| `git.grpc.uri`                                        | `api.git_service.uri`                            | Scheme unchanged (`dns:///host:port`) |
| `datastore.*` (`backend`, `scylla.*`)                 | `api.datastore.*`                                | |
| `auth.staticusers.users_file`                         | `api.auth.static_users.users_file`               | |
| `auth.jwt.secret`                                     | `api.auth.jwt.secret`                            | |
| `auth.jwt.duration`                                   | `api.auth.jwt.ttl`                               | Leaf renamed `duration` → `ttl` |
| `auth.jwt.issuer`                                     | `api.auth.jwt.issuer`                            | |
| `auth.jwt.refresh_grace`                              | `api.auth.jwt.refresh_grace`                     | |
| `auth.grpc.hmac_secret`                               | `grpc_auth.hmac_secret`                          | Moved out of `auth` entirely, to the top-level shared table |
| `auth.authn.chain`                                    | `api.auth.authn.chain`                           | |
| `auth.authz.provider`                                 | `api.auth.authz.provider`                        | |
| `auth.userdir.provider`                               | `api.auth.userdir.provider`                      | |
| `auth.serviceaccount.*`                               | `api.auth.serviceaccount.*`                      | Same leaf names |
| `auth.oidc.*`                                         | `api.auth.oidc_jwt.*`                            | Provider id `oidc-jwt`; same leaf names |
| `auth.rbac.policy_file`                                | `api.auth.rbac_local.policy_file`                | Provider id `rbac-local` |
| `watch.namespace.readers_enabled`                     | removed                                           | The durable watch journal reader is always on |
| `watch.namespace.materializer_enabled`                | removed                                           | The CDC materializer is always on |
| `watch.namespace.cdc_retention_seconds`               | removed                                           | Fixed at 14 days by the baseline schema |
| `watch.namespace.bucket_size`                         | removed                                           | Fixed at 4,096, persisted on first init |
| `watch.namespace.journal_retention_seconds`           | `api.watch.journal.retention`                    | Now a duration string (e.g. `"168h"`), not integer seconds |
| `watch.namespace.cdc_confidence_window_millis`        | `api.watch.journal.cdc.confidence_window`        | Now a duration string (e.g. `"500ms"`), not integer milliseconds |
| `watch.namespace.read_batch_size`                     | `api.watch.journal.read.batch_size`              | |
| `watch.namespace.max_replay_events`                   | `api.watch.journal.read.max_replay_events`       | |
| `watch.namespace.subscriber_buffer`                   | `api.watch.journal.subscriber.buffer`            | |
| `watch.namespace.subscriber_backpressure_millis`      | `api.watch.journal.subscriber.backpressure`      | Now a duration string, not integer milliseconds |
| `watch.namespace.poll_min_millis`                     | `api.watch.journal.poll.min`                     | Now a duration string, not integer milliseconds |
| `watch.namespace.poll_max_millis`                     | `api.watch.journal.poll.max`                     | Now a duration string, not integer milliseconds |
| `watch.namespace.bookmark_interval_seconds`           | `api.watch.journal.bookmark_interval`            | Now a duration string, not integer seconds |
| `watch.namespace.lease_ttl_seconds`                   | `api.watch.journal.materializer.lease_ttl`       | Now a duration string, not integer seconds |
| `watch.namespace.lease_renew_interval_seconds`        | `api.watch.journal.materializer.lease_renew_interval` | Now a duration string, not integer seconds |
| `watch.namespace.max_materializer_lag_seconds`        | `api.watch.journal.materializer.max_lag`         | Now a duration string, not integer seconds |
| `features.namespace_repository_fence`                 | removed                                           | The namespace/repository fence is always enabled |

New: shared `push_limits.max_pack_size` (default `"512MiB"`) and
`push_limits.max_file_size` (default `"100MiB"`) — see
[Shared push-size ceiling](#shared-push-size-ceiling).

### gitstore-controller-manager

| Old key                                           | New key                                           | Notes |
|------------------------------------------------------|-------------------------------------------------------|-------|
| `controller.api_client.requests_per_second`        | `controller.api_client.rate_limit.per_second`       | |
| `controller.api_client.burst`                      | `controller.api_client.rate_limit.burst`            | |

All controller duration keys (`reconcile.stall_threshold`, `watch.max_backoff`,
`watch.resync_interval`) are unchanged keys, now restricted to the `ms|s|m|h`
grammar described above (they were already duration strings before).

### gitctl

`gitctl` reads the same `api.datastore.*` keys/env vars as `gitstore-api` (now
under `api.datastore` instead of top-level `datastore`). Its `gen-jwt-secret`
and `gen-hmac-secret` subcommands now print `GITSTORE_API__AUTH__JWT__SECRET=...`
and `GITSTORE_GRPC_AUTH__HMAC_SECRET=...` respectively (both renamed to match
the keys above).

### Unaffected

`gitstore-oidc-bridge` has its own independent `GITSTORE_OIDC_BRIDGE__*`
environment namespace and does not read `config.toml` at all — it needs no
changes for this migration.

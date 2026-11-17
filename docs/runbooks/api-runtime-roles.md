# API runtime roles and deployment endpoints

## Select processes, then listeners

The same `api` executable and image serve any nonempty subset of
`smart-http`, `catalog`, `admin`, and `storefront`. For example:

```sh
api serve --all
api serve --roles=smart-http,catalog
api serve --roles=admin
api serve --roles=storefront
```

The existing invocation without `serve` remains supported. `--all` and
`--roles` are mutually exclusive. Command flags override `api.roles` in
configuration; `GITSTORE_API__ROLES=smart-http,catalog` is the equivalent
environment setting. Unknown, empty and duplicate roles are rejected.

The default and `--all` select Smart HTTP, catalog and Admin. Storefront
publication readers and trusted target binding are not available. Explicitly
selecting `storefront` starts its bounded unavailable boundary, not a published
catalog: `/storefront/graphql` returns 503 `STOREFRONT_UNAVAILABLE` and never
reads current Admin data. A Storefront-only process constructs no datastore,
Git client, authentication registry, bootstrap provisioner or journal worker.

| Role | Listener setting | Dependency/work |
| --- | --- | --- |
| `smart-http` | `api.git_port`, `api.smart_http.bind_host` | Datastore, configured AuthN/AuthZ/UserDir, Git RPC |
| `catalog` | `api.grpc_port`, `api.catalog.bind_host` | Same, in-process admission, bootstrap and durable journal materialization |
| `admin` | `api.admin_port`, `api.admin.bind_host` | Same, GraphQL authoring, **in-process** admission, bootstrap, journal, configured identity webhooks/mail |
| `storefront` | `api.storefront_port`, `api.storefront.bind_host` | Unavailable GraphQL boundary only |

Omitted bind hosts listen on all local interfaces. Ports default to
9000/6000/4000/4001 respectively. Disabled roles bind nothing. Only active
listeners reserve ports. Internal Git RPC uses `api.git_service.uri`;
Git hooks use `git_service.catalog.uri`. Neither is a client advertisement.
All management/callback replicas must use the same persistent datastore,
provider policies, UserDir configuration and inter-service signing material.
Independent memdb processes do **not** share catalog state.

`api.graphql_layout = "dedicated"` is the default. With `"shared"` and both
GraphQL roles selected, both schemas use the Admin bind host/port; no
Storefront socket is opened. Selecting Storefront alone still uses its named
bind host/port. Paths remain `/admin/graphql` and `/storefront/graphql`;
playgrounds remain prefixed. `/graphql` is not an alias or redirect.
Schema authentication, CORS and execution budgets remain independent.
Keep dedicated listeners or a private Admin-only process when network-level
Admin separation is required.

## Start without circular readiness dependencies

Start the singleton Git service and selected API processes independently.
Do not make Git startup depend on catalog readiness while catalog startup
depends on Git readiness. API listener construction does not call Git.
Datastore construction/migration and local configuration validation still
precede listening; run schema migration separately before a rolling deployment
when startup must not perform DDL.

Admin/catalog processes repair the two bootstrap namespace/repository
identities asynchronously. Concurrent replicas use the existing optimistic
metadata operations and deterministic repository IDs. Each process runs one
attempt at a time, with a 30-second deadline and retry delays of 1, 2, 4,
8, 16, then 30 seconds. Shutdown cancels pending bootstrap. Smart HTTP and
Storefront do not provision repositories. These retries cover idempotent
bootstrap repair only, never automatic replay of authoring or admission.

Admin returns 503 with `Retry-After: 1` until its bootstrap completes.
Catalog continues accepting authenticated callbacks while bootstrap is
pending; otherwise the Git → catalog → Git callback cycle could not recover.
Dependency failures return bounded errors, not a datastore-only authoring
fallback. Readiness is an orchestration signal, not authorization.

Every selected HTTP listener exposes:

* `/health`: local liveness, independent of dependencies and application
  concurrency/rate-limit pools.
* `/ready/<role>`: 200 for a ready selected role, 503 for unavailable
  dependencies, 404 for an unselected/unknown role.
* `/ready`: 200 only when **every** selected role is ready. Explicit
  Storefront selection currently makes aggregate readiness 503.

When exercising the unavailable Storefront boundary alongside Admin, use
`/ready/admin` for management routing. Do not treat aggregate 503 as an
Admin outage or weaken the role-specific dependency checks.

Catalog-only deployments use standard gRPC Health `Check`: service
`liveness` is local liveness; `catalog`, `admin`, `smart-http`, or
`storefront` checks a selected role; empty service checks the aggregate.
Unselected/unknown names return `NOT_FOUND`. `Watch` is not implemented.
Protect these operational surfaces through deployment networking.
`/metrics` remains on Admin only.

Ready management roles require a usable datastore, connected Git transport,
completed bootstrap and current durable journal progress; configured dynamic
authorization also requires its local snapshot readiness. Smart HTTP requires
datastore, Git transport and its configured authorization readiness. Probes
have one-second bounds and never call a peer's readiness endpoint. A
connected transport does not certify every repository operation, authentication
provider or remote catalog callback; monitor real operation failures as well.

Smart HTTP and catalog have independent `in_flight`/`deadline` budgets under
`api.smart_http` and `api.catalog`. Defaults are 32 requests and 2 minutes
for Smart HTTP, 32 RPCs and 30 seconds for catalog; valid deadlines are
positive and at most 10 minutes, valid concurrency is 1–1024. Saturation
fails immediately (HTTP 503 or gRPC `RESOURCE_EXHAUSTED`), without an
unbounded queue. Catalog health checks do not consume callback slots.
Keep callback pools independent from originating Admin/Smart HTTP work:
admission must be able to read Git while an originating operation waits.
GraphQL retains its separate configured endpoint budgets.

## Advertise stable client entry points explicitly

Use the **same** deployment-level advertisement on every API replica, including
replicas that do not serve the advertised role:

```toml
[api.advertised.admin]
uri = "https://management.example.com/team/admin/graphql"
disclose = false

[api.advertised.storefront]
uri = "https://shop.example.com/team/storefront/graphql"
disclose = true

[api.advertised.git_http]
uri = "https://git.example.com/team"
disclose = true
```

The configuration keys are `api.advertised.{admin,storefront,git_http}.uri`;
their environment equivalents are `GITSTORE_API__ADVERTISED__ADMIN__URI`,
`GITSTORE_API__ADVERTISED__STOREFRONT__URI` and
`GITSTORE_API__ADVERTISED__GIT_HTTP__URI`.

Values must be complete HTTP(S) URLs without credentials, queries or fragments;
explicit ports must be between 1 and 65535.
GraphQL URLs include their fixed prefixed path. Proxy TLS termination and
deployment path prefixes are supported; configure the proxy to remove only
its deployment prefix. Empty URLs mean unconfigured/undisclosed. Disclosure
defaults to **false**, including Admin; opting into disclosure requires a URL.
Configuring a private Admin URL does not make it public.

No URL is derived from a bind port, request Host or forwarded headers.
The reusable disclosed configuration contains no catalog or Git RPC target.
This configuration does not add a discovery endpoint or change playground
credential handling. Advertising a cross-origin endpoint never authorizes
clients to forward existing credentials; credential audiences and login
flows must be configured separately.

Compare the `endpoint_config_digest` startup log value across replicas before
routing traffic. It hashes configured URLs and disclosure flags, not local
roles, transient health or internal RPC addresses. A mismatch is a deployment
configuration error: correct it before rollout. Old binaries ignore these
new settings and do not enforce role isolation. Upgrade them under their
existing combined deployment first; only then change role-specific routing.
Do not use a role-specific readiness check or remove legacy process listeners
until that process is upgraded. Rollback restores the preceding combined
configuration and routing together. The earlier unprefixed GraphQL endpoint
is not restored by this feature.

Git remains exactly one active process per deployment, with retained
repository storage and non-overlapping replacement. Role decomposition does
not add Git replication, sharding, autoscaling or zero-downtime Git failover.

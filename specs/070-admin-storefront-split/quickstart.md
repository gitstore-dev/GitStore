# Implementation and Validation Quickstart

Use this implementation and validation procedure for the endpoint split. Run commands from the repository root on `070-admin-storefront-split`. Reuse the root Makefile; no new public feature-specific commands are required. Do not change or commit unrelated worktree edits.

## 1. Contract tests before implementation

Write focused failing tests first in the existing API app/config/security suites and controller schema-validation suites. Cover fixed listener paths, unavailable envelope, schema/ID/APQ isolation, port validation/cleanup, playground switching, configurable CORS and Admin authorization/replay. Reuse integration fixtures/race patterns for two APIs/two controllers and one Git process. First complete the mechanical move to `internal/admin/graph/`, update imports/bindings and compile/regenerate before changing runtime behavior.

The initial boundary contract is [storefront.graphqls](contracts/storefront.graphqls). It must validate independently; the later source split extracts its Node interface to common rather than declaring it twice. No production ready/enablement test switch is permitted.

## 2. Generate and run normal checks

Rename the Admin config to `gqlgen.admin.yml`; generation directives must explicitly select it and `gqlgen.storefront.yml`. Both generator configurations and source sets are explicit:

```sh
make generate TARGET=all
make test TARGET=all
make check TARGET=all
```

Generation changes must be deterministic. Check both generated schemas and verify controller validation includes Admin plus common only. Existing Make test selectors are `all`, `datastore`, `secret-integration`; do not invent `TARGET=api` as a supported public test selector.

## 3. Probe fixed listener routing

Start the normal deployment using documented credentials/configuration. Defaults use Admin port 4000 and Storefront port 4001:

```sh
curl -i http://localhost:4000/health
curl -i http://localhost:4000/ready
curl -i -H 'Content-Type: application/json' \
  --data '{"query":"{ __typename }"}' http://localhost:4000/admin/graphql
curl -i -H 'Content-Type: application/json' \
  --data '{"query":"{ __typename }"}' http://localhost:4001/storefront/graphql
curl -i http://localhost:4000/graphql
```

Admin preserves its authentication requirements; use normal credential acquisition for authorized management probes without logging tokens. Storefront returns 503 with `STOREFRONT_UNAVAILABLE` and no-store. `/graphql` and the opposite endpoint's prefixed path return 404 on each listener. Admin `/ready` can remain ready while Storefront is unavailable.

## 4. Configure ports, CORS and playground switching

Example configuration:

```toml
[api]
admin_port = 4100
storefront_port = 4200

[api.admin.cors]
allowed_origins = ["http://localhost:4200"]
allow_credentials = false

[api.storefront.cors]
allowed_origins = ["http://localhost:4100"]
allow_credentials = false
```

The paths remain `http://localhost:4100/admin/graphql` and `http://localhost:4200/storefront/graphql`; Admin root `/health`, `/ready` and existing compatibility `/metrics` move with `admin_port`. Zero/equal ports and Git HTTP/catalog collisions fail before serving. Generic `api.port` is removed and rejected, not translated. Confirm the fixed contract in [endpoints.md](contracts/endpoints.md).

Open `/admin/playground` or `/storefront/playground` on its listener. Select the other endpoint and explicitly enter its browser-accessible URL if not known. The CORS example permits those cross-origin playground requests without cookies; same-origin access requires no allow-list. Verify switching clears credentials/custom headers, closes old subscriptions, changes schema/editor session and does not run a document automatically. With empty CORS lists, cross-origin requests must fail while same-origin playground use works. Storefront requests remain unavailable regardless of the selector.

Do not add `api.storefront.enabled` or a shared-routing setting. Until real publication/target support exists, the Storefront data endpoint remains unavailable.

## 5. Exercise evidence paths

Existing supported smoke and connected-workload commands, requiring their usual explicit owned fixtures/topology:

```sh
make capacity TARGET=api PROFILE=readiness MODE=diagnostic
make capacity TARGET=repository PROFILE=lifecycle MODE=alpha
```

The endpoint-split dispatcher profile is registered:

```sh
make capacity TARGET=api PROFILE=endpoint-split MODE=diagnostic
make capacity TARGET=api PROFILE=endpoint-split MODE=alpha
```

Boundary acceptance requires the declared retained dataset and verified matching builds. Successful public-read acceptance also requires real publication dependencies; unavailable-boundary observations cannot count as successful public reads. Follow the connected-driver prerequisites in `tests/capacity/README.md`.

Fault injection requires an explicitly owned target and confirmation:

```sh
make chaos CHAOS_PROFILE=api-restart \
  CHAOS_TARGET=gitstore-capacity-api-a CHAOS_CONFIRM=1
```

Verify the target actually belongs to this test run before using the example. Require 30-second API readiness and 60-second observer recovery, including durable cursor verification and every acknowledged change. Preserve unrelated containers and the retained catalog/Git storage.

## 6. Release/cutover and final gates

Update local defaults and external Admin/quickstart consumers before cutover. Controllers, Makefile targets, CLI and Admin clients use a complete `/admin/graphql` URL, including WebSocket watches. Native Make startup derives its default health URL by replacing the full `/admin/graphql` suffix with `/health`, preserving custom ports and proxy prefixes. Set `API_HEALTH_URL` explicitly when operational routing differs; `/admin/health` is incorrect. Upgrade/rollback named ports, API, clients and routing together without mixed old/new routing or alias fallback. New instruments use OpenTelemetry Metrics through shared observability; compatibility scrape output is not a reason to add direct Prometheus collectors.

The implementing PR must update endpoint/security documentation, generated artifacts, ADR-0012 acceptance status/index and spec closure. Acceptance date follows the ADR lifecycle rules, not the planning date. Run the complete default `make pr-ready` before creating that PR. Refresh the AST graph after code changes if the graph tool is available. Do not claim unrun capacity, publication support or adversarial WebSocket frame bounds.

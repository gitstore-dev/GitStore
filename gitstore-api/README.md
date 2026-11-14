# API

Go API service for GitStore. It is the public front door for GraphQL and Git Smart HTTP, and it is also the CatalogService gRPC server called by `gitstore-git-service` during push validation and admission.

## Purpose

`gitstore-api` owns:

- GraphQL queries for namespaces, repositories, products, product variants, categories, and collections.
- Control-plane mutations for authentication, namespaces, and repositories.
- Git Smart HTTP routing for `git clone`, `git fetch`, and `git push`.
- CatalogService gRPC handlers for `ValidateResources` and `AdmitResources`.
- Datastore access through the `memdb` and ScyllaDB backends.
- Authentication, request middleware, health checks, and generated gqlgen wiring.

Catalogue writes are primarily Git-driven. Product and CategoryTaxonomy also expose
Git-backed GraphQL create/update/delete operations; consult the
[API reference](../docs/api-reference.md#catalogue-writes) for the supported
per-kind write surface rather than assuming all catalogue kinds expose CRUD.

Post-receive admission is operation-aware for current Git-backed catalog resources: `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection`. The API compares old/new commit state, treats `apiVersion` + `kind` + namespace + `metadata.name` as identity, stores file path as provenance, deletes removed identities, preserves UID on path moves, and skips stale admissions when the ref has already advanced.

## Boundaries

- Admin clients use port `4000`; Storefront uses port `4001`.
- Git clients use this service's Smart HTTP port `9000` in the default Compose deployment.
- `gitstore-git-service` calls this service's CatalogService gRPC endpoint on port `6000`.
- This service calls `gitstore-git-service` through GitService gRPC on port `50051`.
- This service owns datastore persistence for control-plane records and admitted catalogue projections.

## Ports And Paths

The table uses listener/container ports. Compose publishes Storefront's 4001
listener on host port 4100; native defaults use 4001 directly.

|   Port | Path                                       | Purpose                             |
|-------:|--------------------------------------------|-------------------------------------|
| `4000` | `/admin/graphql`                                 | Admin GraphQL API                   |
| `4000` | `/admin/playground`                              | GraphQL Playground                  |
| `4000` | `/health`                                  | Liveness health check               |
| `4000` | `/ready`                                   | Readiness check                     |
| `4001` | `/storefront/graphql` | Public boundary (503 until publication is implemented) |
| `4001` | `/storefront/playground` | Public API explorer |
| `9000` | `/{namespace}/{repo}.git/info/refs`        | Git Smart HTTP ref advertisement    |
| `9000` | `/{namespace}/{repo}.git/git-upload-pack`  | Git fetch/clone                     |
| `9000` | `/{namespace}/{repo}.git/git-receive-pack` | Git push                            |
| `6000` | gRPC                                       | CatalogService validation/admission |

## Project Structure

```text
gitstore-api/
├── cmd/
│   ├── server/       # API server entrypoint
│   └── gitctl/       # operator CLI: auth config, secrets, and datastore repair
├── gen/              # Generated protobuf Go code
├── internal/
│   ├── app/          # Runtime composition
│   ├── auth/         # Session and JWT logic
│   ├── catalog/      # Catalogue resource structs
│   ├── cataloggrpc/  # CatalogService gRPC server
│   ├── config/       # Configuration loading
│   ├── datastore/    # Datastore abstraction and backends
│   ├── gitclient/    # GitService gRPC client
│   ├── githttp/      # Git Smart HTTP handler
│   ├── admin/graph/  # Admin resolvers and generated code
│   ├── storefront/   # Isolated public boundary and generated schema
│   ├── health/       # Health and readiness handlers
│   ├── middleware/   # HTTP middleware
│   └── validate/     # Frontmatter validation
├── tests/            # Contract tests
├── generate.go       # gqlgen directive
├── gqlgen.admin.yml  # Admin generation
├── gqlgen.storefront.yml # Separate Storefront generation
└── go.mod
```

## Configuration Highlights

Admin and Storefront have separate named listeners with fixed prefixed paths.
`api.admin_port` defaults to 4000; `api.storefront_port` defaults to 4001.
The generic `api.port` and old `/graphql`/`/playground` routes are removed.
Storefront has a separate generated schema but returns a fixed no-store 503
until publication and target binding are implemented. Admin authentication and
durable watches retain their existing contracts; introspection requires login.
The playground selector accepts an explicit peer URL and clears headers on
switch. Configure independent CORS policies to use it across listeners; see
[configuration](../docs/configuration.md).

Upgrade API images, controller/CLI/Admin clients and proxy routes together.
Remove generic port settings and configure complete `/admin/graphql` URLs,
including WebSocket clients. Keep health checks at the Admin root. Roll back
images, client URLs and routes together, preserving authored data and Git
storage. Mixed old/new routing is unsupported. Coordinate the standalone CLI, external Admin and
quickstart consumers before release.

Pass `--config-file PATH` to require and load an explicit TOML file. Environment
variables override file values. Repeat the flag to layer additive overlays on
top of a base file — `--config-file base.toml --config-file overlay.toml` —
each later file is merged on top of the previous ones (later file wins per
key), so an overlay only needs to declare the keys it changes. Root
`make compose` uses the shared development-only `config/config.toml` and needs
no API `.env` file.

Required for local API startup unless provided by `.env`:

| Variable                                        | Default                  | Purpose                  |
|---------------------------------------------------|--------------------------|--------------------------|
| `GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE`  | `users.yaml`             | Local users YAML         |
| `GITSTORE_API__AUTH__JWT__SECRET`               | unset                    | JWT signing secret       |
| `GITSTORE_API__ADMIN_PORT`                            | `4000`                   | GraphQL HTTP port        |
| `GITSTORE_API__GIT_PORT`                        | `9000`                   | Git Smart HTTP port      |
| `GITSTORE_API__GRPC_PORT`                       | `6000`                   | CatalogService gRPC port |
| `GITSTORE_API__GIT_SERVICE__URI`                | `dns:///localhost:50051` | GitService gRPC target   |
| `GITSTORE_API__DATASTORE__BACKEND`              | `memdb`                  | `memdb` or `scylla`      |
| `GITSTORE_LOG__LEVEL`                           | `info`                   | Log level                |
| `GITSTORE_LOG__FORMAT`                          | `json`                   | `json` or `text`         |

Copy the example file for local development:

```bash
cp gitstore-api/.env.example gitstore-api/.env
# The sample points at the tracked development fixture in config/users.yaml.
```

## Commands

From the repository root:

```bash
make api
make dev
make compose DETACH=1
make bootstrap TARGET=all ADMIN_PASSWORD=<admin-password>
```

From this module:

```bash
go test ./...
go generate ./...
go run ./cmd/gitctl hash-password <password>
```

Scylla-backed tests:

```bash
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 \
  go test -tags scylla -v -timeout 10m ./tests/contract/datastore/... ./internal/datastore/scylla/...
```

## Deeper Docs

- [User Guide](../docs/user-guide.md)
- [Developer Guide](../docs/developer-guide.md)
- [API Reference](../docs/api-reference.md)
- [Configuration](../docs/configuration.md)
- [Push Validation](../docs/products/push-validation.md)
- [GraphQL schemas](../shared/schemas/)
- [CatalogService proto](../shared/proto/gitstore/catalog/v1/catalog_service.proto)
- [GitService proto](../shared/proto/gitstore/git/v1/git_service.proto)

## License

AGPL-3.0-or-later. See [LICENSE](../LICENSE).

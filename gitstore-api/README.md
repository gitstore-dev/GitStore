# gitstore-api

Go API service for GitStore. It is the public front door for GraphQL and Git Smart HTTP, and it is also the CatalogService gRPC server called by `gitstore-git-service` during push validation and admission.

## Purpose

`gitstore-api` owns:

- GraphQL queries for namespaces, repositories, products, product variants, categories, and collections.
- Control-plane mutations for authentication, namespaces, and repositories.
- Git Smart HTTP routing for `git clone`, `git fetch`, and `git push`.
- CatalogService gRPC handlers for `ValidateResources` and `AdmitResources`.
- Datastore access through the `memdb` and ScyllaDB backends.
- Authentication, request middleware, health checks, and generated gqlgen wiring.

Catalogue writes are Git-driven today. GraphQL catalogue CRUD is not the documented write path while Git-backed catalogue writes over GraphQL are being finalized.

## Boundaries

- Clients send GraphQL traffic to this service on port `4000`.
- Git clients send Smart HTTP traffic to this service on port `5000`.
- `gitstore-git-service` calls this service's CatalogService gRPC endpoint on port `6000`.
- This service calls `gitstore-git-service` through GitService gRPC on port `50051`.
- This service owns datastore persistence for control-plane records and admitted catalogue projections.

## Ports And Paths

| Port | Path | Purpose |
|---:|---|---|
| `4000` | `/graphql` | GraphQL API |
| `4000` | `/playground` | GraphQL Playground |
| `4000` | `/health` | Liveness health check |
| `4000` | `/ready` | Readiness check |
| `5000` | `/{namespace}/{repo}.git/info/refs` | Git Smart HTTP ref advertisement |
| `5000` | `/{namespace}/{repo}.git/git-upload-pack` | Git fetch/clone |
| `5000` | `/{namespace}/{repo}.git/git-receive-pack` | Git push |
| `6000` | gRPC | CatalogService validation/admission |

## Project Structure

```text
gitstore-api/
├── cmd/
│   ├── server/       # API server entrypoint
│   └── hashpw/       # bcrypt password hash helper
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
│   ├── graph/        # gqlgen resolvers and generated code
│   ├── health/       # Health and readiness handlers
│   ├── middleware/   # HTTP middleware
│   └── validate/     # Frontmatter validation
├── tests/            # Contract tests
├── generate.go       # gqlgen directive
├── gqlgen.yml        # gqlgen configuration
└── go.mod
```

## Configuration Highlights

Required for local API startup unless provided by `.env`:

| Variable | Default | Purpose |
|---|---|---|
| `GITSTORE_AUTH__ADMIN__USERNAME` | unset | Admin username |
| `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` | unset | bcrypt password hash |
| `GITSTORE_AUTH__JWT__SECRET` | unset | JWT signing secret |
| `GITSTORE_API__PORT` | `4000` | GraphQL HTTP port |
| `GITSTORE_API__GIT_PORT` | `5000` | Git Smart HTTP port |
| `GITSTORE_API__GRPC_PORT` | `6000` | CatalogService gRPC port |
| `GITSTORE_GIT__GRPC__URI` | `dns:///localhost:50051` | GitService gRPC target |
| `GITSTORE_DATASTORE__BACKEND` | `memdb` | `memdb` or `scylla` |
| `GITSTORE_LOG__LEVEL` | `info` | Log level |
| `GITSTORE_LOG__FORMAT` | `json` | `json` or `text` |

Copy the example file for local development:

```bash
cp gitstore-api/.env.example gitstore-api/.env
```

## Commands

From the repository root:

```bash
make api
make dev
make compose DETACH=1
make bootstrap ADMIN_PASSWORD=<admin-password>
```

From this module:

```bash
go test ./...
go generate ./...
go run ./cmd/hashpw <password>
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

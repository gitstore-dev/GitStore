# API

GraphQL API service for the GitStore platform. Provides a headless, Relay-compatible GraphQL interface for querying and managing commerce catalogues stored in Git, plus a Git Smart HTTP server for standard push/pull operations.

## Tech Stack

- **Language**: Go 1.25
- **GraphQL**: [gqlgen](https://github.com/99designs/gqlgen) v0.17.90
- **Datastore**: `go-memdb` (development) / ScyllaDB 5.x+ (production)
- **Auth**: JWT (via `golang-jwt/jwt/v5`)
- **Logging**: `go.uber.org/zap`
- **gRPC client**: connects to `gitstore-git-service` for Git operations

## Project Structure

```
gitstore-api/
├── cmd/
│   ├── server/       # Main API server entrypoint
│   └── hashpw/       # Utility to generate bcrypt password hashes
├── internal/
│   ├── auth/         # Authentication & JWT handling
│   ├── config/       # Configuration loading
│   ├── datastore/    # Datastore abstraction (memdb / ScyllaDB)
│   ├── gitclient/    # gRPC client to the git service
│   ├── githttp/      # Git Smart HTTP protocol handler
│   ├── graph/        # GraphQL resolvers and generated code
│   ├── handler/      # HTTP route handlers
│   ├── health/       # Health check endpoint
│   ├── logger/       # Structured logging setup
│   ├── middleware/   # HTTP middleware (auth, CORS, etc.)
│   ├── models/       # Domain models
│   └── validate/     # Input validation
├── gen/              # Generated protobuf Go code
├── tests/            # Integration tests
├── gqlgen.yml        # gqlgen code generation config
└── go.mod
```

## Configuration

Copy `.env.example` to `.env` and fill in the required values:

| Variable                              | Required          | Description                                       |
|---------------------------------------|-------------------|---------------------------------------------------|
| `GITSTORE_AUTH__ADMIN__USERNAME`      | Yes               | Admin username                                    |
| `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` | Yes               | Bcrypt hash (generate with `go run ./cmd/hashpw`) |
| `GITSTORE_AUTH__JWT__SECRET`          | Yes               | Min 32-char random string for JWT signing         |
| `GITSTORE_GIT__GRPC__URI`             | Yes (has default) | gRPC address of the git service                   |
| `GITSTORE_API__GIT_PORT`              | No                | Git Smart HTTP listen port (default: 5000)        |
| `GITSTORE_API_PORT`                   | No                | GraphQL API listen port (default: 4000)           |
| `GITSTORE_DATASTORE__BACKEND`         | No                | `memdb` (default) or `scylla`                     |
| `GITSTORE_LOG__LEVEL`                 | No                | `debug`, `info`, `warn`, `error`                  |

See `.env.example` for the full list including ScyllaDB options.

## Running

From the repository root:

```bash
# Run the API service (requires .env or shell env vars)
make api

# Or run both services together
make dev
```

## Development

```bash
# Run tests
cd gitstore-api && go test ./...

# Regenerate GraphQL code after schema changes
go generate ./...

# Generate a password hash for local development
go run ./cmd/hashpw <password>
```

## API Endpoints

| Port | Path                                       | Description                      |
|------|--------------------------------------------|----------------------------------|
| 4000 | `/graphql`                                 | GraphQL API                      |
| 4000 | `/playground`                              | GraphQL Playground (development) |
| 4000 | `/health`                                  | Health check                     |
| 5000 | `/{namespace}/{repo}.git/info/refs`        | Git Smart HTTP info/refs         |
| 5000 | `/{namespace}/{repo}.git/git-upload-pack`  | Git fetch/clone                  |
| 5000 | `/{namespace}/{repo}.git/git-receive-pack` | Git push                         |

## License

AGPL-3.0-or-later — see [LICENCE](../LICENSE).

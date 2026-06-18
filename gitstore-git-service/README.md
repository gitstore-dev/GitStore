# gitstore-git-service

Rust Git storage and transport service for GitStore. It is gRPC-only and owns bare repository data on disk.

## Purpose

`gitstore-git-service` owns:

- Bare Git repository lifecycle and filesystem storage.
- GitService gRPC operations used by `gitstore-api`.
- Ref advertisement, receive-pack, and upload-pack primitives.
- File read/list operations used by API admission.
- Receive hook phases.
- CatalogService callouts to the API for validation and admission.

It does not expose public Git HTTP endpoints. Git clients enter through `gitstore-api` on Git Smart HTTP port `5000`.

## Boundaries

- `gitstore-api` calls this service through GitService gRPC on port `50051`.
- This service stores repositories below `GITSTORE_GIT__DATA_DIR`.
- During push hooks, this service calls `gitstore-api` CatalogService gRPC on port `6000`.
- Catalogue parsing and datastore persistence are API responsibilities.
- Post-receive admission callouts include the ref name, old commit SHA, and new commit SHA so the API can derive creates, updates, deletes, moves, and stale-admission skips.

## Configuration Highlights

| Variable | Default | Purpose |
|---|---|---|
| `GITSTORE_GRPC__PORT` | `50051` | GitService gRPC listen port |
| `GITSTORE_GIT__DATA_DIR` | `/data/repos` | Bare repository root |
| `GITSTORE_GIT__REPO__MAX_FILE_SIZE` | `52428800` | Per-file size limit |
| `GITSTORE_GIT__MAX_PACK_SIZE_BYTES` | `52428800` | Pack size limit |
| `GITSTORE_CATALOG_SERVICE__URI` | `http://localhost:6000` | API CatalogService target |
| `GITSTORE_LOG__LEVEL` | `info` | Log level |
| `GITSTORE_LOG__FORMAT` | `json` | `json` or `text` |

Hook and admission settings are loaded from defaults, optional `gitstore.toml`, and environment variables. See [docs/configuration.md](../docs/configuration.md).

## Project Structure

```text
gitstore-git-service/
├── src/
│   ├── main.rs        # Server entrypoint
│   ├── config.rs      # Layered configuration
│   ├── git/
│   │   ├── repo.rs        # Repository storage
│   │   ├── pack_server.rs # Pack protocol logic
│   │   ├── hooks/         # Receive hook pipeline and callouts
│   │   └── metrics.rs
│   └── grpc/
│       ├── server.rs      # GitService implementation
│       └── metrics.rs
├── gen/               # Generated Rust proto bindings
├── tests/integration/ # Integration tests
├── build.rs           # Documents generated-proto workflow
└── Cargo.toml
```

## Commands

From the repository root:

```bash
make git
make dev
make compose DETACH=1
```

From this module:

```bash
cargo test
cargo test --test integration
cargo build --release
```

## gRPC API

Canonical contract: [shared/proto/gitstore/git/v1/git_service.proto](../shared/proto/gitstore/git/v1/git_service.proto).

Primary RPC groups:

| Group | RPCs |
|---|---|
| Repository lifecycle | `CreateRepository`, `DeleteRepository` |
| Git Smart HTTP primitives | `InfoRefs`, `ReceivePack`, `UploadPack` |
| Reads | `GetFile`, `GetFileStream`, `ListFiles` |
| Writes | `CommitFile`, `DeleteFile` |
| Tags | `CreateTag`, `ListTags`, `GetLatestTag` |

## Storage

Repositories are stored as bare repositories below `GITSTORE_GIT__DATA_DIR`. The API resolves human-readable namespace/repository paths to stable repository IDs before calling this service.

## Deeper Docs

- [Developer Guide](../docs/developer-guide.md)
- [Configuration](../docs/configuration.md)
- [Push Validation](../docs/products/push-validation.md)
- [GitService proto](../shared/proto/gitstore/git/v1/git_service.proto)
- [CatalogService proto](../shared/proto/gitstore/catalog/v1/catalog_service.proto)

## License

AGPL-3.0-or-later. See [LICENSE](../LICENSE).

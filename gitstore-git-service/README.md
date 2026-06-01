# Git Service

Git repository storage and transport service for the GitStore platform. Provides gRPC endpoints for Git pack protocol operations (push, fetch, ref advertisement) with hook extension points and admission control.

## Tech Stack

- **Language**: Rust (edition 2021, MSRV 1.82)
- **Git library**: [gix](https://github.com/GitoxideLabs/gitoxide) 0.84.0 (pure-Rust Git implementation)
- **Async runtime**: Tokio 1.35
- **gRPC**: Tonic 0.14 + Prost 0.14
- **Logging**: `tracing` 0.1
- **Configuration**: `config` crate + `dotenvy`
- **CLI**: `clap` 4.4

## Project Structure

```
gitstore-git-service/
├── src/
│   ├── main.rs        # Server entrypoint and CLI args
│   ├── lib.rs         # Library root
│   ├── config.rs      # Configuration loading
│   ├── git/
│   │   ├── mod.rs         # Git module root
│   │   ├── repo.rs        # Repository management
│   │   ├── pack_server.rs # Pack protocol handling
│   │   ├── hooks.rs       # Git hook execution
│   │   └── metrics.rs     # Git operation metrics
│   └── grpc/
│       ├── mod.rs         # gRPC module root
│       ├── server.rs      # gRPC service implementation
│       └── metrics.rs     # gRPC metrics
├── gen/               # Generated protobuf Rust code
├── tests/
│   └── integration/   # Integration tests
├── build.rs           # Protobuf code generation build script
└── Cargo.toml
```

## Configuration

Copy `.env.example` to `.env` and adjust as needed. All settings are optional — the service starts with sensible defaults.

| Variable                            | Default       | Description                               |
|-------------------------------------|---------------|-------------------------------------------|
| `GITSTORE_GRPC__PORT`               | `50051`       | gRPC service listen port                  |
| `GITSTORE_GIT__DATA_DIR`            | `/data/repos` | Path to bare Git repository storage       |
| `GITSTORE_LOG__LEVEL`               | `info`        | `trace`, `debug`, `info`, `warn`, `error` |
| `GITSTORE_LOG__FORMAT`              | `json`        | Log format (`json` or `text`)             |
| `GITSTORE_GIT__REPO__MAX_FILE_SIZE` | `52428800`    | Max upload size in bytes (50 MiB)         |

Hook and admission-control settings can be configured via `gitstore.toml`. See `.env.example` for details.

## Running

From the repository root:

```bash
# Run the git service locally (uses GIT_DATA_DIR, default: .gitstore/repos)
make git

# Or run both services together
make dev
```

## Development

```bash
# Run tests
cd gitstore-git-service && cargo test

# Run integration tests only
cargo test --test integration

# Build in release mode
cargo build --release
```

## gRPC API

The service exposes a single gRPC service defined in `shared/proto/gitstore/git/v1/git_service.proto`:

| RPC           | Type             | Description                          |
|---------------|------------------|--------------------------------------|
| `InfoRefs`    | Unary            | Advertise refs for a repository      |
| `ReceivePack` | Client-streaming | Handle `git push` pack data          |
| `UploadPack`  | Server-streaming | Handle `git fetch`/`clone` pack data |

## Storage

Repositories are stored as **bare Git repositories** on the local filesystem under the configured `GIT_DATA_DIR`. Each repository is namespaced by owner:

```
$GIT_DATA_DIR/
└── {namespace}/
    └── {repo}.git/
```

## License

AGPL-3.0-or-later — see [LICENCE](../LICENSE).

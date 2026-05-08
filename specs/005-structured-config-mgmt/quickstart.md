# Quickstart: Structured Configuration Management

**Date**: 2026-05-08  
**Branch**: `005-structured-config-mgmt`

---

## What Changed

Both `gitstore-api` and `gitstore-git-service` now load all configuration through a single structured entry point at startup. There are no more scattered `os.Getenv` / `env::var` calls. Both services validate all config at startup and exit with a clear error message listing every missing or invalid value.

---

## gitstore-api (Go) — Quick Configuration

### Minimal local dev setup (`.env` file)

Create `.env` in the `gitstore-api/` directory (or the working directory from which you run the server):

```bash
# Required — no defaults for auth/secrets
GITSTORE_AUTH_ADMIN_USERNAME=admin
GITSTORE_AUTH_ADMIN_PASSWORD_HASH=$2a$12$...   # bcrypt hash of your password
GITSTORE_AUTH_JWT_SECRET=your-secret-key-minimum-32-characters-long

# Required — defaults to localhost ports of gitstore-git-service
GITSTORE_GIT_GRPC=localhost:50051
GITSTORE_GIT_WS=ws://localhost:8080
GITSTORE_GIT_HTTP_URL=http://localhost:9418

# Optional (defaults shown)
GITSTORE_API_PORT=4000
GITSTORE_CACHE_TTL=300
GITSTORE_LOG_LEVEL=debug
GITSTORE_AUTH_JWT_DURATION=24h
GITSTORE_AUTH_JWT_ISSUER=gitstore
```

Then run normally — the service loads `.env` automatically without any shell exports.

### Config file (optional, `config.toml`)

For non-secret values you can use a `config.toml` in the working directory:

```toml
[api]
port = 4000

[git]
ws = "ws://localhost:8080"
http_url = "http://localhost:9418"

[cache]
ttl = 300

log_level = "debug"
```

Secrets (`jwt_secret`, `admin_password_hash`) must remain in environment variables or `.env` — never in `config.toml`.

### Startup error example

If required keys are missing, the service exits immediately with a message like:

```
Failed to load configuration: invalid configuration (3 error(s)):
  Config.Auth.AdminUsername: constraint "required" violated (value: "")
  Config.Auth.AdminPasswordHash: constraint "required" violated (value: "")
  Config.Auth.JWTSecret: constraint "required" violated (value: "")
```

Git connection fields (`git.grpc`, `git.ws`, `git.http_url`) have localhost defaults and will not appear in validation errors unless explicitly set to an empty string.

### Startup log example

On successful startup, the resolved configuration is logged at INFO level:

```json
{"level":"info","msg":"Configuration loaded","config":{
  "api.port": 4000,
  "git.grpc": "localhost:50051",
  "git.ws": "ws://localhost:8080",
  "git.http_url": "http://localhost:9418",
  "auth.admin_username": "admin",
  "auth.admin_password_hash": "<redacted>",  // GITSTORE_AUTH_ADMIN_PASSWORD_HASH
  "auth.jwt_secret": "<redacted>",            // GITSTORE_AUTH_JWT_SECRET
  "auth.jwt_issuer": "gitstore",
  "auth.jwt_duration": "24h",
  "cache.ttl": 300,
  "log_level": "debug"
}}
```

---

## gitstore-git-service (Rust) — Quick Configuration

### Minimal local dev setup (`.env` file)

Create `.env` in the `gitstore-git-service/` directory (or the working directory from which you run the binary):

```bash
# Optional (defaults shown)
GITSTORE_HTTP_PORT=9418
GITSTORE_WS_PORT=8080
GITSTORE_GRPC_PORT=50051
GITSTORE_DATA_DIR=/data/repos
GITSTORE_LOG_LEVEL=debug
GITSTORE_MAX_FILE_SIZE=52428800

# Hook phase toggles and admission control must be set via gitstore.toml.
# Env var overrides for nested keys are not supported — see gitstore-git-service/.env.example.
```

All keys are optional with sensible defaults — the service starts without any configuration.

### Config file (optional, `gitstore.toml`)

```toml
http_port = 9418
ws_port = 8080
grpc_port = 50051
data_dir = "/data/repos"
log_level = "debug"
max_file_size = 52428800

[hooks.git_receive_pack]
pre_receive  = { enabled = false }
update       = { enabled = false }
post_receive = { enabled = false }
proc_receive = { enabled = false }
post_update  = { enabled = false }

[admission_control.validating_admission_policy]
phase = "pre-receive"
```

### CLI overrides

```bash
# Override config file path
./gitstore-git-service --config-file /path/to/custom.toml

# Override log level (highest priority — overrides all other sources)
./gitstore-git-service --log-level trace
```

### Startup error example

```
Configuration errors:
- http_port must be between 1 and 65535 (got: 0)
- all three ports (http_port=0, ws_port=8080, grpc_port=50051) must be distinct
```

### Startup log example

```
INFO resolved configuration: http_port=9418 ws_port=8080 grpc_port=50051 data_dir="/data/repos" log_level="info"
```

---

## Running Tests

```bash
# Go
cd gitstore-api
go test ./internal/config/...

# Rust
cd gitstore-git-service
cargo test config
```

---

## Full Configuration Reference

See [`contracts/config-schema.md`](contracts/config-schema.md) for the complete key reference, or `docs/configuration.md` in the repository root after the feature is merged.

# Shared module instructions

- Repository-local library modules belong under `shared/`; top-level Go
  modules are standalone services or tooling.
- `secretmaterial` has no third-party dependencies. Provider-owned secret
  records remain bounded in-process memory, not persisted secret caches.
  Keep its standalone `GOWORK=off` build/tests and the aggregate Go checks.
- `observability` is shared by the API and controller: configuration
  validation, bounded OTLP trace/log export, Zap bridge, Prometheus SDK reader
  and trace-context HTTP helpers. Keep fixed service identities and existing
  native Prometheus metrics. Controller spans cover individual reconciliation
  attempts, requests and WebSocket dials, not entire watches.
- Both Go modules participate in the existing build/test/lint families
  and standalone `GOWORK=off` CI checks.
- Use `make generate TARGET=<authz|all>` for authorization catalogs or all API
  generation directives. `make test` and `make check TARGET=all` reject
  authorization catalog drift without rewriting generated files.
- Keep Go and Rust protobuf stubs consistent with `proto/` definitions using
  the existing Buf generation configurations.
- GraphQL schema docstrings are consumer-facing: read
  `../.claude/rules/no-internal-refs-in-user-facing-docs.md`. After changes to
  embedded schema docstrings, run `go generate ./...` in `gitstore-api`.

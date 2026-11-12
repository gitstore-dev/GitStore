# Shared module instructions

- Repository-local library modules belong under `shared/`; top-level Go
  modules are standalone services or tooling.
- `secretmaterial` has no third-party dependencies. Provider-owned secret
  records remain bounded in-process memory, not persisted secret caches.
  Keep its standalone `GOWORK=off` build/tests and the aggregate Go checks.
- `observability` is shared by the API and controller: configuration
  validation, bounded OTLP trace/log export, Zap bridge, Prometheus SDK reader
  and trace-context HTTP helpers. All metrics, including runtime and gRPC
  instrumentation, originate from SDK instruments; Prometheus is a derived
  reader only. Keep fixed service identities and bounded operational attributes.
  Controller spans cover individual reconciliation
  attempts, requests and WebSocket dials, not entire watches.
- `scylla-cdc-go` is a production-source copy of the existing v1.2.1 dependency
  with minimal reader-shutdown and completed-query notification patches.
  Preserve its Apache-2.0 license and
  provenance in `NOTICE.md`. Native and container API builds use the same
  local replacement.
- All three Go modules participate in the existing build/test/lint families
  and standalone `GOWORK=off` CI checks.
- Use `make generate TARGET=<authz|all>` for authorization catalogs or all API
  generation directives. `make test` and `make check TARGET=all` reject
  authorization catalog drift without rewriting generated files.
- Keep Go and Rust protobuf stubs consistent with `proto/` definitions using
  the existing Buf generation configurations.
- GraphQL schema docstrings are consumer-facing: read
  `../.claude/rules/no-internal-refs-in-user-facing-docs.md`. After changes to
  embedded schema docstrings, run `go generate ./...` in `gitstore-api`.

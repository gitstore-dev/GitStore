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
  with reader-shutdown, completed-query notification and opt-in empty-range
  catch-up patches. Wider probes must never deliver changes or acknowledge
  failed queries; nonempty ranges retain the ordinary query bound.
  Opt-in stream batching reuses bounded multi-partition queries and preserves
  every stream's consumer/progress and the unchanged confidence window.
  Preserve its Apache-2.0 license and
  provenance in `NOTICE.md`. Native and container API builds use the same
  local replacement.
- All four Go modules participate in the existing build/test/lint families
  and standalone `GOWORK=off` CI checks.
- `translation` owns pure locale, field-path and target-descriptor validation
  for API and controller reuse. Keep authorization, target lookup and storage
  in the callers; retain its standalone `GOWORK=off` and aggregate checks.
- Use `make generate TARGET=<authz|all>` for authorization catalogs or all API
  generation directives. `make test` and `make check TARGET=all` reject
  authorization catalog drift without rewriting generated files.
- Keep Go and Rust protobuf stubs consistent with `proto/` definitions using
  the existing Buf generation configurations.
- Reserve GraphQL `input` arguments and `*Payload` return types for mutations.
  Queries use descriptive argument names and return their result types directly.
- GraphQL schema docstrings are consumer-facing: read
  `../.claude/rules/no-internal-refs-in-user-facing-docs.md`. After changes to
  embedded schema docstrings, run `go generate ./...` in `gitstore-api`.

# API instructions

- Use the versions pinned in `go.mod`, the workspace and container builders.
  Existing libraries include gqlgen, validator, Zap, Prometheus, CEL, JWT,
  bcrypt and Viper; reuse these rather than introducing replacements.
- `datastore.Datastore` has memdb development/contract-test and Scylla
  production backends. Preserve backend-neutral contracts.
- Reuse existing `Generation`, `ResourceVersion`, `Status json.RawMessage`,
  `DeletionTimestamp`, `Finalizers` and optimistic-concurrency
  `Update*(ctx, resource, expectedResourceVersion)` methods. Reuse indexed
  `RepositoryID`/`NamespaceID` fields and status blobs for ownership and
  existence checks instead of adding redundant schema.
- `internal/watchjournal` is the only watch mechanism. Namespaced catalog
  kinds register `{kind, table, payload}` sources in
  `internal/datastore/scylla/catalog_cdc.go` (`RunCatalogCDC`), not new runners.
  Typed and generic watches fail closed without a journal; kinds without a
  source return `UNSUPPORTED_KIND`. Never reintroduce process-local event fan-out.
- Preserve durable journal behavior across replicas. Scylla authoritative
  watched tables use full-preimage/postimage CDC with 14-day TTL and the shared
  `resource_watch_clock`/`resource_watch_events` journal; memdb implements the
  same journal contract in-process.
- Read `../.claude/rules/scylla-migrations.md` before changing either datastore
  backend. Tables follow `<plural>_by_<key>`; never rewrite applied migration
  history. Use the existing projection audit/repair helpers.
- Materialize related-resource aggregates asynchronously in controller
  `status.resolved`; never add a live aggregate query to a GraphQL resolver.
- Reuse `internal/admission` diagnostics/`RejectedError` and
  `internal/admissionreport` response extensions.
- API/controller telemetry uses `../shared/observability`; read its scoped
  instructions before changing exporters or instrumentation. Preserve native
  Prometheus metrics.
- Run `make generate TARGET=<authz|proto|all>` for committed authorization
  catalogs or protobufs. Changes to GraphQL schema docstrings require
  `go generate ./...` in this module because generated sources embed the schema.
- See `../tests/AGENTS.md` for datastore contracts and
  `../tests/capacity/AGENTS.md` for deployed performance, profiling and replica
  acceptance work.

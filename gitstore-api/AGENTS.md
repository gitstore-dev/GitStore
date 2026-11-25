# API instructions

- Use the versions pinned in `go.mod`, the workspace and container builders.
  Existing libraries include gqlgen, validator, Zap, Prometheus, CEL, JWT,
  bcrypt and Viper; reuse these rather than introducing replacements.
- `datastore.Datastore` has memdb development/contract-test and Scylla
  production backends. Preserve backend-neutral contracts.
- Development-only profiles/providers follow the constitution's replica
  exemption: do not require shared durable state, distributed coordination,
  failover or rolling upgrades from memdb, rbac-local, static-users AuthN/UserDir,
  development notification sinks or other declared development providers.
  Preserve useful in-process concurrency tests and focused checks of shared
  production code; production-capable provider contracts remain unchanged.
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
- Preserve durable journal behavior across production replicas. Scylla authoritative
  watched tables use full-preimage/postimage CDC with 14-day TTL and the shared
  `resource_watch_clock`/`resource_watch_events` journal; memdb implements the
  same journal contract in-process.
- Read `../.claude/rules/scylla-migrations.md` before changing either datastore
  backend. Tables follow `<plural>_by_<key>`; never rewrite applied migration
  history outside an explicitly reviewed alpha re-baseline. Query-first permits
  database indexes/views and explicit tables; justify each access pattern.
  Reuse projection audit/repair helpers for bounded automatic convergence with
  zero routine operator projection work; existing manual-only paths are tracked
  gaps, not evidence of implemented self-healing. Do not assume an immutable
  pre-GA ScyllaDB version; record and verify the actual tested build/capabilities.
- Materialize related-resource aggregates asynchronously in controller
  `status.resolved`; never add a live aggregate query to a GraphQL resolver.
- Reuse `internal/admission` diagnostics/`RejectedError` and
  `internal/admissionreport` response extensions.
- API/controller telemetry uses `../shared/observability`; read its scoped
  instructions before changing exporters or instrumentation. Record metrics
  through SDK instruments and derive compatibility scraping from SDK collection.
- Run `make generate TARGET=<authz|all>` for committed authorization
  catalogs or all API generation directives. Changes to GraphQL schema
  docstrings require `go generate ./...` in this module because generated
  sources embed the schema.
- See `../tests/AGENTS.md` for datastore contracts and
  `../tests/capacity/AGENTS.md` for deployed performance, profiling and replica
  acceptance work.
- Projection maintenance reuses `gitctl scylla-projection-audit|repair`.
  `--kind Translation --namespace-uid <uid>` selects bounded server-resumed
  turns (`--page-size 1..256`, positive `--timeout`); audit/dry-run only previews
  discovery, while `--confirm` performs one turn. Scanned is not certification.
  `--audit-scope indexes` is the default; `targets` uses independent
  coverage-epoch-fenced target backfill/verification. Proved writers preserve
  coverage without stationary material versions; unproved writes invalidate it.
  Saved Verified progress is not positive material, current coverage or
  deployment capability; recheck the coverage epoch.

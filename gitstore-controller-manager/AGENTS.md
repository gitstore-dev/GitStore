# Controller manager instructions

- Reuse the established generic interfaces:
  `internal/types.Reconciler`/`ReconcileResult`,
  `internal/status.StatusClient`/`StatusPatch`,
  `internal/listwatch.ListWatcher[T]`/`Watcher[T]`/`Runner[T]`/`WatchEvent[T]`,
  `internal/cache.Cache[T]`/`CacheAccessor[T]`/`EventHandler[T]` and
  `internal/manager.Manager`.
- Production controllers use `internal/listwatch.PagedListWatcher[T]`,
  `internal/checkpoint.DiskStore` and error-returning
  `internal/cache.LookupFunc[T]`. Full-snapshot/memory adapters remain for
  compatibility fixtures, not production scale.
- Use `internal/graphqlclient.Client` for `POST /graphql` and
  `graphql-transport-ws` subscriptions against the API's gqlgen
  `transport.Websocket`.
- State belongs in `<Kind>.disk-v2` on the existing per-replica checkpoint
  volume. Retain legacy JSON checkpoints but do not load them; first upgrade
  streams a cold list. Never overlap processes on one checkpoint directory.
- Pending work, retry deadlines, quarantine, relation counts and bounded
  fan-out cursors are durable. Preserve lookup I/O errors instead of treating
  them as resource absence.
- Materialize counts, sums and other related-resource aggregates into
  `status.resolved`; GraphQL resolvers must not compute live aggregates.
- Reuse the pinned rate limiter, Pond worker pools, Backoff and LevelDB.
  Verify concurrent-replica behavior, conflicts and rolling recovery without
  weakening ownership or optimistic-concurrency checks.
- Read `../shared/AGENTS.md` for telemetry and secret material. Controller
  spans cover reconciliation attempts, HTTP requests and WebSocket dials,
  not whole watch lifetimes.
- For native startup/enrollment read `../scripts/AGENTS.md`; for deployed
  recovery, sustained load and profiling read `../tests/capacity/AGENTS.md`.

# Feature planning instructions

- Follow repository-wide architecture invariants when writing specs, plans
  and tasks: bounded work, pluggable AuthN/AuthZ, concurrent API/controller
  replica correctness and rolling upgrades. Git remains singleton-only;
  replication, placement-aware routing and autoscaling are not implied scope.
- Reuse existing component interfaces and fields rather than planning
  parallel abstractions. Read the relevant component `AGENTS.md` first.
- `/speckit.plan` and `/speckit.tasks` must explicitly include the ADR status
  flip when implementing an ADR's core decision. That same PR changes
  `Proposed` to `Accepted (YYYY-MM-DD)`, and closes the implemented spec.
  Superseding an ADR updates its status and `docs/ADRs/README.md` together.
- Keep repository-wide instructions concise. Put detailed commands, feature
  history and technology notes in the appropriate scoped instructions or
  feature artifacts, not the root `AGENTS.md`.

## Inherited planning baseline

The former root file's baseline was dated 2026-03-26. These notes preserve
planning context, not a substitute for checking current module manifests,
container builders and the applicable plan.

- Spec 064 planned canonical actions, bounded allow-only v1beta1 RBAC
  snapshots, name-only ServiceAccounts, signed identity migration, replica/
  load validation and cluster-scoped ServiceAccount storage. The planning
  change itself did not implement these decisions or add dependencies.
- The earlier baseline used Go 1.26.0 in API/controller modules and 1.26.1
  container builders, gqlgen v0.17.90, validator, Zap, UUID, encoding/json,
  Prometheus v1.23.2, CEL, JWT v5.3.1, bcrypt and Viper.
- Controllers add x/time, Pond v2.7.1, Backoff v5.0.3 and LevelDB v1.0.0.
  The reusable reconciler/status/cache/manager interfaces began in 026;
  generic list/watch in 036; the GraphQL HTTP/WebSocket client in 039.
  Production projections/work/indexes are disk-backed and bounded.
- The Rust baseline was >=1.88 with gix 0.89.0, gix-ref 0.69.0,
  gix-pack 0.76.0, Tokio 1.35, Tonic 0.14, tracing, anyhow, async-trait,
  serde, HMAC 0.13, SHA-2 0.11 and getrandom 0.4.
- Storage uses bare Git repositories, memdb v1.3.5 for development/
  contracts and gocqlx/v3 v3.0.4 plus gocql for Scylla production storage.
  Shared RPC auth uses `GITSTORE_GRPC_AUTH__HMAC_SECRET`.
- Earlier deltas: 028 branch-deletion admission (no new Go dependency);
  029 Rust config 0.15.22 and regex 1; 033 `cmd/gitctl` replaces `cmd/hashpw`;
  035 Gin, go-grpc-prometheus and Repository push-policy fields; 048
  query-specific projection tables with the memdb contract backend.
- Resource metadata/CAS fields were established by Repository 045/058 and
  Namespace 046; ownership/existence checks reuse indexed IDs/status
  (039-042, 062). The per-resource baseline spans migrations
  `001_infra.cql` through `009_service_account.cql`.
- 057 added `category_ancestor_index` in migration `010`, maintained by
  taxonomy status updates/final removal and repaired through
  `gitctl scylla-projection-audit|repair`. It reused admission diagnostics
  and the admission-report response extension. Its planning baseline was
  Go 1.25 with existing dependencies and a Rust golden-fixture parity test.
- 063 introduced the dependency-free repository-local `shared/secretmaterial`
  module; its historical baseline was Go 1.25 and container builders 1.26.1.
- Namespace 050, Product 055, Repository 058, File and CategoryTaxonomy
  use the durable Scylla CDC-backed watch journal, not process-local fan-out.
  The local `shared/scylla-cdc-go` production copy preserves v1.2.1
  provenance and contains the minimal reader-shutdown patch.

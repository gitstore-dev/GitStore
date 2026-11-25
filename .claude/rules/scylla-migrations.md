---
paths:
  - "gitstore-api/internal/datastore/scylla/**"
  - "gitstore-api/internal/datastore/memdb/**"
---

# Scylla schema migrations (`gitstore-api/internal/datastore/scylla/migrations`)

`gocqlx/migrate` applies the embedded `*.cql` files in lexical order and records
each file's name and checksum in `gocqlx_migrate`. The runner refuses a keyspace
whose recorded history no longer matches.

Unless changes to existing checksums are explicitly approved, never edit an
applied migration. Add incremental files for schema changes and preserve the
supported upgrade path. Keyspaces already provisioned with explicitly approved
updated files do not require conversion or reseeding. A breaking baseline reset
requires a fresh keyspace and reseeding, not an edited migration ledger.
Never delete deployment data implicitly.

Alpha storage re-baselining may deliberately replace this historical layout,
including removing redundant tables, under an explicitly reviewed baseline
change. Specify retained-data conversion or an explicitly approved fresh
keyspace, verification and recovery before changing the baseline. Do not edit
recorded checksums, reseed an existing deployment or drop authority implicitly.
Until that re-baseline is implemented, the bundle below remains the current
schema, not a requirement to preserve every table forever.

## Baseline

`001`–`009` are a one-time, per-resource baseline that replaced the earlier
incremental history (`001_initial_schema.cql` … `014_file_watch_cdc.cql`).
Keyspaces created from the old history must be recreated; there is no upgrade
path across the baseline.

| File                        | Contents                                                                                                                                  |
|-----------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| `001_infra.cql`             | Shared resource watch journal (`resource_watch_clock`, `resource_watch_events`), `owner_reference_dependents`, `auth_session_revocations` |
| `002_namespace.cql`         | `namespaces_by_*`, `namespace_mappings*`                                                                                                  |
| `003_repository.cql`        | `repositories_by_*`                                                                                                                       |
| `004_category_taxonomy.cql` | `category_taxonomies_by_*`                                                                                                                |
| `005_product.cql`           | `products_by_*`                                                                                                                           |
| `006_product_variant.cql`   | `product_variants_by_*`                                                                                                                   |
| `007_collection.cql`        | `collections_by_*`                                                                                                                        |
| `008_file.cql`              | `files_by_*`                                                                                                                              |
| `009_service_account.cql`   | `service_accounts_by_*`, `service_account_assertion_replays`                                                                              |

Existing incremental files after `009` remain part of the bundle.
"One file per resource" describes the baseline only; absent explicit
checksum-change approval, add incremental files rather than rewriting history.

## Conventions

- Tables are named `<plural resource>_by_<key>`.
- Each kind has one authoritative table holding the full resource envelope;
  the others are narrow lookup/list projections.
  - Namespace is cluster-scoped, so its authoritative table is `namespaces_by_uid`.
  - Repository's list projection `repositories_by_namespace` is partitioned by
    `(namespace, bucket)` to bound partition size, so its single authoritative
    row is `repositories_by_uid`.
  - Product uses `products_by_key`, keyed by the complete resource identity
    `((namespace, creation_timestamp, uid))`, with resource-sized name
    reservations and thin 64-shard namespace listings.
  - Other namespaced catalog kinds use `<plural>_by_namespace`, keyed
    `((namespace), creation_timestamp, uid)`, with `_by_uid` as a lookup.
- A watched kind enables full-image CDC
  (`cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}`)
  on its authoritative table only, and is registered as a source in
  `catalog_cdc.go` (namespaced catalog kinds) or its own runner (Namespace,
  Repository). Watched kinds share the `resource_watch_*` journal; never add a
  per-kind journal table. Controller leases belong in their own table, not in
  `resource_watch_clock`.
- Query-first is mechanism-neutral. Evaluate materialized views, global/local
  secondary indexes and explicit query tables per access pattern, independently
  or with their dependencies. Justify consistency, ordering, fan-out, partition
  size, write amplification, memory/disk and operator cost; do not assume manual
  projections are faster or database-managed structures supply uniqueness.
- Separate rebuildable projections from authority, reservations/claims, fences,
  intents and retirement evidence. Automatic repair must use verified authority
  and conditional identity/version/fence checks, never arbitrary conflict
  resolution or a stale projection as proof that no blocker exists.
- Require zero routine operator projection maintenance. Reuse existing
  audit/repair logic behind bounded, durable automatic startup/resume, detected-
  drift and recurring verification paths, or database-managed convergence.
  Missing, stale, dangling and duplicate derived entries must recover without
  manual commands or restart rituals. Keep progress, backpressure and readiness
  observable; block unsafe reads/writes while evidence is incomplete.
- Prefer bounded pages and persisted progress/work over catalogue-sized memory
  indexes; additional disk is acceptable when it reduces memory/operational cost.
  Diagnostics and exceptional recovery tools may remain, but are not the normal
  convergence path. Preserve true authoritative conflicts and alert rather than
  silently deleting a winner or accepted data.
- Do not assume a fixed ScyllaDB version before GA. A design may select required
  database capabilities and revise the tested build; record exact build/config
  in reproducible checks. Do not claim all-version compatibility or relax
  correctness to retain an incidental development image version.
- `gocqlx/migrate` splits files on `;` and silently skips any statement that
  starts with `--`. End a comment block that precedes a statement with `;`, and
  never put `;` inside a comment line. `TestMigrationStatementsSurviveGocqlxSplitting`
  enforces this.

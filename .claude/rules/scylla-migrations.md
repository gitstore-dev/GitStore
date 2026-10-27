---
paths:
  - "gitstore-api/internal/datastore/scylla/**"
  - "gitstore-api/internal/datastore/memdb/**"
---

# Scylla schema migrations (`gitstore-api/internal/datastore/scylla/migrations`)

`gocqlx/migrate` applies the embedded `*.cql` files in lexical order and records
each file's name and checksum in `gocqlx_migrate`. The runner refuses a keyspace
whose recorded history no longer matches.

Until the project leaves beta, explicitly approved breaking schema changes may
replace baseline definitions. They require a fresh keyspace and reseeding;
never rewrite an existing keyspace's migration ledger to disguise the break.
Do not retain legacy tables or add transfer machinery solely for compatibility
with disposable development data. Never delete deployment data implicitly.

After beta, an applied file must never be edited. Schema changes must use new
incremental files and preserve the supported upgrade path.

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

Existing incremental files (`010_…`, `011_…`) remain part of the bundle.
"One file per resource" describes the baseline only; the lifecycle policy above
governs whether a new change replaces a baseline or adds an incremental file.

## Conventions

- Tables are named `<plural resource>_by_<key>`.
- Each kind has one authoritative table holding the full resource envelope;
  the others are narrow lookup/list projections.
  - Namespace is cluster-scoped, so its authoritative table is `namespaces_by_uid`.
  - Repository's list projection `repositories_by_namespace` is partitioned by
    `(namespace, bucket)` to bound partition size, so its single authoritative
    row is `repositories_by_uid`.
  - Product uses `products_by_key`, keyed by the complete resource key
    `((namespace, creation_timestamp, uid))`; `products_by_identity` reserves
    `((namespace, name))`, and `products_by_namespace_shard` is a thin
    64-shard listing projection. `products_by_uid` is a locator. Never restore
    namespace-wide full-envelope or name-reservation Product partitions.
  - Other namespaced catalog kinds use `<plural>_by_namespace`, keyed
    `((namespace), creation_timestamp, uid)`, with `_by_uid` as a lookup.
- A watched kind enables full-image CDC
  (`cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}`)
  on its authoritative table only, and is registered as a source in
  `catalog_cdc.go` (namespaced catalog kinds) or its own runner (Namespace,
  Repository). Watched kinds share the `resource_watch_*` journal; never add a
  per-kind journal table. Controller leases belong in their own table, not in
  `resource_watch_clock`.
- No secondary indexes or materialized views: query-first tables cover every
  access pattern.
- `gocqlx/migrate` splits files on `;` and silently skips any statement that
  starts with `--`. End a comment block that precedes a statement with `;`, and
  never put `;` inside a comment line. `TestMigrationStatementsSurviveGocqlxSplitting`
  enforces this.

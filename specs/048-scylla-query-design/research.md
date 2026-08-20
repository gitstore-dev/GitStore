# Research: Scylla Query and Recovery Hardening

## R1. Query-first Repository storage

**Decision**: Replace the global Repository partition and secondary-index reads with explicit `by_id`, Namespace/month listing, global/month listing, path mapping, and reverse-mapping projections.

**Rationale**: ScyllaDB data modeling starts from access patterns. A direct read should supply the complete partition key, and rows that grow over time require bounded partitions. This mirrors the Namespace design already present on the source branch.

**Alternatives considered**:
- Keep `bucket = "all"`: rejected because all Repository writes target one partition.
- Keep secondary indexes: rejected because primary routing and authorization paths should not depend on index fan-out.
- Materialized views: rejected because application-owned projections provide explicit failure and recovery behavior.

**Sources**:
- https://docs.scylladb.com/stable/get-started/data-modeling/query-design.html
- https://www.scylladb.com/2024/10/31/nosql-data-modeling/

## R2. Bucket size and pagination

**Decision**: Use calendar-month (`YYYY-MM`) buckets for Repository global and Namespace-scoped listing projections, with clustering order `(creation_timestamp DESC, uid DESC)`.

**Rationale**: Monthly buckets match the existing Namespace contract, make cross-bucket keyset pagination reusable, and bound partition growth. Production monitoring must validate a 100 MB hard ceiling and 10 MB hot-partition soft target.

**Alternatives considered**:
- Unbucketed Namespace partitions: rejected because large tenants grow without bound.
- Weekly buckets: deferred until measured write volume proves monthly buckets too large.
- Hash buckets: rejected because global chronological pagination becomes more expensive and less predictable.

**Sources**:
- https://docs.scylladb.com/stable/get-started/data-modeling/query-design.html
- https://docs.scylladb.com/manual/stable/troubleshooting/large-partition-table.html

## R3. Uniqueness enforcement

**Decision**: Reserve every unique name, UID, SKU, and Repository path with `INSERT ... IF NOT EXISTS`; inspect the applied result and owning identity.

**Rationale**: Read-before-write checks race. LWT provides one linearizable winner and is safe to retry when the reservation stores the stable resource identity.

**Alternatives considered**:
- Application mutexes: rejected because they do not coordinate multiple API replicas.
- Logged batches: rejected because uniqueness keys span partitions and batches do not provide cross-partition compare-and-set.
- Accept last-write-wins: rejected because it silently changes resource identity.

**Sources**:
- https://docs.scylladb.com/manual/stable/features/lwt.html
- https://www.scylladb.com/2020/07/15/getting-the-most-out-of-lightweight-transactions-in-scylla/

## R4. Cross-partition mutation fan-out

**Decision**: Replace logged batches with deterministic, individually awaited, idempotent statements. Use compensation when a later step fails.

**Rationale**: Logged batches add batchlog overhead but do not create cross-partition transactions. Individual statements make partial progress explicit, testable, and recoverable.

**Alternatives considered**:
- Multi-partition logged batches: rejected because they add latency and still expose eventual replay windows.
- Unlogged batches: rejected because they still permit partial application and obscure which projection failed.
- New distributed transaction service: rejected by simplicity and operational cost.

**Sources**:
- https://docs.scylladb.com/manual/stable/cql/dml/batch.html
- https://docs.datastax.com/en/cql-oss/3.x/cql/cql_reference/cqlBatch.html

## R5. Compensation and retry

**Decision**: Capture prior state, make every projection statement idempotent, compensate reversible pre-commit steps in reverse order, and roll projections forward after an authoritative version has committed. Condition every cleanup on the same resource identity or expected resource version.

**Rationale**: Application-owned denormalization cannot be rolled back atomically. Conditional compensation prevents a retry from deleting another writer's valid reservation. Once an authoritative update commits, restoring older projection values would increase inconsistency; recovery must converge projections on the committed version instead.

**Alternatives considered**:
- Fire-and-forget repair: rejected because failure can be lost on process restart.
- New persistent outbox/controller: rejected for this feature because compensation plus operator-visible repair satisfies the issue scope without a new subsystem.
- Silent best-effort writes: rejected because read paths can contradict each other indefinitely.

## R6. Repository rename and transfer

**Decision**: Model both operations as a saga: reserve target path, update authoritative Repository with expected resource version, then remove the old path conditionally.

**Rationale**: The target path must be protected before ownership changes. Stable Repository identity makes every step retryable, while conditional cleanup prevents removal of a mapping reassigned by another operation.

**Alternatives considered**:
- Delete old mapping first: rejected because the Repository becomes temporarily unreachable and target reservation may fail.
- Update Repository first: rejected because readers may observe a path with no mapping.
- Cross-partition batch: rejected for the reasons in R4.

## R7. Tombstones, compaction, and repair cadence

**Decision**: Keep the default 10-day `gc_grace_seconds`, require repair completion within 7 days, monitor large partitions and tombstones, retain general-purpose compaction by default, and prohibit TWCS for delete-heavy tables.

**Rationale**: Tombstones removed before repair can resurrect deleted data. Compaction must match measured table behavior; premature strategy changes can increase write amplification or strand tombstones.

**Alternatives considered**:
- `gc_grace_seconds = 0`: rejected because it risks resurrection after replica downtime.
- TWCS for all time-bucket tables: rejected because Repository and catalogue tables support explicit deletes and updates.
- Immediate LCS adoption: deferred until read/write amplification measurements justify it.

**Sources**:
- https://cassandra.apache.org/doc/latest/cassandra/managing/operating/repair.html
- https://cassandra.apache.org/doc/latest/cassandra/managing/operating/compaction/stcs.html
- https://cassandra.apache.org/doc/latest/cassandra/managing/operating/compaction/lcs.html
- https://cassandra.apache.org/doc/latest/cassandra/managing/operating/compaction/twcs.html
- https://docs.scylladb.com/manual/stable/kb/ttl-facts.html

## R8. Projection repair and observability

**Decision**: Detect and signal dangling projections in normal reads and mutations, provide a documented audit/reconciliation procedure, and reuse existing logging, metrics, and datastore tooling rather than adding a controller.

**Rationale**: The issue requires recoverability and operator control, not a new always-on subsystem. A runbook plus deterministic datastore reconciliation can repair cold rows that foreground read repair never encounters.

**Alternatives considered**:
- Controller-manager poison queue: useful for reconciliation failures but rejected for raw datastore projection repair because no durable repair intent currently crosses the API/controller boundary.
- Database anti-entropy repair alone: rejected because replica repair does not infer relationships between independently denormalized tables.
- Ignore dangling rows: rejected because silent skipping hides correctness loss.

**Sources**:
- https://cassandra.apache.org/doc/latest/cassandra/managing/operating/repair.html
- https://docs.scylladb.com/manual/stable/architecture/anti-entropy/read-repair.html

## R9. Product label selectors

**Decision**: Do not implement the current materialization proposal. Record decision inputs and revisit only after Product and Collection controllers ship.

**Rationale**: Controller ownership, selector cardinality, and update patterns are not yet measured. Choosing a projection now would be speculative and violate the issue's explicit deferral.

**Alternatives considered**:
- Implement the existing document immediately: rejected because the user has not accepted it as the final design.
- External search now: rejected because it introduces a new service without proven need.
- Inverted tables now: rejected until actual controller and query behavior is available.

## R10. Canonical storage naming

**Decision**: Use `uid` for a resource's stable UUID, `repository_id` for a reference to a Repository, and `namespace` for the immutable Namespace name. Use `namespace_uid` only if a query explicitly requires the Namespace UUID. Rename query tables accordingly, including `namespaces_by_uid`, `repositories_by_uid`, and `namespace_mappings_by_repository`.

**Rationale**: Catalogue entities and GraphQL metadata already use UID terminology, catalogue tables already store the Namespace name in `namespace`, and provenance fields already use `repository_id`. A column named `namespace` must not sometimes contain a name and sometimes a UUID. Namespace names are immutable resource identity components, so they are safe query keys for Repository ownership and path access.

**Alternatives considered**:
- Keep `namespace_id`: rejected because it creates a Repository-only vocabulary and obscures whether the value is a Relay ID, UUID, or name.
- Rename `namespace_id` to `namespace` while retaining UUID values: rejected because it would make the same column name carry different semantics across tables.
- Use `namespace_uid` everywhere: rejected because all other catalogue access patterns are name-scoped and would require unnecessary UID resolution.

## R11. Manifest body persistence

**Decision**: Add raw Markdown `body` to the authoritative Namespace and Repository entities and `*_by_uid` tables. Treat body changes as author-owned generation changes. Keep listing projections narrow and hydrate bodies from authoritative UID rows.

**Rationale**: Both public resource contracts already expose a Markdown body and the shared parser already separates body bytes from frontmatter. Discarding that value makes Namespace and Repository behavior inconsistent with Product, Collection, CategoryTaxonomy, and ProductVariant. Storing body only once avoids multiplying potentially large Markdown content across monthly listing projections.

**Alternatives considered**:
- Continue returning an empty body: rejected because it violates the manifest and GraphQL contracts.
- Duplicate body into every listing projection: rejected because it increases partition size and write amplification without serving a distinct access pattern.
- Re-read body from Git on every query: rejected because datastore reads are the consumer-facing authoritative projection and must not depend on Git availability.

## R12. Uniform authoritative resource envelope

**Decision**: Normalize every full manifest-backed resource row to one physical superset with the same column names and compatible types: `api_version`, `kind`, optional `namespace`, `uid`, `name`, `generation`, `resource_version`, `revision`, `creation_timestamp`, `creation_actor`, `update_timestamp`, `update_actor`, `labels`, `annotations`, `owner_references`, `finalizers`, `deletion_timestamp`, optional `repository_id`, `source_path`, `git_commit_sha`, `git_ref`, `spec`, `body`, and `status`. Persist null or empty values where a field has no current semantics.

**Rationale**: These fields represent the same Kubernetes-style resource envelope for every kind. Today Namespace and Repository omit most of them, Collection and CategoryTaxonomy omit owner references, and existing resources abbreviate `owner_references` as `owner_refs`. Uniform persistence removes read-time synthesis, makes admission/recovery generic, and lets contract tests verify one invariant across resource kinds.

**Alternatives considered**:
- Preserve resource-specific subsets: rejected because the public contracts expose the same envelope and omitted fields are silently lost or synthesized.
- Store one opaque resource JSON document: rejected because query-first access, conditional version updates, and operational diagnostics require typed identity/version/provenance columns.
- Duplicate the full envelope into every lookup projection: rejected because query projections should remain narrow and hydrate authoritative rows.

**Explicit exceptions**:
- Namespace is cluster-scoped and therefore has no parent `namespace` or authoring `repository_id` column.
- Lifecycle and audit fields remain present but nullable where a resource has no current writer for them.
- Resource-specific query columns remain denormalized additions.

# Datastore Resource Envelopes and Projections

[Storage groups](../storage-groups/README.md) describe where desired state,
runtime records, and payloads belong. This page describes how the datastore
represents resource envelopes and serves indexed reads.

## Authoritative envelopes

Namespace and Repository authoritative UID rows persist the complete applicable
author and system state, including raw Markdown body content. GraphQL Relay
`id` values are encoded API-boundary identifiers; datastore identity is the
resource `uid`.

Canonical datastore naming is consistent across resource kinds:

- `uid` is the resource's stable identity.
- `repository_id` references a Repository UID.
- `namespace` is the immutable Namespace name, never its UUID.
- `owner_references` contains canonical JSON.

Namespace omits parent `namespace` and `repository_id`. Direct lookups and
connection reads return the same Namespace and Repository body unchanged.
Changes to their author metadata, spec, or body advance `generation`;
status-only changes do not.

## Query projections and pagination

Namespace name lookups, Repository path mappings, and monthly listing buckets
are narrow projections containing the keys needed for an access pattern.
They hydrate the authoritative row before returning a resource. Repair
restores projections from that row without overwriting its body, desired state,
audit fields, or status.

Namespace and Repository connections use bounded keyset pagination over monthly
projections and hydrate only the requested page. They do not expose
`totalCount`: exact counting would require an unbounded historical scan.
Fetch-all-then-sort is not the pagination model.

### Product partitions

Product envelopes reside in `products_by_key`, with the complete
`(namespace, creation_timestamp, uid)` key forming one partition.
`products_by_identity` reserves names in resource-sized partitions;
`products_by_uid` locates an envelope.

`products_by_namespace_shard` contains listing keys across 64 stable shards.
Forward and backward pages merge ordered shards and hydrate a window of at
most 16 rows, with budgets of at most 256 projection queries and 4096 envelope
lookups. Missing or mismatched rows are skipped within those budgets. Errors
or budget exhaustion fail the page rather than returning partial success.

Both backends retain a default Product page size of 100 and enforce a maximum
of 250 in either direction before query sizing or merge allocation. Negative
sizes are rejected.

## Product CDC and durable watches

Full-image Product CDC watches `products_by_key`. Every resource transition
in a bounded multi-row CDC frame must enter the shared durable watch journal
before the source position is checkpointed.

The pinned CDC reader's shutdown handling cancels the generation fetcher as
well as stream workers on cancellation or consumer error. This prevents a
queued-generation deadlock during reader replacement. Native and container
API builds use the same local module, with standalone race regressions for
these shutdown paths.

## Namespace catalog fences

Catalog and parent lifecycle mutations share a non-expiring, exact-epoch
reservation in `namespace_catalog_fences_by_uid`. Coordination writes do not
produce Namespace CDC. Repository creation retains its independent Namespace
counter.

Ambiguous outcomes retain the catalog reservation. Repair requires quiescent
writers and the audited epoch and pending value; a stale repair plan cannot
clear a newer holder. See [projection repair](../runbooks/scylla-projection-repair.md)
for operational safeguards.

## Schema compatibility

The schema baseline is defined by `002_namespace.cql` and `005_product.cql`.
Keyspaces already provisioned from those definitions do not need conversion
or reseeding. Read-only schema validation rejects incompatible recorded
histories; it does not reset data, transfer it automatically, or rewrite the
migration ledger.

Binaries using the previous Product or Namespace-fence schema cannot be mixed
with these writers. Bounded partitions, pagination, and reader shutdown do
not by themselves establish old-schema rolling compatibility or production
capacity acceptance.

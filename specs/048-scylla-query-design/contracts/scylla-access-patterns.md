# Contract: Scylla Access Patterns

## Namespace

- UID lookup reads one authoritative UUID partition.
- Name lookup reads one name projection, then hydrates the authoritative UUID row.
- Global listing reads bounded monthly partitions and merges stable keyset order.
- Existing Namespace secondary indexes remain absent.

## Repository

| Operation | Required projection | Query contract |
|-----------|---------------------|----------------|
| Get by UID | `repositories_by_uid` | One partition-key lookup |
| Get by Namespace/name | `namespace_mappings` then `repositories_by_uid` | One path partition lookup plus one UID lookup |
| Reverse path lookup | `namespace_mappings_by_repository` | One Repository-UID lookup |
| List by Namespace | `repositories_by_namespace` | Bounded month partitions, server-side limit, stable keyset |
| Global list | `repositories_by_bucket` | Bounded month partitions, server-side limit, stable keyset |

Repository listing projections contain ordering keys and UID only. Each returned entry is hydrated from `repositories_by_uid`, including the manifest body.

## Pagination

- Sort key is `(creation_timestamp DESC, uid DESC)`.
- Forward and backward pagination MUST preserve global order across buckets.
- Cursors MUST be stable across rename and transfer.
- Empty buckets MUST be skipped without changing page semantics.
- Requested page work MUST be bounded by page size plus fixed look-ahead/bucket overhead.
- `PageResult.TotalCount` MUST be `-1` when an exact count would require an unbounded historical-bucket scan.

## Prohibited query shapes

- `ALLOW FILTERING`.
- Primary reads through secondary indexes.
- Fetch-all then sort/page in application memory.
- A sentinel global partition containing every Repository.

## Canonical column semantics

- `uid`: the stable UUID owned by the row's resource.
- `repository_id`: a foreign reference to a Repository UUID.
- `namespace`: the immutable Namespace name.
- `namespace_uid`: a Namespace UUID only where the UUID itself is required.

The same column name MUST NOT carry a Namespace name in one table and a Namespace UUID in another.

## Authoritative envelope contract

Authoritative rows conform to [resource-storage-envelope.md](resource-storage-envelope.md). Narrow query projections are exempt from the full physical superset but MUST hydrate the authoritative row before returning a complete resource.

## Manifest body

- `namespaces_by_uid.body` and `repositories_by_uid.body` contain raw Markdown after frontmatter.
- Name and listing projections MUST NOT duplicate body content.
- Direct and paginated reads MUST hydrate and return the authoritative body.
- Body changes MUST affect generation; status-only writes MUST preserve body.

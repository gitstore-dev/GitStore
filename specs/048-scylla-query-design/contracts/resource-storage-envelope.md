# Contract: Canonical Resource Storage Envelope

## Scope

This contract applies to every authoritative Scylla row for Product, ProductVariant, Collection, CategoryTaxonomy, Namespace, and Repository. Lookup, reservation, bucket, and mapping projections are intentionally narrow and are excluded.

## Required physical columns

| Column | CQL type | Null/empty rule |
|--------|----------|-----------------|
| `api_version` | `text` | Required |
| `kind` | `text` | Required |
| `namespace` | `text` | Required except on Namespace |
| `uid` | `uuid` | Required |
| `name` | `text` | Required |
| `generation` | `bigint` | Required |
| `resource_version` | `text` | Required |
| `revision` | `text` | Nullable before Git admission |
| `creation_timestamp` | `timestamp` | Required |
| `creation_actor` | `text` | Nullable when Git provenance is the only actor record |
| `update_timestamp` | `timestamp` | Nullable until the first update |
| `update_actor` | `text` | Nullable when Git provenance is the only actor record |
| `labels` | `map<text,text>` | Empty map permitted |
| `annotations` | `map<text,text>` | Empty map permitted |
| `owner_references` | `text` | Canonical JSON; empty list permitted |
| `finalizers` | `list<text>` | Empty list permitted |
| `deletion_timestamp` | `timestamp` | Nullable |
| `repository_id` | `uuid` | Required except on Namespace |
| `source_path` | `text` | Nullable before Git admission |
| `git_commit_sha` | `text` | Nullable before Git admission |
| `git_ref` | `text` | Nullable before Git admission |
| `spec` | `text` | Canonical JSON; empty object permitted |
| `body` | `text` | Empty string permitted |
| `status` | `text` | Canonical JSON; empty object permitted |

## Structural exceptions

- Namespace is cluster-scoped, so it has no parent `namespace`.
- Namespace has no authoring `repository_id`; its system-repository location is fixed by the Namespace admission contract.
- No other resource kind may omit a canonical column. An inapplicable value is represented as null or the documented empty value.

## Naming and type rules

- Resource identity is `uid`, never `id`.
- A Repository UUID reference is `repository_id`, never `repo_id`.
- `namespace` always contains the immutable Namespace name, never a Namespace UUID.
- Owner references use `owner_references` in CQL and `OwnerReferences` in Go, never `owner_refs`.
- Identically named canonical columns use identical CQL and Go types across resource kinds.
- Resource-specific denormalized columns may be added but may not replace canonical fields.

## Read/write behavior

- Create writes every required envelope value or its documented null/empty representation.
- Manifest update preserves system-owned fields and advances generation only when author-owned metadata, spec, or body changes.
- Status update preserves author-owned fields and generation.
- Complete API reads hydrate the authoritative row; narrow projections never synthesize missing envelope values.

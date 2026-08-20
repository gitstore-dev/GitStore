# Data Model: Scylla Query and Recovery Hardening

## 0. Canonical authoritative resource envelope

All full manifest-backed resource rows share this envelope:

| Field | Type | Semantics |
|-------|------|-----------|
| `api_version` | text | Manifest API version |
| `kind` | text | Resource kind |
| `namespace` | text | Immutable Namespace name; omitted for Namespace itself |
| `uid` | uuid | Stable resource identity |
| `name` | text | Immutable resource name within its scope |
| `generation` | bigint | Author-owned desired-state generation |
| `resource_version` | text | Optimistic-concurrency version |
| `revision` | text | Last admitted Git revision |
| `creation_timestamp` | timestamp | Resource creation time |
| `creation_actor` | text | Authenticated creator when applicable |
| `update_timestamp` | timestamp | Last mutation time when applicable |
| `update_actor` | text | Authenticated last mutator when applicable |
| `labels` | map<text,text> | Author-owned labels |
| `annotations` | map<text,text> | Author-owned annotations |
| `owner_references` | text | Canonical JSON owner-reference list |
| `finalizers` | list<text> | System lifecycle finalizers |
| `deletion_timestamp` | timestamp | Requested deletion time |
| `repository_id` | uuid | Authoring Repository UID |
| `source_path` | text | Manifest path in the authoring Repository |
| `git_commit_sha` | text | Admitted commit SHA |
| `git_ref` | text | Admitted Git ref |
| `spec` | text | Canonical JSON desired state |
| `body` | text | Raw Markdown after frontmatter |
| `status` | text | Canonical JSON observed state |

**Authoritative rows**:
- Product: existing Namespace-listing row remains authoritative.
- ProductVariant: existing Namespace-listing row remains authoritative.
- Collection: existing Namespace-listing row remains authoritative.
- CategoryTaxonomy: existing Namespace-listing row remains authoritative.
- Namespace: `namespaces_by_uid`.
- Repository: `repositories_by_uid`.

**Extensions**:
- Resource-specific query fields supplement the envelope.
- Fields with no current semantics for a kind remain null or empty.
- Namespace omits parent `namespace` and authoring `repository_id`; every other authoritative kind uses the full physical superset.
- Lookup, bucket, and mapping tables are intentionally narrow projections, not authoritative resource rows.

## 1. Repository authoritative record

### `repositories_by_uid`

One full Repository row per stable Repository UUID.

**Partition key**: `uid`

**Fields**:
- all canonical envelope fields applicable to a namespace-scoped resource
- `default_branch`
- `storage_class`
- `max_pack_size_bytes`
- `max_file_size_bytes`

**Rules**:
- Direct Repository lookup reads this table.
- Resource-version updates use a conditional write against this row.
- Rename and transfer do not change `uid` or `creation_timestamp`.
- Deletion removes this row only after dependent projections have been removed.
- The Go datastore entity mirrors these names as `UID`, `Namespace`, and `RepositoryID` where applicable; public GraphQL Relay IDs remain an API-boundary encoding.
- `body` stores raw Markdown after frontmatter and participates in generation-change detection.

## 2. Repository listing projections

### `repositories_by_namespace`

Ordered Repository listing for one Namespace and one calendar month.

**Partition key**: `(namespace, bucket)`

**Clustering columns**: `(creation_timestamp DESC, uid DESC)`

**Projection fields**: enough data to hydrate the Repository without a full-table scan; authoritative fields are read by UID.

The projection remains narrow: `namespace`, `bucket`, `creation_timestamp`, and `uid`. Page results are hydrated from `repositories_by_uid`, including `body`.

Exact total count is intentionally unknown (`PageResult.TotalCount = -1`) when computing it would require scanning all historical buckets.

### `repositories_by_bucket`

Globally ordered Repository listing for one calendar month.

**Partition key**: `bucket`

**Clustering columns**: `(creation_timestamp DESC, uid DESC)`

### Bucket

**Format**: `YYYY-MM` in UTC, derived from immutable `creation_timestamp`.

**Rules**:
- Bucket never changes during rename or transfer.
- Pagination merges ordered rows across adjacent buckets.
- A cursor carries creation timestamp and stable Repository UID; bucket is derived from the timestamp.

## 3. Repository mapping projections

### `namespace_mappings`

Unique path reservation and lookup.

**Partition key**: `namespace`

**Clustering key**: `name`

**Fields**:
- `namespace`
- `name`
- `repository_id`

**Rules**:
- Create/rename/transfer reserves with `IF NOT EXISTS`.
- A repeated reservation for the same `repository_id` is idempotent.
- A reservation owned by another `repository_id` returns `ErrAlreadyExists`.
- Conditional delete removes the row only when `repository_id` still matches.

### `namespace_mappings_by_repository`

Direct reverse lookup by stable Repository identity.

**Partition key**: `repository_id`

**Fields**:
- `repository_id`
- `namespace`
- `name`

**Rules**:
- Exactly one active reverse mapping is expected.
- Mismatch with the authoritative Repository produces a consistency finding.

## 4. Namespace identity projections

### `namespaces_by_uid`

The authoritative Namespace table uses `uid` as its partition key and contains the canonical envelope except parent `namespace` and authoring `repository_id`. It includes `body text` for raw Markdown after frontmatter. Name lookup remains in `namespaces_by_name`, whose value column is also `uid`. `namespaces_by_bucket` remains a narrow ordering projection and hydrates the authoritative row.

**Naming rules**:
- Resource-owned UUID: `uid`.
- Reference to a Repository UUID: `repository_id`.
- Immutable Namespace name: `namespace`.
- Namespace UUID, only when unavoidable: `namespace_uid`.

### Manifest body

**Fields**:
- `body`: raw Markdown after frontmatter; empty string is a valid body.

**Rules**:
- Stored only on authoritative Namespace and Repository UID rows.
- Returned unchanged by direct and paginated reads.
- Included in spec-generation comparison.
- Never modified by status writers or projection repair unless restoring from the authoritative row.

## 5. Catalogue uniqueness reservations

Existing `_by_name`, `_by_uid`, and `_by_sku` projections serve as uniqueness reservations.

**Reservation identity**:
- Product/Collection/CategoryTaxonomy name: `(namespace, name) → uid`
- Product/ProductVariant UID: `uid → namespace, creation_timestamp`
- ProductVariant SKU: `(namespace, sku) → uid`

**States**:
- **Absent**: identity is available.
- **Owned**: reservation points to the same stable UID; retry may continue.
- **Conflicting**: reservation points to a different UID; return `ErrAlreadyExists`.
- **Dangling**: reservation exists but the authoritative row is absent; emit a consistency finding and follow repair procedure.

## 6. Mutation state transitions

### Create

```text
Unreserved
  → ReservationsOwned
  → AuthoritativeWritten
  → ProjectionsWritten
  → Complete
```

Failure transitions:
- Before reservation: no compensation.
- After reservation: conditionally release reservations owned by the operation.
- After authoritative write: delete the newly written authoritative row only when it still matches the creating identity/version, then release reservations.
- Compensation failure: `RepairRequired`.

### Update

```text
Current
  → AuthoritativeVersionAdvanced
  → ProjectionsUpdated
  → Complete
```

Failure transitions:
- Stale resource version: `Conflict`, no projection writes.
- Projection failure: retry and roll missing/stale projections forward from the committed authoritative version.
- Roll-forward failure: `RepairRequired`, carrying the committed authoritative version and failed projection.

### Delete

```text
Current
  → ProjectionsRemoved
  → ReservationsReleased
  → AuthoritativeDeleted
  → Complete
```

Failure transitions:
- Before authoritative deletion: recreate removed projections from retained authoritative state.
- Stale resource version: `Conflict`.
- Compensation failure: `RepairRequired`.

### Repository rename/transfer

```text
CurrentPath
  → TargetReserved
  → AuthoritativeUpdated
  → OldPathRemoved
  → Complete
```

Failure transitions:
- Target conflict: `AlreadyExists`.
- Authoritative conflict/failure: remove target reservation.
- Old-path removal failure: desired path remains authoritative; old path is marked stale and removed on retry/repair.

## 7. Consistency finding

An observed mismatch between an authoritative row and a query projection.

**Attributes**:
- resource kind
- stable resource identity
- projection/table
- lookup key
- operation
- finding type: `missing`, `dangling`, `duplicate`, `stale`
- detection timestamp
- repair outcome when attempted

Findings are operational signals, not persisted domain resources in this feature.

## 8. Validation invariants

- One stable resource identity owns each unique reservation.
- Every active Repository has one authoritative row, one reverse mapping, one active path mapping, one Namespace listing row, and one global listing row.
- Listing projection bucket equals the UTC month of immutable creation time.
- No compensation may delete a row owned by a different stable identity.
- A successful mutation leaves all required projections present and stale projections absent.
- A failed mutation either restores the prior valid state or emits `RepairRequired`.

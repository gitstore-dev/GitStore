# Data Model: Product Resource Contract — Kubernetes-style Frontmatter Schema

**Branch**: `014-product-frontmatter` | **Phase**: 1 | **Date**: 2026-06-01

## Overview

The Product resource uses a **single-store read model**:

| Layer                        | Owner  | Content                                                                                          |
|------------------------------|--------|--------------------------------------------------------------------------------------------------|
| Git (`.md` blob)             | Author | `apiVersion`, `kind`, writable `metadata`, `spec`, markdown body — source of truth for authoring |
| Datastore (ScyllaDB / memdb) | System | Full hydrated view: all metadata (including system-assigned), spec, status, markdown body        |

After a push is accepted, the system parses the git content and writes the complete hydrated `Product` record to the datastore. **All reads go directly to the datastore** — no git blob lookups, no merge at read time. Git is the authoritative source for what the author wrote; the datastore is the authoritative source for what consumers read.

---

## Author-Supplied Git Content (YAML Frontmatter)

The following types describe what an author writes in a `.md` file. They are used by the admission validator and the post-push ingest pipeline.

### `ProductResource` (top-level envelope — parsed from git)

| Field        | Type          | Required | Constraint                             |
|--------------|---------------|----------|----------------------------------------|
| `apiVersion` | `string`      | Yes      | Must be `catalog.gitstore.dev/v1beta1` |
| `kind`       | `string`      | Yes      | Must be exactly `Product`              |
| `metadata`   | `ObjectMeta`  | Yes      | See ObjectMeta table                   |
| `spec`       | `ProductSpec` | Yes      | See ProductSpec table                  |

### `ObjectMeta` (author-writable fields only)

| Field          | Type                | Required | Constraint                                                                  |
|----------------|---------------------|----------|-----------------------------------------------------------------------------|
| `name`         | `string`            | Yes      | Unique within namespace; slug format (lowercase alphanumeric + hyphens)     |
| `namespace`    | `string`            | No       | Inherited from push context when absent                                     |
| `generateName` | `string`            | No       | Prefix for system-generated names; mutually exclusive with `name`           |
| `labels`       | `map[string]string` | No       | Keys: max 63 chars per segment, 253 chars with prefix. Values: max 63 chars |
| `annotations`  | `map[string]string` | No       | Arbitrary string key-value pairs                                            |

**Read-only fields (system-assigned on ingest — MUST NOT appear in git file):**

| Field               | Type               | Description                                                           |
|---------------------|--------------------|-----------------------------------------------------------------------|
| `uid`               | `string` (UUID)    | Immutable unique identifier assigned on first admission               |
| `resourceVersion`   | `string`           | Opaque token for optimistic concurrency; updated on every spec change |
| `generation`        | `int64`            | Incremented on every accepted spec write                              |
| `creationTimestamp` | `RFC3339 datetime` | Set once on first admission                                           |
| `revision`          | `string`           | Git revision, e.g. `main@sha1:a1b2c3d`                                |
| `ownerReferences`   | `[]OwnerReference` | System-set ownership links                                            |

### `OwnerReference`

| Field        | Type     | Required |
|--------------|----------|----------|
| `apiVersion` | `string` | Yes      |
| `kind`       | `string` | Yes      |
| `name`       | `string` | Yes      |
| `uid`        | `string` | Yes      |

### `ProductSpec`

| Field         | Type                        | Required | Constraint                                                     |
|---------------|-----------------------------|----------|----------------------------------------------------------------|
| `title`       | `string`                    | No       | Product display name; max 200 chars                            |
| `categoryRef` | `ObjectReference`           | No       | Exactly one category per product                               |
| `tags`        | `[]string`                  | No       | Free-form keywords; empty list is valid                        |
| `media`       | `[]MediaDefinition`         | No       | Ordered list of media slots                                    |
| `options`     | `[]ProductOptionDefinition` | No       | Variant dimensions; each `name` must be unique within the list |

### `ObjectReference`

| Field             | Type     | Required | Constraint                                             |
|-------------------|----------|----------|--------------------------------------------------------|
| `apiVersion`      | `string` | No       |                                                        |
| `kind`            | `string` | No       |                                                        |
| `name`            | `string` | Yes      |                                                        |
| `namespace`       | `string` | No       |                                                        |
| `uid`             | `string` | No       |                                                        |
| `resourceVersion` | `string` | No       |                                                        |
| `fieldPath`       | `string` | No       | JSON/Go field access path, e.g. `spec.mediaFrom{name}` |

### `MediaDefinition`

| Field     | Type            | Required |
|-----------|-----------------|----------|
| `fileRef` | `FileReference` | Yes      |

### `FileReference`

| Field      | Type     | Required | Default |
|------------|----------|----------|---------|
| `name`     | `string` | Yes      |         |
| `kind`     | `string` | Yes      |         |
| `optional` | `bool`   | No       | `false` |

### `ProductOptionDefinition`

| Field    | Type       | Required | Constraint                             |
|----------|------------|----------|----------------------------------------|
| `name`   | `string`   | Yes      | Unique within `spec.options` list      |
| `title`  | `string`   | No       | Display label                          |
| `values` | `[]string` | No       | May be empty for open-ended dimensions |

---

## System-Managed Fields (written to datastore after admission)

### `ProductStatus`

System-assigned after push acceptance. Stored in the datastore as part of the full hydrated product record.

| Field                 | Type                        | Description                                    |
|-----------------------|-----------------------------|------------------------------------------------|
| `observedGeneration`  | `int64`                     | The `metadata.generation` this status reflects |
| `lastAppliedRevision` | `string`                    | e.g. `main@sha1:a1b2c3d`                       |
| `conditions`          | `[]Condition`               | Fixed enumeration — see Condition table        |
| `resolved`            | `ResolvedProductDefinition` | System-computed aggregates                     |

### `Condition`

| Field                | Type               | Constraint                                                                                                   |
|----------------------|--------------------|--------------------------------------------------------------------------------------------------------------|
| `type`               | `string` (enum)    | One of: `Published`, `AdmissionAccepted`, `CategoryResolved`, `OptionsAccepted`, `VariantsResolved`, `Ready` |
| `status`             | `string` (enum)    | One of: `True`, `False`, `Unknown`                                                                           |
| `observedGeneration` | `int64`            |                                                                                                              |
| `lastTransitionTime` | `RFC3339 datetime` |                                                                                                              |
| `reason`             | `string`           | CamelCase short reason code                                                                                  |
| `message`            | `string`           | Human-readable description                                                                                   |

### `ResolvedProductDefinition`

| Field               | Type                         | Description                                  |
|---------------------|------------------------------|----------------------------------------------|
| `category`          | `ResolvedCategoryDefinition` | Resolved category identity and path          |
| `priceRange`        | `[]PriceRangeDefinition`     | Per-currency min/max, one entry per currency |
| `totalInventory`    | `int64`                      | Total stock across all variants              |
| `variantSummary`    | `VariantSummaryDefinition`   | Variant counts by readiness                  |
| `defaultVariantRef` | `ObjectReference`            | Reference to the default/primary variant     |
| `media`             | `[]ResolvedFileDefinition`   | Resolved media files with URLs               |

### `ResolvedCategoryDefinition`

| Field  | Type       |
|--------|------------|
| `name` | `string`   |
| `path` | `[]string` |

### `PriceRangeDefinition`

| Field          | Type               | Constraint                                                      |
|----------------|--------------------|-----------------------------------------------------------------|
| `currencyCode` | `string`           | ISO 4217                                                        |
| `min`          | `decimal.Decimal`  | `shopspring/decimal` — consistent with Decimal GraphQL scalar   |
| `max`          | `decimal.Decimal`  | `shopspring/decimal` — consistent with Decimal GraphQL scalar   |

### `VariantSummaryDefinition`

| Field         | Type    |
|---------------|---------|
| `total`       | `int64` |
| `ready`       | `int64` |
| `unavailable` | `int64` |

### `ResolvedFileDefinition`

| Field         | Type     |
|---------------|----------|
| `name`        | `string` |
| `url`         | `string` |
| `contentType` | `string` |

---

## ScyllaDB Schema — Rewritten `products` Table

The existing `products` table is replaced with a new schema. Alpha software: no production data, no migration needed. The CQL in `001_initial_schema.cql` is rewritten directly.

```sql
CREATE TABLE IF NOT EXISTS products (
    -- Partition + clustering key
    namespace          text,
    name               text,

    -- Immutable identity
    uid                uuid,
    api_version        text,
    kind               text,

    -- Versioning / optimistic concurrency
    generation         bigint,
    resource_version   text,
    creation_timestamp timestamp,
    revision           text,

    -- Author-supplied classification
    labels             map<text, text>,
    annotations        map<text, text>,

    -- Ownership (serialized JSON — rarely queried directly)
    owner_refs         text,

    -- Git provenance
    git_commit_sha     text,
    git_ref            text,

    -- Spec (complex nested structure — serialized JSON)
    spec               text,

    -- Markdown body content
    body               text,

    -- Status (system-only — serialized JSON)
    status             text,

    PRIMARY KEY (namespace, name)
) WITH CLUSTERING ORDER BY (name ASC);

-- Secondary index for UID lookups (global ID resolution)
CREATE INDEX IF NOT EXISTS products_by_uid ON products (uid);
```

**Design notes:**
- `namespace` is the partition key — all products within a namespace are co-located.
- `name` is the clustering key — unique within namespace, supports range scans and prefix queries.
- `spec` and `status` are serialised JSON blobs. Their internal structure is defined by the Go types in `contracts/go-types.md`; they are never queried column-by-column in the datastore layer.
- `body` stores the Markdown content verbatim from the git file.
- Legacy columns (`bucket`, `sku`, `title`, `price`, `currency`, `inventory_status`, `inventory_quantity`, `category_id`, `collection_ids`, `images`, `metadata`) are removed. No legacy data to preserve (alpha).

---

## memdb Schema — Rewritten `product` Table

The existing `product` table in `gitstore-api/internal/datastore/memdb/schema.go` is replaced:

```go
"product": {
    Name: "product",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
        },
        "name_namespace": {
            Name:    "name_namespace",
            Unique:  true,
            Indexer: &memdb.CompoundIndex{
                Indexes: []memdb.Indexer{
                    &memdb.StringFieldIndex{Field: "Namespace"},
                    &memdb.StringFieldIndex{Field: "Name"},
                },
            },
        },
        "namespace": {
            Name:    "namespace",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
        },
    },
},
```

The existing `product` table definition and its test fixtures are replaced. No separate `product_resource` table is needed.

---

## State Transitions

The `conditions` array on `ProductStatus` reflects the product's reconciliation lifecycle:

```
Push accepted
    └─► AdmissionAccepted: True  (git service post-receive)
            └─► CategoryResolved: True | False
            └─► OptionsAccepted: True | False
            └─► VariantsResolved: True | False  (after GH#83)
                    └─► Ready: True  (all above True)
                            └─► Published: True  (release tag applied)
```

- `Ready: False` + `CategoryResolved: False` → category reference does not exist yet.
- `Ready: False` + `OptionsAccepted: False` → option definitions are structurally invalid.
- `Published: False` → product exists and is ready but no release tag has been applied.

---

## Validation Rules Summary

Two enforcement points: **schema validation** (blocking, `pre-receive`, configured via `GITSTORE_SCHEMA_VALIDATION__PHASE`) and **admission control** (fire-and-forget, `post-receive`, configured via `GITSTORE_ADMISSION_CONTROL__PHASE`). Both are implemented in `gitstore-api`; the git service delegates blindly.

| Rule                                                        | Phase                            | Error                                                                                                          |
|-------------------------------------------------------------|----------------------------------|----------------------------------------------------------------------------------------------------------------|
| `apiVersion` must be `catalog.gitstore.dev/v1beta1`         | Schema validation (pre-receive)  | "apiVersion must be catalog.gitstore.dev/v1beta1"                                                              |
| `kind` must be `Product`                                    | Schema validation (pre-receive)  | "kind must be Product, got: X"                                                                                 |
| `metadata.name` required and non-empty                      | Schema validation (pre-receive)  | "metadata.name is required"                                                                                    |
| `status` must not appear in author-pushed file              | Schema validation (pre-receive)  | "status is system-managed and must not be set by authors"                                                      |
| Read-only metadata fields must not appear                   | Schema validation (pre-receive)  | "metadata.uid/resourceVersion/generation/creationTimestamp/revision are read-only"                             |
| `spec.options[].name` required                              | Schema validation (pre-receive)  | "spec.options[N].name is required"                                                                             |
| `spec.options[].name` unique within list                    | Schema validation (pre-receive)  | "spec.options contains duplicate name 'X'"                                                                     |
| `metadata.labels` key/value length (Kubernetes conventions) | Schema validation (pre-receive)  | "label key 'X' exceeds maximum length"                                                                         |
| Legacy frontmatter format (no `apiVersion`)                 | Schema validation (pre-receive)  | "document does not use Kubernetes-style frontmatter (missing apiVersion); migration is not supported in alpha" |
| `metadata.name` unique within namespace                     | Admission control (post-receive) | logged as error; write skipped                                                                                 |
| `conditions[].status` must be True/False/Unknown            | Admission control (post-receive) | logged as error                                                                                                |
| `conditions[].type` must be one of 6 fixed types            | Admission control (post-receive) | logged as error                                                                                                |

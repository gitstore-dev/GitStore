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

CREATE TABLE IF NOT EXISTS categories (
    bucket             text,
    created_at         timestamp,
    id                 uuid,
    name               text,
    slug               text,
    parent_id          text,
    display_order      int,
    updated_at         timestamp,
    body               text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS collections (
    bucket             text,
    created_at         timestamp,
    id                 uuid,
    name               text,
    slug               text,
    display_order      int,
    product_ids        set<text>,
    updated_at         timestamp,
    body               text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS namespaces (
    bucket               text,
    created_at           timestamp,
    id                   uuid,
    identifier           text,
    display_name         text,
    tier                 text,
    parent_enterprise_id text,
    created_by           text,
    updated_at           timestamp,
    updated_by           text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS repositories (
    bucket         text,
    created_at     timestamp,
    id             uuid,
    namespace_id   uuid,
    name           text,
    default_branch text,
    storage_class  text,
    created_by     text,
    updated_at     timestamp,
    updated_by     text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

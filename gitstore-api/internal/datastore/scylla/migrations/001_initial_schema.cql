-- Migration 001: initial schema.
-- Assumes the keyspace already exists (provisioned by the operator / compose init).
-- Updated: 010-repo-storage-identity — added repositories and namespace_mappings tables.

CREATE TABLE IF NOT EXISTS products (
    id                 uuid       PRIMARY KEY,
    sku                text,
    title              text,
    price              decimal,
    currency           text,
    inventory_status   text,
    inventory_quantity int,
    category_id        text,
    collection_ids     set<text>,
    images             set<text>,
    metadata           map<text, text>,
    created_at         timestamp,
    updated_at         timestamp,
    body               text
);

CREATE TABLE IF NOT EXISTS categories (
    id                 uuid       PRIMARY KEY,
    name               text,
    slug               text,
    parent_id          text,
    display_order      int,
    created_at         timestamp,
    updated_at         timestamp,
    body               text
);

CREATE TABLE IF NOT EXISTS collections (
    id                 uuid       PRIMARY KEY,
    name               text,
    slug               text,
    display_order      int,
    product_ids        set<text>,
    created_at         timestamp,
    updated_at         timestamp,
    body               text
);

CREATE TABLE IF NOT EXISTS namespaces (
    id                   uuid       PRIMARY KEY,
    identifier           text,
    display_name         text,
    tier                 text,
    parent_enterprise_id text,
    created_at           timestamp,
    created_by           text,
    updated_at           timestamp,
    updated_by           text
);

CREATE TABLE IF NOT EXISTS repositories (
    id              uuid      PRIMARY KEY,
    namespace_id    uuid,
    name            text,
    default_branch  text,
    storage_class   text,
    created_at      timestamp,
    created_by      text,
    updated_at      timestamp,
    updated_by      text
);

-- CALL wait_schema;
CREATE INDEX IF NOT EXISTS repositories_by_namespace ON repositories (namespace_id);

-- namespace_mappings: composite primary key enables O(1) lookup by (namespace_id, name).
-- Storage path is derived from repo_id using the fanout formula and is NOT stored here.
CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

-- CALL wait_schema;
CREATE INDEX IF NOT EXISTS mappings_by_repo_id ON namespace_mappings (repo_id);

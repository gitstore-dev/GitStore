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

CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

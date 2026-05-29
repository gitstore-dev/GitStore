CREATE TABLE IF NOT EXISTS products (
    bucket             text,
    created_at         timestamp,
    id                 uuid,
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
    updated_at         timestamp,
    body               text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

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

CREATE TABLE IF NOT EXISTS products_by_repository (
    repository_id uuid,
    shard tinyint,
    uid uuid,
    namespace text,
    name text,
    creation_timestamp timestamp,
    PRIMARY KEY ((repository_id, shard), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variants_by_repository (
    repository_id uuid,
    shard tinyint,
    uid uuid,
    namespace text,
    name text,
    creation_timestamp timestamp,
    PRIMARY KEY ((repository_id, shard), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS category_taxonomies_by_repository (
    repository_id uuid,
    shard tinyint,
    uid uuid,
    namespace text,
    name text,
    creation_timestamp timestamp,
    PRIMARY KEY ((repository_id, shard), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS collections_by_repository (
    repository_id uuid,
    shard tinyint,
    uid uuid,
    namespace text,
    name text,
    creation_timestamp timestamp,
    PRIMARY KEY ((repository_id, shard), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS repository_catalog_projection_state (
    repository_id uuid PRIMARY KEY,
    ready boolean,
    deleted boolean
) WITH gc_grace_seconds = 864000;

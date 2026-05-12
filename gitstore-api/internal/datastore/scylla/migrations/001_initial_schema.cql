-- Migration 001: initial schema.
-- Assumes the keyspace already exists (provisioned by the operator / compose init).
-- UUID fields use text for Go string compatibility. Price uses decimal;

CREATE TABLE IF NOT EXISTS products (
    id                 text       PRIMARY KEY,
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
    id            text  PRIMARY KEY,
    name          text,
    slug          text,
    parent_id     text,
    display_order int,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

CREATE TABLE IF NOT EXISTS collections (
    id            text  PRIMARY KEY,
    name          text,
    slug          text,
    display_order int,
    product_ids   set<text>,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

-- CALL await_tables;

CREATE INDEX IF NOT EXISTS products_by_sku      ON products  (sku);
CREATE INDEX IF NOT EXISTS products_by_category ON products  (category_id);
CREATE INDEX IF NOT EXISTS categories_by_slug   ON categories(slug);
CREATE INDEX IF NOT EXISTS collections_by_slug  ON collections(slug);

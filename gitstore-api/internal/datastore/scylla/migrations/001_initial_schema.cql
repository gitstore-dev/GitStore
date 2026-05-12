-- Migration 001: initial schema
-- Creates keyspace, entity tables, and the migration lock table.
-- UUID fields use text for Go string compatibility; price uses double.

CREATE KEYSPACE IF NOT EXISTS gitstore
    WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'}
    AND durable_writes = true;

CREATE TABLE IF NOT EXISTS gitstore.products (
    id                 text       PRIMARY KEY,
    sku                text,
    title              text,
    price              double,
    currency           text,
    inventory_status   text,
    inventory_quantity int,
    category_id        text,
    collection_ids     list<text>,
    images             list<text>,
    metadata           map<text, text>,
    created_at         timestamp,
    updated_at         timestamp,
    body               text
);

CREATE TABLE IF NOT EXISTS gitstore.categories (
    id            text  PRIMARY KEY,
    name          text,
    slug          text,
    parent_id     text,
    display_order int,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

CREATE TABLE IF NOT EXISTS gitstore.collections (
    id            text  PRIMARY KEY,
    name          text,
    slug          text,
    display_order int,
    product_ids   list<text>,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

CREATE TABLE IF NOT EXISTS gitstore.schema_migrations_lock (
    lock_key    text PRIMARY KEY,
    holder      text,
    acquired_at timestamp
);

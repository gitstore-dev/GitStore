CREATE TABLE IF NOT EXISTS category_products_by_category (
    namespace text,
    category_uid text,
	shard tinyint,
    creation_timestamp timestamp,
    product_uid uuid,
	resource_version text,
    PRIMARY KEY ((namespace, category_uid, shard), creation_timestamp, product_uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, product_uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS category_products_by_product (
    namespace text,
    product_uid uuid,
    category_uid text,
    shard tinyint,
	resource_version text,
    PRIMARY KEY ((namespace, product_uid), category_uid)
) WITH CLUSTERING ORDER BY (category_uid ASC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS category_product_projection_state (
    projection text PRIMARY KEY,
    ready boolean
) WITH gc_grace_seconds = 864000;

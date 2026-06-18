ALTER TABLE products_by_namespace ADD IF NOT EXISTS repository_id text;
ALTER TABLE products_by_namespace ADD IF NOT EXISTS source_path text;

ALTER TABLE category_taxonomy ADD IF NOT EXISTS repository_id text;
ALTER TABLE category_taxonomy ADD IF NOT EXISTS source_path text;

ALTER TABLE collection ADD IF NOT EXISTS repository_id text;
ALTER TABLE collection ADD IF NOT EXISTS source_path text;

ALTER TABLE product_variant_by_namespace ADD IF NOT EXISTS repository_id text;
ALTER TABLE product_variant_by_namespace ADD IF NOT EXISTS source_path text;

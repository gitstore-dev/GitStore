ALTER TABLE products_by_namespace ADD repository_id text;
ALTER TABLE products_by_namespace ADD source_path text;

ALTER TABLE category_taxonomy ADD repository_id text;
ALTER TABLE category_taxonomy ADD source_path text;

ALTER TABLE collection ADD repository_id text;
ALTER TABLE collection ADD source_path text;

ALTER TABLE product_variant_by_namespace ADD repository_id text;
ALTER TABLE product_variant_by_namespace ADD source_path text;

-- CALL log_tables;

CREATE INDEX IF NOT EXISTS products_by_sku          ON products  (sku);
CREATE INDEX IF NOT EXISTS products_by_category     ON products  (category_id);
CREATE INDEX IF NOT EXISTS categories_by_slug       ON categories (slug);
CREATE INDEX IF NOT EXISTS collections_by_slug      ON collections (slug);
CREATE INDEX IF NOT EXISTS namespaces_by_identifier ON namespaces (identifier);

-- Migration 002: secondary indexes for lookup patterns
-- ScyllaDB requires each CREATE INDEX in a separate batch step (no IF NOT EXISTS on index names).

CREATE INDEX IF NOT EXISTS products_by_sku      ON gitstore.products  (sku);
CREATE INDEX IF NOT EXISTS products_by_category ON gitstore.products  (category_id);
CREATE INDEX IF NOT EXISTS categories_by_slug   ON gitstore.categories(slug);
CREATE INDEX IF NOT EXISTS collections_by_slug  ON gitstore.collections(slug);

CREATE INDEX IF NOT EXISTS products_by_repository_id          ON products_by_namespace       (repository_id);
CREATE INDEX IF NOT EXISTS category_taxonomy_by_repository_id ON category_taxonomy            (repository_id);
CREATE INDEX IF NOT EXISTS collection_by_repository_id        ON collection                   (repository_id);
CREATE INDEX IF NOT EXISTS product_variant_by_repository_id   ON product_variant_by_namespace (repository_id);

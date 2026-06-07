CREATE INDEX IF NOT EXISTS namespaces_by_id           ON namespaces         (id);
CREATE INDEX IF NOT EXISTS namespaces_by_identifier   ON namespaces         (identifier);

CREATE INDEX IF NOT EXISTS repositories_by_id         ON repositories       (id);
CREATE INDEX IF NOT EXISTS repositories_by_namespace  ON repositories       (namespace_id);

CREATE INDEX IF NOT EXISTS mappings_by_repo_id        ON namespace_mappings (repo_id);

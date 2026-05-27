-- Migration 004: repository storage identity indices (010-repo-storage-identity).

CREATE INDEX IF NOT EXISTS repositories_by_namespace ON repositories       (namespace_id);
CREATE INDEX IF NOT EXISTS mappings_by_repo_id       ON namespace_mappings (repo_id);

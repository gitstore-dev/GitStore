-- Migration 003: repository storage identity tables (010-repo-storage-identity).

CREATE TABLE IF NOT EXISTS repositories (
    id              uuid      PRIMARY KEY,
    namespace_id    uuid,
    name            text,
    default_branch  text,
    storage_class   text,
    created_at      timestamp,
    created_by      text,
    updated_at      timestamp,
    updated_by      text
);

-- namespace_mappings: composite primary key enables O(1) lookup by (namespace_id, name).
-- Storage path is derived from repo_id using the fanout formula and is NOT stored here.
CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

CREATE TABLE IF NOT EXISTS resources_by_repository (
    repository_id uuid,
    shard tinyint,
    kind text,
    uid uuid,
    namespace text,
    name text,
    creation_timestamp timestamp,
    PRIMARY KEY ((repository_id, shard), kind, uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS repository_catalog_projection_state (
    repository_id uuid PRIMARY KEY,
    membership_ready boolean,
    deleted boolean
) WITH gc_grace_seconds = 864000;

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

ALTER TABLE repository_catalog_projection_state ADD membership_ready boolean;

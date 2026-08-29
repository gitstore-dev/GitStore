CREATE TABLE IF NOT EXISTS owner_reference_dependents (
    namespace            text,
    repository_id        text,
    owner_uid            text,
    block_owner_deletion boolean,
    dependent_kind       text,
    dependent_uid        text,
    name                 text,
    resource_version     text,
    PRIMARY KEY ((namespace, repository_id, owner_uid, block_owner_deletion), dependent_kind, dependent_uid)
) WITH CLUSTERING ORDER BY (dependent_kind ASC, dependent_uid ASC)
  AND gc_grace_seconds = 864000;

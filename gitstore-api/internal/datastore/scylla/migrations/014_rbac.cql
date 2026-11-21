CREATE TABLE IF NOT EXISTS rbac_resources_by_scope (
    tier text,
    entry text,
    uid text,
    payload blob,
    creation_timestamp timestamp,
    creation_actor text,
    update_timestamp timestamp,
    update_actor text,
    revision bigint,
    role_count int,
    binding_count int,
    payload_bytes bigint,
    projection blob,
    repair blob,
    namespace_retired boolean,
    PRIMARY KEY ((tier), entry)
) WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}
    AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS rbac_resources_by_id (
    uid text PRIMARY KEY,
    tier text,
    entry text,
    deleted boolean
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS rbac_namespace_cleanups_by_bucket (
    bucket int,
    namespace_uid text,
    namespace_name text,
    PRIMARY KEY ((bucket), namespace_uid)
);

CREATE TABLE IF NOT EXISTS rbac_bindings_by_role (
    role_uid text,
    binding_uid text,
    tier text,
    entry text,
    deleted boolean,
    PRIMARY KEY ((role_uid), binding_uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS rbac_role_guards_by_id (
    role_uid text,
    entry text,
    version bigint,
    state text,
    claims bigint,
    payload blob,
    PRIMARY KEY ((role_uid), entry)
) WITH gc_grace_seconds = 864000;

-- ServiceAccount, listing projection and assertion replay protection;

CREATE TABLE IF NOT EXISTS service_accounts_by_cluster_name (
    creation_timestamp timestamp,
    uid                uuid,
    name               text,
    generation         bigint,
    resource_version   text,
    creation_actor     text,
    update_timestamp   timestamp,
    update_actor       text,
    deletion_timestamp timestamp,
    disabled           boolean,
    public_keys        text,
    enrollment_complete boolean,
    PRIMARY KEY ((name))
);

CREATE TABLE IF NOT EXISTS service_accounts_by_cluster_uid (
    uid uuid, name text, creation_timestamp timestamp,
    PRIMARY KEY ((uid))
);

CREATE TABLE IF NOT EXISTS service_accounts_by_cluster_bucket (
    bucket             text,
    creation_timestamp timestamp,
    uid                uuid,
    name               text,
    PRIMARY KEY ((bucket), creation_timestamp, uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS service_account_assertion_replays (
    jti_digest text PRIMARY KEY,
    consumed_at timestamp
);

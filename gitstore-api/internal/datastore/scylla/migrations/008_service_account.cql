CREATE TABLE IF NOT EXISTS service_accounts_by_namespace (
    namespace          text,
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
    PRIMARY KEY ((namespace), creation_timestamp, uid)
);

CREATE TABLE IF NOT EXISTS service_accounts_by_name (
    namespace text, name text, uid uuid, creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS service_accounts_by_uid (
    uid uuid, namespace text, creation_timestamp timestamp,
    PRIMARY KEY ((uid))
);

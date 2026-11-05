-- Product envelopes and name reservations have resource-sized partitions.
-- Namespace listings contain only keys, distributed across 64 stable shards;

CREATE TABLE IF NOT EXISTS products_by_key (
    namespace            text,
    creation_timestamp   timestamp,
    uid                  uuid,
    name                 text,
    api_version          text,
    kind                 text,
    generation           bigint,
    resource_version     text,
    revision             text,
    creation_actor       text,
    update_timestamp     timestamp,
    update_actor         text,
    labels               map<text, text>,
    annotations          map<text, text>,
    owner_references     text,
    finalizers           list<text>,
    deletion_timestamp   timestamp,
    repository_id        uuid,
    source_path          text,
    git_commit_sha       text,
    git_ref              text,
    spec                 text,
    body                 text,
    status               text,
    PRIMARY KEY ((namespace, creation_timestamp, uid))
) WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS products_by_identity (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace, name))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS products_by_namespace_shard (
    namespace          text,
    shard              tinyint,
    creation_timestamp timestamp,
    uid                uuid,
    PRIMARY KEY ((namespace, shard), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS products_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

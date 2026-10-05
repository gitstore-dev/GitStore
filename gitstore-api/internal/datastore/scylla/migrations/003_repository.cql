-- Repository. repositories_by_uid is authoritative and the CDC watch source.
-- repositories_by_namespace is a bucketed list projection that bounds
-- per-namespace partition size;

CREATE TABLE IF NOT EXISTS repositories_by_uid (
    uid                  uuid PRIMARY KEY,
    namespace            text,
    name                 text,
    api_version          text,
    kind                 text,
    generation           bigint,
    resource_version     text,
    revision             text,
    creation_timestamp   timestamp,
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
    default_branch       text,
    storage_class        text,
    max_pack_size_bytes  bigint,
    max_file_size_bytes  bigint
) WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS repositories_by_namespace (
    namespace          text,
    bucket             text,
    creation_timestamp timestamp,
    uid                uuid,
    PRIMARY KEY ((namespace, bucket), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS repositories_by_bucket (
    bucket             text,
    creation_timestamp timestamp,
    uid                uuid,
    PRIMARY KEY ((bucket), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

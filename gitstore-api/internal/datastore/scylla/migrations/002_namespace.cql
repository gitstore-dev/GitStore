-- Namespace. namespaces_by_uid is authoritative and the CDC watch source.
-- Namespace is cluster-scoped, so there is no namespace partition key;

CREATE TABLE IF NOT EXISTS namespaces_by_uid (
    uid                  uuid PRIMARY KEY,
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
    source_path          text,
    git_commit_sha       text,
    git_ref              text,
    spec                 text,
    body                 text,
    status               text,
    title                text,
    tier                 text,
    watch_committed      boolean,
    repository_creation_epoch    bigint,
    pending_repository_creations bigint
) WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS namespaces_by_name (
    name text PRIMARY KEY,
    uid  uuid
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS namespaces_by_bucket (
    bucket               text,
    creation_timestamp   timestamp,
    uid                  uuid,
    PRIMARY KEY ((bucket), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

-- Resolves a namespace/name path to its repository UID. Partitioned by
-- namespace so lookups by (namespace, name) are single-partition reads.
-- Kept in sync with namespace_mappings_by_repository below by the
-- repository mapping saga (see namespace_mapping.go). Drift between the
-- two is detected and fixed by repair.go;
CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace      text,
    name           text,
    repository_id  uuid,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

-- Reverse index of namespace_mappings: resolves a repository UID back to
-- its current namespace/name path. Partitioned by repository_id so this
-- direction is also a single-partition read instead of a scan over
-- namespace_mappings;
CREATE TABLE IF NOT EXISTS namespace_mappings_by_repository (
    repository_id uuid PRIMARY KEY,
    namespace     text,
    name          text
) WITH gc_grace_seconds = 864000;

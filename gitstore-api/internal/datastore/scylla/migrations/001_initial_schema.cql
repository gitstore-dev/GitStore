CREATE TABLE IF NOT EXISTS products_by_namespace (
    namespace          text,
    creation_timestamp timestamp,
    uid                uuid,
    name               text,
    api_version        text,
    kind               text,
    generation         bigint,
    resource_version   text,
    revision           text,
    labels             map<text, text>,
    annotations        map<text, text>,
    owner_refs         text,
    git_commit_sha     text,
    git_ref            text,
    spec               text,
    body               text,
    status             text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);

CREATE TABLE IF NOT EXISTS products_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS products_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);

CREATE TABLE IF NOT EXISTS categories (
    bucket             text,
    created_at         timestamp,
    id                 uuid,
    name               text,
    slug               text,
    parent_id          text,
    display_order      int,
    updated_at         timestamp,
    body               text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS collections (
    bucket             text,
    created_at         timestamp,
    id                 uuid,
    name               text,
    slug               text,
    display_order      int,
    product_ids        set<text>,
    updated_at         timestamp,
    body               text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS namespaces (
    bucket               text,
    created_at           timestamp,
    id                   uuid,
    identifier           text,
    display_name         text,
    tier                 text,
    parent_enterprise_id text,
    created_by           text,
    updated_at           timestamp,
    updated_by           text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS repositories (
    bucket         text,
    created_at     timestamp,
    id             uuid,
    namespace_id   uuid,
    name           text,
    default_branch text,
    storage_class  text,
    created_by     text,
    updated_at     timestamp,
    updated_by     text,
    PRIMARY KEY (bucket, created_at, id)
) WITH CLUSTERING ORDER BY (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

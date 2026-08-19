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
    repository_id      text,
    source_path        text,
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

CREATE TABLE IF NOT EXISTS collection (
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
    git_commit_sha     text,
    git_ref            text,
    spec               text,
    body               text,
    status             text,
    repository_id      text,
    source_path        text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);

CREATE TABLE IF NOT EXISTS collection_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS collection_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);

CREATE TABLE IF NOT EXISTS namespaces_by_id (
    id                   uuid PRIMARY KEY,
    name                 text,
    title                text,
    tier                 text,
    generation           bigint,
    resource_version     text,
    status               text,
    deletion_timestamp   timestamp,
    finalizers           list<text>,
    creation_timestamp   timestamp,
    creation_actor       text,
    update_timestamp     timestamp,
    update_actor         text
);

CREATE TABLE IF NOT EXISTS namespaces_by_name (
    name text PRIMARY KEY,
    id   uuid
);

CREATE TABLE IF NOT EXISTS namespaces_by_bucket (
    bucket               text,
    creation_timestamp   timestamp,
    id                   uuid,
    PRIMARY KEY ((bucket), creation_timestamp, id)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, id DESC);

CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace_id uuid,
    name         text,
    repo_id      uuid,
    PRIMARY KEY  (namespace_id, name)
);

CREATE TABLE IF NOT EXISTS repositories (
    bucket              text,
    creation_timestamp  timestamp,
    id                  uuid,
    namespace_id        uuid,
    name                text,
    default_branch      text,
    storage_class       text,
    generation          bigint,
    resource_version    text,
    status              text,
    creation_actor      text,
    max_pack_size_bytes bigint,
    max_file_size_bytes bigint,
    update_timestamp    timestamp,
    update_actor        text,
    PRIMARY KEY ((bucket), creation_timestamp, id)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, id DESC);

CREATE TABLE IF NOT EXISTS category_taxonomy (
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
    parent_name        text,
    ancestor_path      text,
    git_commit_sha     text,
    git_ref            text,
    spec               text,
    body               text,
    status             text,
    repository_id      text,
    source_path        text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);

CREATE TABLE IF NOT EXISTS category_taxonomy_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS category_taxonomy_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);

CREATE TABLE IF NOT EXISTS product_variant_by_namespace (
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
    sku                text,
    product_ref_name   text,
    git_commit_sha     text,
    git_ref            text,
    spec               text,
    body               text,
    status             text,
    repository_id      text,
    source_path        text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);

CREATE TABLE IF NOT EXISTS product_variant_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
);

CREATE TABLE IF NOT EXISTS product_variant_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
);

CREATE TABLE IF NOT EXISTS product_variant_by_sku (
    namespace          text,
    sku                text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), sku)
);

CREATE TABLE IF NOT EXISTS product_variant_by_product_ref (
    namespace          text,
    product_ref_name   text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace, product_ref_name), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC);

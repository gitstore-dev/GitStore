CREATE TABLE IF NOT EXISTS products_by_namespace (
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
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS products_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS products_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS collection (
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
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS collection_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS collection_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

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
    tier                 text
) WITH gc_grace_seconds = 864000;

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

CREATE TABLE IF NOT EXISTS namespace_mappings (
    namespace      text,
    name           text,
    repository_id  uuid,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS namespace_mappings_by_repository (
    repository_id uuid PRIMARY KEY,
    namespace     text,
    name          text
) WITH gc_grace_seconds = 864000;

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
) WITH gc_grace_seconds = 864000;

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

CREATE TABLE IF NOT EXISTS category_taxonomy (
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
    parent_name          text,
    ancestor_path        text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS category_taxonomy_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS category_taxonomy_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variant_by_namespace (
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
    sku                  text,
    product_ref_name     text,
    PRIMARY KEY ((namespace), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variant_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variant_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variant_by_sku (
    namespace          text,
    sku                text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), sku)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variant_by_product_ref (
    namespace          text,
    product_ref_name   text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace, product_ref_name), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

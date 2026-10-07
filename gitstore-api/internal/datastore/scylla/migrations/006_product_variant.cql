-- ProductVariant. product_variants_by_namespace is authoritative;

CREATE TABLE IF NOT EXISTS product_variants_by_namespace (
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

CREATE TABLE IF NOT EXISTS product_variants_by_name (
    namespace          text,
    name               text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), name)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variants_by_uid (
    uid                uuid,
    namespace          text,
    creation_timestamp timestamp,
    PRIMARY KEY (uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variants_by_sku (
    namespace          text,
    sku                text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace), sku)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS product_variants_by_product_ref (
    namespace          text,
    product_ref_name   text,
    uid                uuid,
    creation_timestamp timestamp,
    PRIMARY KEY ((namespace, product_ref_name), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

-- SPDX-License-Identifier: AGPL-3.0-or-later
-- Copyright (c) 2026 GitStore contributors
-- Resource-sized authority and versioned, segmented thin projections;

CREATE TABLE IF NOT EXISTS translation_target_mutations_by_namespace (
    namespace         text PRIMARY KEY,
    translation_epoch text,
    translation_coverage_epoch text,
    translation_owner text
);

ALTER TABLE product_variants_by_namespace WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'};
ALTER TABLE collections_by_namespace WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'};

ALTER TABLE products_by_key ADD translation_retired boolean;
ALTER TABLE category_taxonomies_by_namespace ADD translation_retired boolean;
ALTER TABLE collections_by_namespace ADD translation_retired boolean;
ALTER TABLE product_variants_by_namespace ADD translation_retired boolean;
ALTER TABLE files_by_namespace ADD translation_retired boolean;

ALTER TABLE products_by_key ADD translation_source_identity text;
ALTER TABLE category_taxonomies_by_namespace ADD translation_source_identity text;
ALTER TABLE collections_by_namespace ADD translation_source_identity text;
ALTER TABLE product_variants_by_namespace ADD translation_source_identity text;
ALTER TABLE files_by_namespace ADD translation_source_identity text;

CREATE TABLE IF NOT EXISTS translations_by_key (
    namespace_uid        uuid,
    creation_timestamp   timestamp,
    uid                  uuid,
    namespace            text,
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
    source_blob_oid      text,
    source_digest        text,
    raw_source           blob,
    spec                 text,
    body                 text,
    status               text,
    reference_epoch      text,
    coordination_version text,
    phase                text,
    binding_snapshot     text,
    maintenance_intent   text,
    repair_watermark     text,
    revalidation_version text,
    PRIMARY KEY ((namespace_uid, creation_timestamp, uid))
) WITH cdc = {'enabled': 'true', 'preimage': 'full', 'postimage': 'true', 'ttl': '1209600'}
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translations_by_name (
    namespace_uid        uuid,
    name                 text,
    uid                  uuid,
    creation_timestamp   timestamp,
    resource_version     text,
    reservation_token    uuid,
    phase                text,
    PRIMARY KEY ((namespace_uid, name))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translations_by_uid (
    uid                  uuid,
    namespace_uid        uuid,
    namespace            text,
    name                 text,
    creation_timestamp   timestamp,
    reservation_token    uuid,
    expected_name_token  uuid,
    retired              boolean,
    PRIMARY KEY ((uid))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translations_by_namespace (
    namespace_uid        uuid,
    shard                tinyint,
    segment              bigint,
    creation_timestamp   timestamp,
    uid                  uuid,
    source_version       text,
    reference_epoch      text,
    slot                 int,
    reservation_token    uuid,
    retired              boolean,
    PRIMARY KEY ((namespace_uid, shard, segment), creation_timestamp, uid)
) WITH CLUSTERING ORDER BY (creation_timestamp DESC, uid DESC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_bindings_by_uid (
    uid                  uuid,
    resource_version     text,
    source_version       text,
    current_epoch        text,
    epochs               text,
    PRIMARY KEY ((uid))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_dependents_by_target_name (
    namespace_uid        uuid,
    api_version          text,
    kind                 text,
    name                 text,
    shard                tinyint,
    segment              bigint,
    uid                  uuid,
    source_version       text,
    reference_epoch      text,
    slot                 int,
    reservation_token    uuid,
    retired              boolean,
    creation_timestamp   timestamp,
    PRIMARY KEY ((namespace_uid, api_version, kind, name, shard, segment), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_dependents_by_target_uid (
    namespace_uid        uuid,
    target_uid           uuid,
    shard                tinyint,
    segment              bigint,
    uid                  uuid,
    source_version       text,
    reference_epoch      text,
    slot                 int,
    reservation_token    uuid,
    retired              boolean,
    creation_timestamp   timestamp,
    PRIMARY KEY ((namespace_uid, target_uid, shard, segment), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_index_heads_by_shard (
    index_kind           text,
    namespace_uid        uuid,
    target_key           text,
    shard                tinyint,
    resource_version     text,
    segment              bigint,
    next_slot            int,
    reserved_bytes       bigint,
    pending_token        uuid,
    PRIMARY KEY ((index_kind, namespace_uid, target_key, shard))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_index_reservations_by_token (
    reservation_token    uuid,
    resource_version     text,
    index_kind           text,
    namespace_uid        uuid,
    target_key           text,
    shard                tinyint,
    segment              bigint,
    slot                 int,
    uid                  uuid,
    source_version       text,
    reference_epoch      text,
    payload_bytes        bigint,
    phase                text,
    creation_timestamp   timestamp,
    expected_head_version text,
    allocation_version   text,
    reserved_bytes       bigint,
    PRIMARY KEY ((reservation_token))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_maintenance_by_bucket (
    bucket               tinyint,
    id                   text,
    uid                  uuid,
    namespace_uid        uuid,
    creation_timestamp   timestamp,
    source_version       text,
    resource_version     text,
    PRIMARY KEY ((bucket), id)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_target_fences_by_name (
    namespace_uid             uuid,
    api_version               text,
    kind                      text,
    name                      text,
    target_name               text,
    fence_token               text,
    phase                     text,
    target_creation_timestamp timestamp,
    evidence                  text,
    write_intent              text,
    PRIMARY KEY ((namespace_uid, api_version, kind, name))
) WITH gc_grace_seconds = 864000;

-- Co-partitioned retained Translation ownership retry fences;
ALTER TABLE owner_reference_dependents ADD (
    translation_coordination_version text,
    translation_projection_retired boolean
);

ALTER TABLE resources_by_repository ADD (
    translation_coordination_version text,
    translation_projection_retired boolean
);

CREATE TABLE IF NOT EXISTS translation_target_tombstones_by_uid (
    kind text,
    uid uuid,
    retired boolean,
    namespace text,
    creation_timestamp timestamp,
    PRIMARY KEY ((kind, uid))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_target_write_intents_by_id (
    id uuid,
    record text,
    closed boolean,
    PRIMARY KEY ((id))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_target_maintenance_by_segment (
    index_kind text,
    namespace_uid uuid,
    shard tinyint,
    segment bigint,
    uid uuid,
    source_version text,
    reference_epoch text,
    slot int,
    reservation_token uuid,
    retired boolean,
    creation_timestamp timestamp,
    PRIMARY KEY ((index_kind, namespace_uid, shard, segment), uid)
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_target_maintenance_cursors_by_namespace (
    scope text,
    namespace_uid uuid,
    cursor text,
    PRIMARY KEY ((scope, namespace_uid))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_audit_progress_by_namespace (
    namespace_uid uuid,
    resource_version text,
    run_id uuid,
    phase text,
    cursor text,
    scanned bigint,
    PRIMARY KEY ((namespace_uid))
) WITH gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS translation_target_audits_by_namespace_uid (
    namespace_uid uuid PRIMARY KEY,
    progress text
) WITH gc_grace_seconds = 864000;

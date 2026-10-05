-- Shared infrastructure: durable resource watch journal, owner-reference
-- reverse projection and auth session revocations.

-- Fenced journal clock, per-source CDC progress and materializer lease shared
-- by every watched kind. Controller leases live in their own table (ADR 0018);
CREATE TABLE IF NOT EXISTS resource_watch_clock (
  journal text,
  stream_id text,
  epoch uuid static,
  high_water bigint static,
  oldest bigint static,
  bucket_size bigint static,
  update_timestamp timestamp static,
  bookmark_timestamp timestamp static,
  cdc_progress_timestamp timestamp static,
  lease_holder text static,
  fencing_token bigint static,
  lease_expiration_timestamp timestamp static,
  position blob,
  progress_update_timestamp timestamp,
  PRIMARY KEY ((journal), stream_id)
);

CREATE TABLE IF NOT EXISTS resource_watch_events (
  epoch uuid,
  bucket bigint,
  sequence bigint,
  event_type text,
  kind text,
  namespace text,
  name text,
  payload text,
  labels map<text, text>,
  previous_labels map<text, text>,
  deduplication_key text,
  fencing_token bigint,
  event_timestamp timestamp,
  PRIMARY KEY ((epoch, bucket), sequence)
) WITH CLUSTERING ORDER BY (sequence ASC)
  AND default_time_to_live = 604800;

CREATE TABLE IF NOT EXISTS owner_reference_dependents (
    namespace            text,
    repository_id        text,
    owner_uid            text,
    block_owner_deletion boolean,
    dependent_kind       text,
    dependent_uid        text,
    name                 text,
    resource_version     text,
    PRIMARY KEY ((namespace, repository_id, owner_uid, block_owner_deletion), dependent_kind, dependent_uid)
) WITH CLUSTERING ORDER BY (dependent_kind ASC, dependent_uid ASC)
  AND gc_grace_seconds = 864000;

CREATE TABLE IF NOT EXISTS auth_session_revocations (
  jti text PRIMARY KEY,
  expires_at timestamp
) WITH gc_grace_seconds = 864000;

ALTER TABLE namespaces_by_uid WITH cdc = {
  'enabled': 'true',
  'preimage': 'full',
  'postimage': 'true',
  'ttl': '1209600'
};

CREATE TABLE IF NOT EXISTS namespace_watch_clock (
  journal text PRIMARY KEY,
  epoch uuid,
  high_water bigint,
  oldest bigint,
  updated_at timestamp
);

CREATE TABLE IF NOT EXISTS namespace_watch_events (
  epoch uuid,
  bucket bigint,
  sequence bigint,
  event_type text,
  name text,
  payload text,
  deduplication_key text,
  event_at timestamp,
  PRIMARY KEY ((epoch, bucket), sequence)
) WITH CLUSTERING ORDER BY (sequence ASC)
  AND default_time_to_live = 604800;

CREATE TABLE IF NOT EXISTS namespace_cdc_progress (
  stream_id text PRIMARY KEY,
  position blob,
  fencing_token bigint,
  updated_at timestamp
);

CREATE TABLE IF NOT EXISTS namespace_watch_materializer_lease (
  lease_name text PRIMARY KEY,
  holder text,
  fencing_token bigint,
  expires_at timestamp
);

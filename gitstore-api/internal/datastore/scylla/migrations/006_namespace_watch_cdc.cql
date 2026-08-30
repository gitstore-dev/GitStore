ALTER TABLE namespaces_by_uid WITH cdc = {
  'enabled': 'true',
  'preimage': 'full',
  'postimage': 'true',
  'ttl': '1209600'
};

CREATE TABLE IF NOT EXISTS namespace_watch_clock (
  journal text,
  stream_id text,
  epoch uuid static,
  high_water bigint static,
  oldest bigint static,
  updated_at timestamp static,
  cdc_progress_at timestamp static,
  holder text static,
  fencing_token bigint static,
  expires_at timestamp static,
  position blob,
  progress_updated_at timestamp,
  PRIMARY KEY ((journal), stream_id)
);

CREATE TABLE IF NOT EXISTS namespace_watch_events (
  epoch uuid,
  bucket bigint,
  sequence bigint,
  event_type text,
  name text,
  payload text,
  selector_labels text,
  deduplication_key text,
  fencing_token bigint,
  event_at timestamp,
  PRIMARY KEY ((epoch, bucket), sequence)
) WITH CLUSTERING ORDER BY (sequence ASC)
  AND default_time_to_live = 604800;

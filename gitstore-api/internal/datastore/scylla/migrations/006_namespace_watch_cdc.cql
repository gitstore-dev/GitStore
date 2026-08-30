ALTER TABLE namespaces_by_uid ADD watch_committed boolean;

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
  bucket_size bigint static,
  update_timestamp timestamp static,
  cdc_progress_timestamp timestamp static,
  lease_holder text static,
  fencing_token bigint static,
  lease_expiration_timestamp timestamp static,
  position blob,
  progress_update_timestamp timestamp,
  PRIMARY KEY ((journal), stream_id)
);

CREATE TABLE IF NOT EXISTS namespace_watch_events (
  epoch uuid,
  bucket bigint,
  sequence bigint,
  event_type text,
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

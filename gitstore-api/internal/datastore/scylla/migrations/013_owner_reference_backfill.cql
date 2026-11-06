CREATE TABLE IF NOT EXISTS owner_reference_backfill_by_job (
    job_id text PRIMARY KEY,
    lease_holder text,
    fencing_token bigint,
    lease_expiration_timestamp timestamp,
    progress_json text
) WITH gc_grace_seconds = 864000;

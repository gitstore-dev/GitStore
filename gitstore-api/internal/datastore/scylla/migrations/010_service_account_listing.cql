CREATE TABLE IF NOT EXISTS service_accounts_by_bucket (
    bucket             text,
    creation_timestamp timestamp,
    uid                uuid,
    namespace          text,
    PRIMARY KEY ((bucket), creation_timestamp, uid)
) WITH gc_grace_seconds = 864000;

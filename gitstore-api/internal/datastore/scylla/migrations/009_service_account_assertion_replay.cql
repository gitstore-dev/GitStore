CREATE TABLE IF NOT EXISTS service_account_assertion_replays (
    jti_digest text PRIMARY KEY,
    consumed_at timestamp
);

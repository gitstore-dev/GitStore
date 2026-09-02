CREATE TABLE IF NOT EXISTS auth_session_revocations (
  jti text PRIMARY KEY,
  expires_at timestamp
) WITH gc_grace_seconds = 864000;

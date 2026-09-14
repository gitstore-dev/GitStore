-- The durable journal is shared by Namespace, Repository, and future CRDs.
-- Keep the existing alpha table names: journal="resource" identifies the
-- shared stream, while each event carries its canonical resource identity.
ALTER TABLE namespace_watch_events ADD kind text;
ALTER TABLE namespace_watch_events ADD namespace text;

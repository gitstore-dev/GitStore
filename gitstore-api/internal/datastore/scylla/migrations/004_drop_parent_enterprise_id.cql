DROP INDEX IF EXISTS namespaces_by_id;
DROP INDEX IF EXISTS namespaces_by_identifier;
ALTER TABLE namespaces DROP parent_enterprise_id;
CREATE INDEX IF NOT EXISTS namespaces_by_id         ON namespaces (id);
CREATE INDEX IF NOT EXISTS namespaces_by_identifier ON namespaces (identifier);

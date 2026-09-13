ALTER TABLE audit_events ALTER COLUMN audit_id TYPE text USING audit_id::text;

ALTER TABLE audit_events ALTER COLUMN object_uid TYPE text USING object_uid::text;

ALTER TABLE audit_events ADD COLUMN canonical bytea NOT NULL DEFAULT ''::bytea;

ALTER TABLE audit_events ALTER COLUMN canonical DROP DEFAULT;

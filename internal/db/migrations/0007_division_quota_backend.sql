ALTER TABLE divisions ALTER COLUMN tenant_name DROP NOT NULL;

ALTER TABLE divisions ADD COLUMN quota_backend text;

ALTER TABLE namespaces ADD COLUMN role_bindings integer NOT NULL DEFAULT 0;

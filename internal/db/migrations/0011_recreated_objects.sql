ALTER TABLE cost_samples DROP CONSTRAINT cost_samples_division_uid_fkey;
ALTER TABLE cost_samples ADD CONSTRAINT cost_samples_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE division_grants DROP CONSTRAINT division_grants_division_uid_fkey;
ALTER TABLE division_grants ADD CONSTRAINT division_grants_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE division_members DROP CONSTRAINT division_members_division_uid_fkey;
ALTER TABLE division_members ADD CONSTRAINT division_members_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE invoices DROP CONSTRAINT invoices_division_uid_fkey;
ALTER TABLE invoices ADD CONSTRAINT invoices_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE namespaces DROP CONSTRAINT namespaces_division_uid_fkey;
ALTER TABLE namespaces ADD CONSTRAINT namespaces_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE policy_violations DROP CONSTRAINT policy_violations_division_uid_fkey;
ALTER TABLE policy_violations ADD CONSTRAINT policy_violations_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE requests DROP CONSTRAINT requests_division_uid_fkey;
ALTER TABLE requests ADD CONSTRAINT requests_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE timeline_events DROP CONSTRAINT timeline_events_division_uid_fkey;
ALTER TABLE timeline_events ADD CONSTRAINT timeline_events_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE CASCADE ON UPDATE CASCADE;

ALTER TABLE workloads DROP CONSTRAINT workloads_division_uid_fkey;
ALTER TABLE workloads ADD CONSTRAINT workloads_division_uid_fkey
    FOREIGN KEY (division_uid) REFERENCES divisions (uid) ON DELETE SET NULL ON UPDATE CASCADE;

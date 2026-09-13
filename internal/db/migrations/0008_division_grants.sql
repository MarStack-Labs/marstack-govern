CREATE TABLE division_grants (
    division_uid        uuid NOT NULL REFERENCES divisions (uid) ON DELETE CASCADE,
    role                text NOT NULL,
    group_claim         text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (division_uid, role, group_claim),
    CONSTRAINT division_grants_role_known CHECK (role IN ('admin', 'operator', 'viewer', 'approver'))
);

CREATE INDEX division_grants_group_claim_idx ON division_grants (group_claim);

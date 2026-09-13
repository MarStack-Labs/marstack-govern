CREATE TABLE projection_sources (
    source              text PRIMARY KEY,
    cursor              text,
    last_event_at       timestamptz,
    last_synced_at      timestamptz NOT NULL DEFAULT now(),
    healthy             boolean NOT NULL DEFAULT true,
    last_error          text,
    CONSTRAINT projection_sources_source_known CHECK (source IN (
        'kubernetes',
        'kubernetes-audit',
        'oci-registry',
        'mimir',
        'loki',
        'tempo',
        'kyverno',
        'trivy',
        'opencost',
        'argocd',
        'hubble',
        'idp'
    ))
);

CREATE TABLE divisions (
    uid                 uuid PRIMARY KEY,
    name                text NOT NULL UNIQUE,
    display_name        text NOT NULL,
    tenant_name         text NOT NULL,
    phase               text NOT NULL,
    quota_cpu_millicores    bigint,
    quota_memory_bytes      bigint,
    quota_storage_bytes     bigint,
    quota_pods              integer,
    used_cpu_millicores     bigint,
    used_memory_bytes       bigint,
    used_storage_bytes      bigint,
    used_pods               integer,
    created_at          timestamptz NOT NULL,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT divisions_phase_known CHECK (phase IN ('pending', 'active', 'suspended', 'terminating'))
);

CREATE INDEX divisions_observed_at_idx ON divisions (observed_at DESC);

CREATE TABLE namespaces (
    name                text PRIMARY KEY,
    division_uid        uuid REFERENCES divisions (uid) ON DELETE CASCADE,
    environment         text,
    phase               text NOT NULL,
    default_deny_present boolean NOT NULL DEFAULT false,
    limit_range_present boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX namespaces_division_uid_idx ON namespaces (division_uid);

CREATE TABLE division_members (
    division_uid        uuid NOT NULL REFERENCES divisions (uid) ON DELETE CASCADE,
    subject             text NOT NULL,
    role                text NOT NULL,
    group_claim         text NOT NULL,
    synced_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (division_uid, subject, role),
    CONSTRAINT division_members_role_known CHECK (role IN ('admin', 'operator', 'viewer', 'approver'))
);

CREATE INDEX division_members_subject_idx ON division_members (subject);

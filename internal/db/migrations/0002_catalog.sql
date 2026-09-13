CREATE TABLE images (
    digest              text PRIMARY KEY,
    registry            text NOT NULL,
    repository          text NOT NULL,
    tag                 text,
    tag_mutable         boolean,
    source_repo         text,
    source_revision     text,
    signed              boolean,
    signature_issuer    text,
    slsa_attested       boolean,
    sbom_present        boolean,
    base_image_age_days integer,
    cve_critical        integer,
    cve_high            integer,
    cve_medium          integer,
    cve_low             integer,
    scanned_at          timestamptz,
    observed_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workloads (
    uid                 uuid PRIMARY KEY,
    division_uid        uuid REFERENCES divisions (uid) ON DELETE SET NULL,
    namespace           text NOT NULL,
    name                text NOT NULL,
    kind                text NOT NULL,
    api_version         text NOT NULL,
    image_digest        text REFERENCES images (digest) ON DELETE SET NULL,
    image_ref           text,
    replicas_desired    integer,
    replicas_ready      integer,
    health              text NOT NULL,
    tier                text,
    tier_source         text,
    tier_override_until timestamptz,
    owner_subject       text,
    ingress_hosts       text[],
    has_pvc             boolean NOT NULL DEFAULT false,
    has_pdb             boolean NOT NULL DEFAULT false,
    has_hpa             boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workloads_health_known CHECK (health IN ('healthy', 'progressing', 'degraded', 'unknown')),
    CONSTRAINT workloads_tier_source_known CHECK (tier_source IS NULL OR tier_source IN ('classifier', 'override')),
    CONSTRAINT workloads_override_has_expiry CHECK (tier_source <> 'override' OR tier_override_until IS NOT NULL),
    UNIQUE (namespace, kind, name)
);

CREATE INDEX workloads_division_uid_idx ON workloads (division_uid);
CREATE INDEX workloads_namespace_idx ON workloads (namespace);
CREATE INDEX workloads_health_idx ON workloads (health) WHERE health <> 'healthy';

CREATE TABLE workload_containers (
    workload_uid            uuid NOT NULL REFERENCES workloads (uid) ON DELETE CASCADE,
    container               text NOT NULL,
    image_digest            text REFERENCES images (digest) ON DELETE SET NULL,
    cpu_request_millicores  bigint,
    cpu_limit_millicores    bigint,
    memory_request_bytes    bigint,
    memory_limit_bytes      bigint,
    observed_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workload_uid, container)
);

CREATE TABLE gitops_applications (
    name                text NOT NULL,
    namespace           text NOT NULL,
    workload_uid        uuid REFERENCES workloads (uid) ON DELETE CASCADE,
    repo_url            text NOT NULL,
    target_revision     text,
    synced_revision     text,
    sync_status         text NOT NULL,
    health_status       text NOT NULL,
    drifted_fields      jsonb NOT NULL DEFAULT '[]'::jsonb,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, name)
);

CREATE INDEX gitops_applications_workload_uid_idx ON gitops_applications (workload_uid);

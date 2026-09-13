CREATE TABLE audit_events (
    seq                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    audit_id            uuid NOT NULL UNIQUE,
    event_at            timestamptz NOT NULL,
    stage               text NOT NULL,
    actor               text NOT NULL,
    actor_groups        text[] NOT NULL DEFAULT '{}',
    impersonated_by     text,
    acting_division     text,
    verb                text NOT NULL,
    resource            text NOT NULL,
    subresource         text,
    namespace           text,
    object_name         text,
    object_uid          uuid,
    response_code       integer,
    source_ips          text[] NOT NULL DEFAULT '{}',
    user_agent          text,
    payload             jsonb NOT NULL,
    prev_hash           bytea NOT NULL,
    hash                bytea NOT NULL UNIQUE,
    ingested_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_hash_len CHECK (length(hash) = 32),
    CONSTRAINT audit_events_prev_hash_len CHECK (length(prev_hash) IN (0, 32))
);

CREATE INDEX audit_events_event_at_idx ON audit_events (event_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor, event_at DESC);
CREATE INDEX audit_events_object_uid_idx ON audit_events (object_uid) WHERE object_uid IS NOT NULL;
CREATE INDEX audit_events_division_idx ON audit_events (acting_division, event_at DESC);

CREATE FUNCTION audit_events_reject_mutation() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;

CREATE TRIGGER audit_events_no_update
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_reject_mutation();

CREATE TRIGGER audit_events_no_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_reject_mutation();

CREATE FUNCTION audit_events_link() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    tip bytea;
BEGIN
    SELECT hash INTO tip FROM audit_events ORDER BY seq DESC LIMIT 1;

    IF tip IS NULL THEN
        IF length(NEW.prev_hash) <> 0 THEN
            RAISE EXCEPTION 'the first audit event must carry an empty prev_hash';
        END IF;
    ELSIF NEW.prev_hash IS DISTINCT FROM tip THEN
        RAISE EXCEPTION 'audit chain break: prev_hash does not match the current tip';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_events_link_chain
    BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_link();

CREATE TABLE timeline_events (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dedupe_key          text NOT NULL UNIQUE,
    workload_uid        uuid REFERENCES workloads (uid) ON DELETE CASCADE,
    division_uid        uuid REFERENCES divisions (uid) ON DELETE CASCADE,
    occurred_at         timestamptz NOT NULL,
    kind                text NOT NULL,
    source              text NOT NULL,
    summary             text NOT NULL,
    detail              jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT timeline_events_kind_known CHECK (kind IN (
        'rollout',
        'kubernetes_event',
        'alert',
        'audit',
        'decision',
        'policy_violation',
        'quota_change'
    )),
    CONSTRAINT timeline_events_has_subject CHECK (workload_uid IS NOT NULL OR division_uid IS NOT NULL)
);

CREATE INDEX timeline_events_workload_idx ON timeline_events (workload_uid, occurred_at DESC);
CREATE INDEX timeline_events_division_idx ON timeline_events (division_uid, occurred_at DESC);

CREATE TABLE policy_violations (
    policy              text NOT NULL,
    rule                text NOT NULL,
    namespace           text NOT NULL,
    division_uid        uuid REFERENCES divisions (uid) ON DELETE CASCADE,
    resource_kind       text NOT NULL,
    resource_name       text NOT NULL,
    workload_uid        uuid REFERENCES workloads (uid) ON DELETE CASCADE,
    severity            text NOT NULL,
    result              text NOT NULL,
    message             text,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (policy, rule, namespace, resource_kind, resource_name),
    CONSTRAINT policy_violations_severity_known CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT policy_violations_result_known CHECK (result IN ('fail', 'warn', 'error'))
);

CREATE INDEX policy_violations_division_idx ON policy_violations (division_uid);
CREATE INDEX policy_violations_workload_idx ON policy_violations (workload_uid);

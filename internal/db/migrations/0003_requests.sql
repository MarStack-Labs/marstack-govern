CREATE TABLE requests (
    uid                 uuid PRIMARY KEY,
    kind                text NOT NULL,
    name                text NOT NULL,
    namespace           text NOT NULL,
    division_uid        uuid REFERENCES divisions (uid) ON DELETE CASCADE,
    requester           text NOT NULL,
    requester_groups    text[] NOT NULL DEFAULT '{}',
    acting_division     text NOT NULL,
    reason              text NOT NULL,
    spec                jsonb NOT NULL,
    recommendation      jsonb,
    preflight           jsonb,
    phase               text NOT NULL,
    created_at          timestamptz NOT NULL,
    expires_at          timestamptz,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT requests_kind_known CHECK (kind IN (
        'QuotaRequest',
        'AccessRequest',
        'PeeringRequest',
        'MembershipRequest',
        'NamespaceRequest',
        'EphemeralEnvironment'
    )),
    CONSTRAINT requests_phase_known CHECK (phase IN (
        'pending',
        'preflight_failed',
        'awaiting_decision',
        'approved',
        'rejected',
        'changes_requested',
        'applied',
        'expired',
        'withdrawn'
    )),
    UNIQUE (namespace, kind, name)
);

CREATE INDEX requests_division_uid_idx ON requests (division_uid);
CREATE INDEX requests_requester_idx ON requests (requester);
CREATE INDEX requests_open_idx ON requests (created_at DESC)
    WHERE phase IN ('pending', 'awaiting_decision', 'changes_requested');

CREATE TABLE decisions (
    uid                 uuid PRIMARY KEY,
    request_uid         uuid NOT NULL REFERENCES requests (uid) ON DELETE CASCADE,
    decider             text NOT NULL,
    decider_groups      text[] NOT NULL DEFAULT '{}',
    outcome             text NOT NULL,
    reason              text NOT NULL,
    evidence            jsonb NOT NULL,
    granted_until       timestamptz,
    decided_at          timestamptz NOT NULL,
    revoked_at          timestamptz,
    revoked_reason      text,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT decisions_outcome_known CHECK (outcome IN ('approved', 'rejected', 'changes_requested')),
    CONSTRAINT decisions_approval_is_bounded CHECK (outcome <> 'approved' OR granted_until IS NOT NULL),
    CONSTRAINT decisions_evidence_not_empty CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb)
);

CREATE INDEX decisions_request_uid_idx ON decisions (request_uid);
CREATE INDEX decisions_decided_at_idx ON decisions (decided_at DESC);
CREATE INDEX decisions_expiring_idx ON decisions (granted_until)
    WHERE outcome = 'approved' AND revoked_at IS NULL;

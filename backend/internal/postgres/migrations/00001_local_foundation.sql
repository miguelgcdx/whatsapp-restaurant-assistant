-- +goose Up
CREATE SCHEMA IF NOT EXISTS app;
CREATE TABLE app.conversation_sequences (
    sender text PRIMARY KEY,
    next_sequence bigint NOT NULL CHECK (next_sequence > 0)
);
CREATE TABLE app.inbound_events (
    id uuid PRIMARY KEY,
    provider_event_id text NOT NULL UNIQUE,
    sender text NOT NULL REFERENCES app.conversation_sequences(sender),
    sequence bigint NOT NULL CHECK (sequence > 0),
    content text NOT NULL,
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    created_at timestamptz NOT NULL,
    UNIQUE (sender, sequence)
);
CREATE TABLE app.jobs (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE REFERENCES app.inbound_events(id),
    state text NOT NULL CHECK (state IN ('queued', 'leased', 'completed', 'failed')),
    due_at timestamptz NOT NULL,
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    lease_until timestamptz,
    CHECK ((state = 'leased') = (lease_until IS NOT NULL))
);
CREATE TABLE app.outbox (
    id uuid PRIMARY KEY,
    event_id uuid NOT NULL UNIQUE REFERENCES app.inbound_events(id),
    content text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'sending', 'accepted', 'unknown', 'failed')),
    due_at timestamptz NOT NULL,
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    lease_until timestamptz,
    provider_id text NOT NULL DEFAULT '',
    CHECK ((state = 'sending') = (lease_until IS NOT NULL))
);
CREATE TABLE app.send_attempts (
    outbox_id uuid NOT NULL REFERENCES app.outbox(id),
    attempt integer NOT NULL CHECK (attempt > 0),
    generation bigint NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    outcome text NOT NULL CHECK (outcome IN ('sending', 'accepted', 'unknown', 'failed')),
    provider_id text NOT NULL DEFAULT '',
    PRIMARY KEY (outbox_id, attempt)
);
CREATE INDEX jobs_due ON app.jobs (due_at, id) WHERE state = 'queued';
CREATE INDEX jobs_expired ON app.jobs (lease_until) WHERE state = 'leased';
CREATE INDEX outbox_due ON app.outbox (due_at, id) WHERE state = 'pending';
CREATE INDEX outbox_expired ON app.outbox (lease_until) WHERE state = 'sending';

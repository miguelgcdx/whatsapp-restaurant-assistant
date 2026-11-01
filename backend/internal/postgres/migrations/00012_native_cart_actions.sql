-- +goose Up
-- Deliberate native admission only: no legacy backfill or reply-job resurrection.
CREATE TABLE app.native_cart_actions (
    event_id uuid NOT NULL,
    runtime_slot bigint NOT NULL CHECK (runtime_slot >= 0),
    operation_id uuid NOT NULL UNIQUE REFERENCES app.cart_execution_calls(operation_id),
    restaurant_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    admission_job_id uuid NOT NULL REFERENCES app.jobs(id),
    admission_version bigint NOT NULL CHECK (admission_version >= 0),
    admission_generation bigint NOT NULL CHECK (admission_generation > 0),
    admission_attempt bigint NOT NULL CHECK (admission_attempt > 0),
    admitted_at timestamptz NOT NULL,
    committed_at timestamptz,
    lease_until timestamptz,
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    attempt bigint NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    due_at timestamptz NOT NULL,
    PRIMARY KEY (event_id, runtime_slot),
    FOREIGN KEY (event_id, runtime_slot) REFERENCES app.cart_execution_calls(event_id, runtime_slot),
    FOREIGN KEY (event_id, restaurant_id, conversation_id)
        REFERENCES app.cart_execution_sources(event_id, restaurant_id, conversation_id),
    CHECK ((generation = 0 AND attempt = 0 AND lease_until IS NULL)
        OR (generation > 0 AND attempt > 0)),
    CHECK (isfinite(admitted_at) AND isfinite(due_at)),
    CHECK (committed_at IS NULL OR isfinite(committed_at)),
    CHECK (lease_until IS NULL OR isfinite(lease_until))
);
CREATE INDEX native_cart_actions_pending ON app.native_cart_actions(due_at, event_id, runtime_slot)
    WHERE committed_at IS NULL;

-- +goose Down
-- Never discard deliberately admitted business work or its history.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.native_cart_actions) THEN
        RAISE EXCEPTION 'Native cart admission history prevents migration 12 rollback';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE app.native_cart_actions;

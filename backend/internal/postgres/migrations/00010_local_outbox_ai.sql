-- +goose Up
-- Root discriminator: an absent optional record must never turn AI into an ACK.
ALTER TABLE app.outbox
    ADD COLUMN kind text NOT NULL DEFAULT 'legacy_ack',
    ADD COLUMN restaurant_id uuid,
    ADD COLUMN conversation_id uuid,
    ADD COLUMN control_version bigint,
    ADD COLUMN creation_generation bigint,
    ADD COLUMN creation_attempt integer,
    ADD CONSTRAINT outbox_kind_shape CHECK (
        (kind = 'legacy_ack' AND restaurant_id IS NULL AND conversation_id IS NULL
         AND control_version IS NULL AND creation_generation IS NULL AND creation_attempt IS NULL)
        OR
        (kind = 'ai_reply' AND restaurant_id IS NOT NULL AND conversation_id IS NOT NULL
         AND control_version IS NOT NULL AND control_version >= 0
         AND creation_generation IS NOT NULL AND creation_generation > 0
         AND creation_attempt IS NOT NULL AND creation_attempt > 0)),
    ADD CONSTRAINT outbox_ai_source FOREIGN KEY (event_id,restaurant_id,conversation_id)
        REFERENCES app.cart_execution_sources(event_id,restaurant_id,conversation_id);
ALTER TABLE app.outbox DROP CONSTRAINT outbox_state_check;
ALTER TABLE app.outbox ADD CONSTRAINT outbox_state_check
    CHECK (state IN ('pending','sending','accepted','unknown','failed','suppressed'));
ALTER TABLE app.outbox ADD CONSTRAINT outbox_suppressed_ai
    CHECK (state <> 'suppressed' OR kind = 'ai_reply');
ALTER TABLE app.send_attempts DROP CONSTRAINT send_attempts_outcome_check;
ALTER TABLE app.send_attempts ADD CONSTRAINT send_attempts_outcome_check
    CHECK (outcome IN ('sending','accepted','unknown','failed','suppressed'));
-- Only the root row can establish kind; no parallel kind defaults on attempts.
-- +goose StatementBegin
CREATE FUNCTION app.check_suppressed_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.outcome = 'suppressed' AND NOT EXISTS (
        SELECT 1 FROM app.outbox WHERE id=NEW.outbox_id AND kind='ai_reply'
    ) THEN
        RAISE EXCEPTION 'suppression requires AI intent' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER send_attempt_suppressed_ai BEFORE INSERT OR UPDATE ON app.send_attempts
    FOR EACH ROW EXECUTE FUNCTION app.check_suppressed_attempt();

-- +goose Down
-- Fail closed before any DDL: rollback must not erase or reclassify history.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.outbox WHERE kind <> 'legacy_ack' OR state='suppressed')
       OR EXISTS (SELECT 1 FROM app.send_attempts WHERE outcome='suppressed') THEN
        RAISE EXCEPTION 'AI history prevents migration 10 rollback';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER send_attempt_suppressed_ai ON app.send_attempts;
DROP FUNCTION app.check_suppressed_attempt();
ALTER TABLE app.send_attempts DROP CONSTRAINT send_attempts_outcome_check;
ALTER TABLE app.send_attempts ADD CONSTRAINT send_attempts_outcome_check
    CHECK (outcome IN ('sending','accepted','unknown','failed'));
ALTER TABLE app.outbox DROP CONSTRAINT outbox_suppressed_ai;
ALTER TABLE app.outbox DROP CONSTRAINT outbox_state_check;
ALTER TABLE app.outbox ADD CONSTRAINT outbox_state_check
    CHECK (state IN ('pending','sending','accepted','unknown','failed'));
ALTER TABLE app.outbox DROP CONSTRAINT outbox_ai_source, DROP CONSTRAINT outbox_kind_shape,
    DROP COLUMN kind, DROP COLUMN restaurant_id, DROP COLUMN conversation_id,
    DROP COLUMN control_version, DROP COLUMN creation_generation, DROP COLUMN creation_attempt;

-- +goose Up
-- Reserve a distinct route before introducing any menu producer or consumer.
ALTER TABLE app.jobs
    ADD COLUMN route text NOT NULL DEFAULT 'legacy_ack',
    ADD COLUMN restaurant_id uuid,
    ADD COLUMN conversation_id uuid,
    ADD COLUMN admission_mode text,
    ADD COLUMN admission_version bigint,
    ADD COLUMN no_reply_reason text,
    ADD CONSTRAINT jobs_route_shape CHECK (
        (route = 'legacy_ack' AND restaurant_id IS NULL AND conversation_id IS NULL
         AND admission_mode IS NULL AND admission_version IS NULL AND no_reply_reason IS NULL)
        OR
        (route = 'menu_message' AND restaurant_id IS NOT NULL AND conversation_id IS NOT NULL
         AND admission_mode IS NOT NULL AND admission_version IS NOT NULL AND admission_version >= 0
         AND ((attempt = 0 AND generation = 0) OR (attempt > 0 AND generation > 0))
         AND (state <> 'leased' OR (attempt > 0 AND generation > 0))
         AND (
             (admission_mode = 'staff' AND state = 'completed' AND attempt = 0 AND generation = 0
              AND no_reply_reason IS NOT NULL AND no_reply_reason = 'staff_at_admission')
             OR
             (admission_mode = 'ai' AND (
                 (state IN ('queued','leased') AND no_reply_reason IS NULL)
                 OR (state = 'completed' AND (
                     (no_reply_reason IS NULL AND attempt > 0 AND generation > 0)
                     OR (no_reply_reason IS NOT NULL AND no_reply_reason = 'control_changed')
                 ))
             ))
         ))),
    ADD CONSTRAINT jobs_menu_source FOREIGN KEY (event_id,restaurant_id,conversation_id)
        REFERENCES app.cart_execution_sources(event_id,restaurant_id,conversation_id);

-- +goose Down
-- Staff/no-outbox admissions are history too. Refuse before changing any DDL.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.jobs WHERE route <> 'legacy_ack') THEN
        RAISE EXCEPTION 'Menu admission history prevents migration 11 rollback';
    END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE app.jobs DROP CONSTRAINT jobs_menu_source, DROP CONSTRAINT jobs_route_shape,
    DROP COLUMN route, DROP COLUMN restaurant_id, DROP COLUMN conversation_id,
    DROP COLUMN admission_mode, DROP COLUMN admission_version, DROP COLUMN no_reply_reason;

package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// pre11LegacyJobFormat supplies only the discriminator required by today's
// public legacy queries/whole-claim guard while constructing historical data.
// A CHECK makes menu rows impossible; no authority checks are bypassed. Remove
// it BEFORE the historical schema/row baseline and real migration execution.
// This is fixture compatibility, never a production old-schema fallback.
func pre11LegacyJobFormat(t *testing.T, dsn string) func() {
	t.Helper()
	c := claimConn(t, dsn)
	if _, err := c.Exec(t.Context(), `ALTER TABLE app.jobs ADD COLUMN route text NOT NULL DEFAULT 'legacy_ack' CHECK (route='legacy_ack')`); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := c.Exec(t.Context(), `ALTER TABLE app.jobs DROP COLUMN route`); err != nil {
			t.Fatal(err)
		}
	}
}

// freezePre10AcceptedDelivery records the historical migration-1 delivery
// format, not a runtime compatibility path. Accept/ClaimJob/CompleteJob have
// already created the genuine pending intent through their public interfaces.
// Current delivery queries require migration 10 and must not run on old DDL.
// The original fixtures claimed and finished at the same supplied instant:
// one attempt/generation, no remaining lease, and identical start/finish times.
func freezePre10AcceptedDelivery(t *testing.T, dsn, eventID, providerID string, now time.Time) {
	t.Helper()
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `UPDATE app.outbox o
SET state='accepted',attempt=1,generation=1,provider_id=$2
FROM app.inbound_events e
WHERE o.event_id=e.id AND e.provider_event_id=$1
AND o.state='pending' AND o.attempt=0 AND o.generation=0
AND o.lease_until IS NULL AND o.provider_id='' AND o.due_at <= $3
RETURNING o.id::text`, eventID, providerID, now).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.send_attempts
(outbox_id,attempt,generation,started_at,finished_at,outcome,provider_id)
VALUES($1,1,1,$2,$2,'accepted',$3)`, id, now, providerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// Only migrations 10/11's six additive outbox/job keys are excluded.
// JSONB subtraction is safe on both schemas, keeps every old key/value, and
// does not normalize business string bytes. All other tables retain their
// original row_to_json projection, counts, and deterministic full-row ordering.
func legacyMigrationFingerprintSQL(table string) string {
	row := "row_to_json(r)::text"
	if table == "jobs" {
		row = `(to_jsonb(r) - ARRAY['route','restaurant_id','conversation_id','admission_mode','admission_version','no_reply_reason'])::text`
	}
	if table == "outbox" {
		row = `(to_jsonb(r) - ARRAY['kind','restaurant_id','conversation_id','control_version','creation_generation','creation_attempt'])::text`
	}
	return `SELECT count(*)::text||':'||md5(COALESCE(string_agg(` + row + `,',' ORDER BY ` + row + `),'')) FROM app.` + table + ` r`
}

func assertMigratedLegacyMetadata(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var count int
	var legacy, applied bool
	err := conn.QueryRow(t.Context(), `SELECT count(*),
COALESCE(bool_and(kind='legacy_ack' AND restaurant_id IS NULL AND conversation_id IS NULL
AND control_version IS NULL AND creation_generation IS NULL AND creation_attempt IS NULL),false),
EXISTS(SELECT 1 FROM app.goose_db_version WHERE version_id=10 AND is_applied)
FROM app.outbox`).Scan(&count, &legacy, &applied)
	if err != nil || count == 0 || !legacy || !applied {
		t.Fatalf("legacy metadata: count=%d allLegacyUntyped=%v v10Applied=%v err=%v", count, legacy, applied, err)
	}
	err = conn.QueryRow(t.Context(), `SELECT count(*),
COALESCE(bool_and(route='legacy_ack' AND restaurant_id IS NULL AND conversation_id IS NULL
AND admission_mode IS NULL AND admission_version IS NULL AND no_reply_reason IS NULL),false),
EXISTS(SELECT 1 FROM app.goose_db_version WHERE version_id=11 AND is_applied)
FROM app.jobs`).Scan(&count, &legacy, &applied)
	if err != nil || count == 0 || !legacy || !applied {
		t.Fatalf("legacy job metadata: count=%d allLegacyUntyped=%v v11Applied=%v err=%v", count, legacy, applied, err)
	}
}

// Run after every historical hash comparison: new data must not change the
// preservation baseline. This replaces the old fixtures' delivery API exercise
// with the supported current-code/current-schema public path, not frozen SQL.
func assertCurrentLegacyDelivery(t *testing.T, store *postgres.Store) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	event := foundation.Event{ID: "post-upgrade-delivery", Sender: "post-upgrade-delivery", Text: "fresh legacy intent"}
	const reply = "  exact fresh legacy reply\né  "
	if fresh, err := store.Accept(ctx, event, now); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	job, ok, err := store.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok || job.Event != event || job.Generation != 1 || job.Attempt != 1 {
		t.Fatal(job, ok, err)
	}
	if err := store.CompleteJob(ctx, job, reply, now); err != nil {
		t.Fatal(err)
	}
	delivery, ok, err := store.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || !ok || delivery.EventID != event.ID || delivery.Text != reply || delivery.Generation != 1 || delivery.Attempt != 1 {
		t.Fatal(delivery, ok, err)
	}
	if err := store.FinishDelivery(ctx, delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "post-upgrade-provider"}, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Inspect(ctx, event.ID)
	want := foundation.Snapshot{Event: event, JobState: "completed", JobAttempt: 1, OutboxState: "accepted", ProviderID: "post-upgrade-provider", JobCount: 1, OutboxCount: 1}
	if err != nil || snapshot != want {
		t.Fatalf("current legacy delivery: got=%+v want=%+v err=%v", snapshot, want, err)
	}
}

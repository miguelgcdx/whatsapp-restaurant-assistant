package postgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestMenuRuntimeFormat(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	c := claimConn(t, dsn)
	var version int
	if err := c.QueryRow(t.Context(), `SELECT max(version_id) FROM app.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("latest runtime format version = %d, want 14", version)
	}
}

// Unit B has no producer yet: SQL only establishes the reserved route fixture.
// All isolation assertions below use existing public APIs and exact snapshots.
func reserveMenuJob(t *testing.T, f claimFixture, mode string) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO app.restaurants(id) VALUES('b0000000-0000-4000-8000-000000000001')`,
		`INSERT INTO app.cart_conversations(id,restaurant_id,source,business,sender,created_at) VALUES('b0000000-0000-4000-8000-000000000002','b0000000-0000-4000-8000-000000000001','local_fixture','fixture','fixture-sender',clock_timestamp())`,
		`INSERT INTO app.cart_execution_sources(event_id,restaurant_id,conversation_id,created_at) SELECT event_id,'b0000000-0000-4000-8000-000000000001','b0000000-0000-4000-8000-000000000002',clock_timestamp() FROM app.jobs`,
	} {
		if _, err := f.observer.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	set := `admission_mode='ai'`
	if mode == "staff" {
		set = `admission_mode='staff',state='completed',attempt=0,generation=0,lease_until=NULL,no_reply_reason='staff_at_admission'`
	}
	if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET route='menu_message',restaurant_id='b0000000-0000-4000-8000-000000000001',conversation_id='b0000000-0000-4000-8000-000000000002',admission_version=0,`+set); err != nil {
		t.Fatal(err)
	}
}

func menuSnapshot(t *testing.T, f claimFixture) foundation.Snapshot {
	t.Helper()
	s, err := f.store.Inspect(t.Context(), f.job.Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMenuRuntimeLegacyPreservation(t *testing.T) {
	f := claimSetup(t)
	ctx := t.Context()
	before := menuSnapshot(t, f)
	claimCheck(t, f, f.job, f.event, f.job.Event.Sender, nil)
	if fresh, err := f.store.Accept(ctx, f.job.Event, time.Now()); err != nil || fresh {
		t.Fatal(fresh, err)
	}
	changed := f.job.Event
	changed.Text += "changed"
	if fresh, err := f.store.Accept(ctx, changed, time.Now()); fresh || !errors.Is(err, foundation.ErrConflict) {
		t.Fatal(fresh, err)
	}
	if after := menuSnapshot(t, f); after != before {
		t.Fatal("replay/conflict/guard changed snapshot", before, after)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := now.Add(time.Second)
	if err := f.store.RetryJob(ctx, f.job, due, now); err != nil {
		t.Fatal(err)
	}
	if next, ok, err := f.store.NextDue(ctx); err != nil || !ok || !next.Equal(due) {
		t.Fatal(next, ok, err)
	}
	if job, ok, err := f.store.ClaimJob(ctx, now, time.Minute); err != nil || ok || job != (foundation.Job{}) {
		t.Fatal(job, ok, err)
	}
	job, ok, err := f.store.ClaimJob(ctx, due, time.Minute)
	if err != nil || !ok || job.Event != f.job.Event || job.Attempt != 2 || job.Generation != 2 {
		t.Fatal(job, ok, err)
	}
	if err := f.store.FailJob(ctx, job, due); err != nil {
		t.Fatal(err)
	}
	want := foundation.Snapshot{Event: job.Event, JobState: "failed", JobAttempt: 2, JobCount: 1}
	if got := menuSnapshot(t, f); got != want {
		t.Fatal(got, want)
	}
	if next, ok, err := f.store.NextDue(ctx); err != nil || ok || !next.IsZero() {
		t.Fatal(next, ok, err)
	}
	// Also retains exact CompleteJob/outbox/delivery/Inspect behavior on a
	// separate sender, including generations/attempts and accepted provider ID.
	assertCurrentLegacyDelivery(t, f.store)
}

func TestMenuRuntimeLegacyClaimExclusion(t *testing.T) {
	f := claimSetup(t)
	reserveMenuJob(t, f, "ai")
	for _, state := range []string{"queued", "leased"} {
		t.Run(state, func(t *testing.T) {
			lease := "NULL"
			if state == "leased" {
				lease = "clock_timestamp()-interval '1 second'"
			}
			if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET state=$1,lease_until=`+lease, state); err != nil {
				t.Fatal(err)
			}
			before := menuSnapshot(t, f)
			rowsBefore := menuRows(t, f.observer, false)
			job, ok, err := f.store.ClaimJob(t.Context(), time.Now(), time.Minute)
			if err != nil || ok || job != (foundation.Job{}) {
				t.Fatalf("reserved claim = %+v %v %v", job, ok, err)
			}
			if after := menuSnapshot(t, f); after != before {
				t.Fatalf("claim changed snapshot: %+v -> %+v", before, after)
			}
			if !reflect.DeepEqual(menuRows(t, f.observer, false), rowsBefore) {
				t.Fatal("claim changed persisted rows/counters")
			}
		})
	}
}

func TestMenuRuntimeLegacyMutationExclusion(t *testing.T) {
	for _, operation := range []string{"complete", "retry", "fail"} {
		t.Run(operation, func(t *testing.T) {
			f := claimSetup(t)
			reserveMenuJob(t, f, "ai")
			before := menuSnapshot(t, f)
			rowsBefore := menuRows(t, f.observer, false)
			now := time.Now()
			var err error
			switch operation {
			case "complete":
				err = f.store.CompleteJob(t.Context(), f.job, "must not become ACK", now)
			case "retry":
				err = f.store.RetryJob(t.Context(), f.job, now.Add(time.Minute), now)
			case "fail":
				err = f.store.FailJob(t.Context(), f.job, now)
			}
			if !errors.Is(err, foundation.ErrStaleLease) {
				t.Fatalf("%s reserved claim: %v", operation, err)
			}
			if after := menuSnapshot(t, f); after != before {
				t.Fatalf("mutation changed snapshot: %+v -> %+v", before, after)
			}
			if !reflect.DeepEqual(menuRows(t, f.observer, false), rowsBefore) {
				t.Fatal("mutation changed persisted rows/counters")
			}
		})
	}
}

func TestMenuRuntimeLegacyGuardExclusion(t *testing.T) {
	f := claimSetup(t)
	reserveMenuJob(t, f, "ai")
	before := menuSnapshot(t, f)
	claimCheck(t, f, f.job, f.event, f.job.Event.Sender, foundation.ErrStaleLease)
	if after := menuSnapshot(t, f); after != before {
		t.Fatalf("guard changed snapshot: %+v -> %+v", before, after)
	}
}

func TestMenuRuntimeLegacyNextDueExclusion(t *testing.T) {
	f := claimSetup(t)
	reserveMenuJob(t, f, "ai")
	for _, state := range []string{"queued", "leased"} {
		t.Run(state, func(t *testing.T) {
			lease := "NULL"
			if state == "leased" {
				lease = "clock_timestamp()+interval '1 minute'"
			}
			if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET state=$1,lease_until=`+lease, state); err != nil {
				t.Fatal(err)
			}
			before := menuSnapshot(t, f)
			next, ok, err := f.store.NextDue(t.Context())
			if err != nil || ok || !next.IsZero() {
				t.Fatalf("reserved next due = %v %v %v", next, ok, err)
			}
			if after := menuSnapshot(t, f); after != before {
				t.Fatalf("NextDue changed snapshot: %+v -> %+v", before, after)
			}
		})
	}
}

func TestMenuRuntimeCrossRoutePredecessor(t *testing.T) {
	for _, state := range []string{"queued", "leased"} {
		t.Run(state, func(t *testing.T) {
			f := claimSetup(t)
			reserveMenuJob(t, f, "ai")
			if state == "queued" {
				if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET state='queued',lease_until=NULL`); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			event := foundation.Event{ID: "later-legacy", Sender: f.job.Event.Sender, Text: "exact legacy bytes"}
			if fresh, err := f.store.Accept(t.Context(), event, now); err != nil || !fresh {
				t.Fatal(fresh, err)
			}
			before := menuSnapshot(t, f)
			if next, ok, err := f.store.NextDue(t.Context()); err != nil || ok || !next.IsZero() {
				t.Fatal(next, ok, err)
			}
			if job, ok, err := f.store.ClaimJob(t.Context(), now, time.Minute); err != nil || ok || job != (foundation.Job{}) {
				t.Fatal(job, ok, err)
			}
			if after := menuSnapshot(t, f); after != before {
				t.Fatal("predecessor changed", before, after)
			}
			if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET state='completed',lease_until=NULL,no_reply_reason='control_changed' WHERE id=$1`, f.job.ID); err != nil {
				t.Fatal(err)
			}
			if next, ok, err := f.store.NextDue(t.Context()); err != nil || !ok || !next.Equal(now.Truncate(time.Microsecond)) {
				t.Fatal(next, ok, err)
			}
			job, ok, err := f.store.ClaimJob(t.Context(), now, time.Minute)
			if err != nil || !ok || job.Event != event || job.Attempt != 1 || job.Generation != 1 {
				t.Fatal(job, ok, err)
			}
			if err := f.store.FailJob(t.Context(), job, now); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.Inspect(t.Context(), event.ID)
			want := foundation.Snapshot{Event: event, JobState: "failed", JobAttempt: 1, JobCount: 1}
			if err != nil || got != want {
				t.Fatal(got, want, err)
			}
		})
	}
}

func TestMenuRuntimeLegacyAcceptConflict(t *testing.T) {
	for _, mode := range []string{"ai", "staff"} {
		t.Run(mode, func(t *testing.T) {
			f := claimSetup(t)
			reserveMenuJob(t, f, mode)
			before := menuSnapshot(t, f)
			rowsBefore := menuRows(t, f.observer, false)
			for _, text := range []string{f.job.Event.Text, "different digest"} {
				e := f.job.Event
				e.Text = text
				fresh, err := f.store.Accept(t.Context(), e, time.Now())
				if fresh || !errors.Is(err, foundation.ErrConflict) {
					t.Fatalf("legacy replay of menu = %v %v", fresh, err)
				}
				if after := menuSnapshot(t, f); after != before {
					t.Fatalf("rejected intake changed snapshot: %+v -> %+v", before, after)
				}
				if !reflect.DeepEqual(menuRows(t, f.observer, false), rowsBefore) {
					t.Fatal("rejected intake changed persisted rows/counters")
				}
			}
		})
	}
}

func TestMenuRuntimeConstraints(t *testing.T) {
	f := claimSetup(t)
	reserveMenuJob(t, f, "ai")
	before := menuSnapshot(t, f)
	for _, set := range []string{
		`route='other'`, `route='legacy_ack'`, `restaurant_id=NULL`, `conversation_id=NULL`,
		`admission_mode=NULL`, `admission_mode='human'`, `admission_version=NULL`, `admission_version=-1`,
		`attempt=0`, `generation=0`,
		`no_reply_reason='unknown'`, `no_reply_reason='staff_at_admission'`, `no_reply_reason='control_changed'`,
		`admission_mode='staff'`, `admission_mode='staff',state='completed',lease_until=NULL,no_reply_reason='staff_at_admission'`,
		`state='completed',lease_until=NULL,attempt=0,generation=0`,
		`admission_mode='staff',state='completed',lease_until=NULL,attempt=0,generation=0,no_reply_reason=NULL`,
		`admission_mode='staff',state='completed',lease_until=NULL,attempt=0,generation=0,no_reply_reason='control_changed'`,
		`conversation_id='b0000000-0000-4000-8000-000000000009'`,
	} {
		t.Run(set, func(t *testing.T) {
			tx, err := f.observer.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(t.Context(), `UPDATE app.jobs SET `+set+` WHERE id=$1`, f.job.ID)
			if rollbackErr := tx.Rollback(t.Context()); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			var constraint *pgconn.PgError
			if !errors.As(err, &constraint) || (constraint.Code != "23514" && constraint.Code != "23503") {
				t.Fatalf("expected shape/source rejection: %v", err)
			}
			if after := menuSnapshot(t, f); after != before {
				t.Fatal("invalid metadata changed snapshot", before, after)
			}
		})
	}
	// Zero is a valid admission epoch; terminal AI control-change may precede a claim.
	if _, err := f.observer.Exec(t.Context(), `UPDATE app.jobs SET state='completed',lease_until=NULL,attempt=0,generation=0,no_reply_reason='control_changed'`); err != nil {
		t.Fatal(err)
	}
}

// Full old rows/counts are compared. Only the six deliberate additive job keys
// are projected away during Up; schema and Goose are compared on refused Down.
func menuRows(t *testing.T, c *pgx.Conn, project bool) map[string]string {
	t.Helper()
	rows, err := c.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='app' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, table := range tables {
		if project && table == "goose_db_version" {
			continue
		}
		value := "to_jsonb(r)"
		if project && table == "jobs" {
			value += ` - ARRAY['route','restaurant_id','conversation_id','admission_mode','admission_version','no_reply_reason']`
		}
		var data string
		if err := c.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(v ORDER BY v),'[]'::jsonb)::text FROM (SELECT `+value+` AS v FROM app.`+pgx.Identifier{table}.Sanitize()+` r) s`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		result[table] = data
	}
	var schema string
	if err := c.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position)::text FROM information_schema.columns c WHERE table_schema='app'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !project {
		result["schema"] = schema
	}
	return result
}

func TestMenuRuntimeMigrationPreservesV10AndDown(t *testing.T) {
	dsn := testpg.Start(t)
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	if _, err := p.UpTo(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 10)
	c := claimConn(t, dsn)
	// Frozen v10 data: explicit non-default old values, exact timestamps and bytes.
	for _, sql := range []string{
		`INSERT INTO app.conversation_sequences VALUES('v10-sender',7)`,
		`INSERT INTO app.inbound_events VALUES('c0000000-0000-4000-8000-000000000001','v10-event','v10-sender',7,E' exact\n é ',decode(repeat('00',32),'hex'),'2026-01-01T01:02:03Z')`,
		`INSERT INTO app.jobs VALUES('c0000000-0000-4000-8000-000000000002','c0000000-0000-4000-8000-000000000001','completed','2026-01-01T01:02:04Z',3,4,NULL)`,
		`INSERT INTO app.outbox(id,event_id,content,state,due_at,attempt,generation,provider_id) VALUES('c0000000-0000-4000-8000-000000000003','c0000000-0000-4000-8000-000000000001',E' exact reply\n é ','accepted','2026-01-01T01:02:05Z',2,3,'v10-provider')`,
		`INSERT INTO app.send_attempts VALUES('c0000000-0000-4000-8000-000000000003',1,2,'2026-01-01T01:02:06Z','2026-01-01T01:02:07Z','failed',''),('c0000000-0000-4000-8000-000000000003',2,3,'2026-01-01T01:02:08Z','2026-01-01T01:02:09Z','accepted','v10-provider')`,
	} {
		if _, err := c.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	seedAIFormat(t, c)
	for _, sql := range []string{
		`INSERT INTO app.staff_memberships VALUES('a0000000-0000-4000-8000-000000000001','v10-actor',true)`,
		`INSERT INTO app.conversation_controls VALUES('a0000000-0000-4000-8000-000000000001','a0000000-0000-4000-8000-000000000002','staff',0,'2026-01-01T01:02:10Z')`,
		`INSERT INTO app.conversation_control_operations VALUES('a0000000-0000-4000-8000-000000000001','v10-actor','v10-control','a0000000-0000-4000-8000-000000000002','initialize','staff',NULL,'staff',0,'2026-01-01T01:02:10Z')`,
	} {
		if _, err := c.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	event := foundation.Event{ID: "v10-event", Sender: "v10-sender", Text: " exact\n é "}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if _, err := c.Exec(t.Context(), `UPDATE app.inbound_events SET digest=$1 WHERE provider_event_id=$2`, digest[:], event.ID); err != nil {
		t.Fatal(err)
	}
	before, exactBefore := menuRows(t, c, true), menuRows(t, c, false)
	if _, exists := before["native_cart_actions"]; exists {
		t.Fatal("v10 unexpectedly contains native_cart_actions")
	}
	// Only the known empty tables added after v10 are excluded from this
	// old-row view. Every old table, field and counter retains exact comparison.
	latestRows := func() map[string]string {
		t.Helper()
		rows := menuRows(t, c, true)
		if data, exists := rows["native_cart_actions"]; !exists || data != "[]" {
			t.Fatal("expected only an empty new native_cart_actions table", rows)
		}
		delete(rows, "native_cart_actions")
		if data, exists := rows["unresolved_cart_edits"]; !exists || data != "[]" {
			t.Fatalf("expected empty new unresolved_cart_edits table: %q", data)
		}
		delete(rows, "unresolved_cart_edits")
		for _, name := range []string{"pickup_candidates", "pickup_candidate_units", "pickup_presentations", "pickup_orders", "pickup_evidence", "pickup_intents", "pickup_staff_operations", "pickup_local_replies"} {
			if data, exists := rows[name]; !exists || data != "[]" {
				t.Fatalf("expected empty new pickup table %s: %q", name, data)
			}
			delete(rows, name)
		}
		return rows
	}
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 14)
	if after := latestRows(); !reflect.DeepEqual(after, before) {
		t.Fatal("Up changed old fields/counts", before, after)
	}
	var valid bool
	if err := c.QueryRow(t.Context(), `SELECT bool_and(route='legacy_ack' AND restaurant_id IS NULL AND conversation_id IS NULL AND admission_mode IS NULL AND admission_version IS NULL AND no_reply_reason IS NULL) FROM app.jobs`).Scan(&valid); err != nil || !valid {
		t.Fatal(valid, err)
	}
	store, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Inspect(t.Context(), "v10-event")
	want := foundation.Snapshot{Event: foundation.Event{ID: "v10-event", Sender: "v10-sender", Text: " exact\n é "}, JobState: "completed", JobAttempt: 3, OutboxState: "accepted", ProviderID: "v10-provider", JobCount: 1, OutboxCount: 1}
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
	if fresh, err := store.Accept(t.Context(), event, time.Now()); err != nil || fresh {
		t.Fatal("preserved replay", fresh, err)
	}
	if after := latestRows(); !reflect.DeepEqual(after, before) {
		t.Fatal("replay changed historical rows/counters", before, after)
	}
	assertCurrentLegacyDelivery(t, store)
	// Prove empty v12 can roll back, then restore the original v10 boundary.
	oldRows := latestRows()
	for _, version := range []int64{11, 10} {
		if _, err := p.DownTo(t.Context(), version); err != nil {
			t.Fatal(err)
		}
		assertMigrationVersion(t, p, version)
		if after := menuRows(t, c, true); !reflect.DeepEqual(after, oldRows) {
			t.Fatal("Down changed rows", oldRows, after)
		}
	}
	after := menuRows(t, c, false)
	if after["schema"] != exactBefore["schema"] || after["goose_db_version"] != exactBefore["goose_db_version"] {
		t.Fatal("Down did not restore v10 schema/Goose")
	}
}

func TestMenuRuntimeHistoryRefusesDown(t *testing.T) {
	for _, mode := range []string{"ai", "staff"} {
		t.Run(mode, func(t *testing.T) {
			f := claimSetup(t)
			reserveMenuJob(t, f, mode)
			p, closeDB := aiMigrationProvider(t, f.dsn)
			defer closeDB()
			if _, err := p.DownTo(t.Context(), 11); err != nil {
				t.Fatal(err)
			}
			assertMigrationVersion(t, p, 11)
			before := menuRows(t, f.observer, false)
			snapshot := menuSnapshot(t, f)
			if mode == "staff" {
				want := foundation.Snapshot{Event: f.job.Event, JobState: "completed", JobCount: 1}
				if snapshot != want {
					t.Fatal(snapshot, want)
				}
			}
			if snapshot.OutboxCount != 0 {
				t.Fatal("fixture has outbox", snapshot)
			}
			if _, err := p.DownTo(t.Context(), 10); err == nil || !strings.Contains(err.Error(), "Menu admission history prevents migration 11 rollback") {
				t.Fatal("Down failed to refuse menu history", err)
			}
			assertMigrationVersion(t, p, 11)
			if after := menuRows(t, f.observer, false); !reflect.DeepEqual(after, before) {
				t.Fatal("refused Down changed data/schema/Goose", before, after)
			}
			if after := menuSnapshot(t, f); after != snapshot {
				t.Fatal("refused Down changed snapshot", snapshot, after)
			}
		})
	}
}

func TestMenuRuntimeNativeHistoryRefusesDown(t *testing.T) {
	f := claimSetup(t)
	reserveMenuJob(t, f, "ai")
	// Reuse the reserved job/source scope; the execution call and admission
	// reference that same event, slot, operation and claimed job.
	for _, sql := range []string{
		`INSERT INTO app.cart_execution_calls(event_id,runtime_slot,restaurant_id,conversation_id,action,canonical_intent,fingerprint,operation_id,created_at) SELECT event_id,0,restaurant_id,conversation_id,'create','{"action":"create"}',decode(repeat('00',32),'hex'),'d0000000-0000-4000-8000-000000000001',created_at FROM app.cart_execution_sources`,
		`INSERT INTO app.native_cart_actions(event_id,runtime_slot,operation_id,restaurant_id,conversation_id,admission_job_id,admission_version,admission_generation,admission_attempt,admitted_at,due_at) SELECT c.event_id,c.runtime_slot,c.operation_id,c.restaurant_id,c.conversation_id,j.id,j.admission_version,j.generation,j.attempt,clock_timestamp(),j.due_at FROM app.cart_execution_calls c JOIN app.jobs j ON j.event_id=c.event_id`,
	} {
		if tag, err := f.observer.Exec(t.Context(), sql); err != nil || tag.RowsAffected() != 1 {
			t.Fatal("native history fixture", tag, err)
		}
	}
	p, closeDB := aiMigrationProvider(t, f.dsn)
	defer closeDB()
	assertMigrationVersion(t, p, 14)
	if _, err := p.DownTo(t.Context(), 12); err != nil {
		t.Fatal(err)
	}
	assertMigrationVersion(t, p, 12)
	before := menuRows(t, f.observer, false)
	snapshot := menuSnapshot(t, f)
	if _, err := p.DownTo(t.Context(), 11); err == nil || !strings.Contains(err.Error(), "Native cart admission history prevents migration 12 rollback") {
		t.Fatal("Down failed to refuse native history", err)
	}
	assertMigrationVersion(t, p, 12)
	if after := menuRows(t, f.observer, false); !reflect.DeepEqual(after, before) {
		t.Fatal("refused native Down changed data/counters/schema/Goose", before, after)
	}
	if after := menuSnapshot(t, f); after != snapshot {
		t.Fatal("refused native Down changed snapshot", snapshot, after)
	}
}

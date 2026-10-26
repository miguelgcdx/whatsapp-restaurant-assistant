package postgres_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

// S3/S4: the root discriminator is public schema, not an optional sidecar.
// Existing CompleteJob must continue to produce byte-identical, untyped ACKs.
func TestOutboxAIMigration(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
	provider, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	finishLegacyFormat := pre11LegacyJobFormat(t, dsn)
	now := time.Now()
	event := foundation.Event{ID: "legacy-before-ai", Sender: "owned-sender", Text: "question"}
	if fresh, err := store.Accept(ctx, event, now); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	job, ok, err := store.ClaimJob(ctx, now, time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	const reply = "  exact legacy reply\né  "
	if err := store.CompleteJob(ctx, job, reply, now); err != nil {
		t.Fatal(err)
	}
	finishLegacyFormat()
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var kind, content string
	var typedNull bool
	if err := conn.QueryRow(ctx, `SELECT kind,content,restaurant_id IS NULL AND conversation_id IS NULL AND control_version IS NULL AND creation_generation IS NULL AND creation_attempt IS NULL FROM app.outbox`).Scan(&kind, &content, &typedNull); err != nil {
		t.Fatal(err)
	}
	if kind != "legacy_ack" || content != reply || !typedNull {
		t.Fatal(kind, content, typedNull)
	}
	delivery, ok, err := store.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || !ok || delivery.Text != reply || delivery.EventID != event.ID {
		t.Fatal(delivery, ok, err)
	}
	if err := store.FinishDelivery(ctx, delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "legacy-provider-id"}, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Inspect(ctx, event.ID)
	if err != nil || snapshot.JobState != "completed" || snapshot.OutboxState != "accepted" || snapshot.ProviderID != "legacy-provider-id" || snapshot.JobCount != 1 || snapshot.OutboxCount != 1 {
		t.Fatal(snapshot, err)
	}
}

func aiMigrationProvider(t *testing.T, dsn string) (*goose.Provider, func()) {
	t.Helper()
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*cfg.ConnConfig)
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(t.Context(), `CREATE SCHEMA IF NOT EXISTS app`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"), goose.WithTableName("app.goose_db_version"))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return p, func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}
}

// Schema constraints are a public format boundary; all rejected statements run
// in their own rolled-back transaction, leaving the accepted row unchanged.
func TestOutboxAIConstraints(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	c, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(t.Context())
	seedAIFormat(t, c)
	const oid = "a0000000-0000-4000-8000-000000000004"
	for _, tc := range []struct{ name, set string }{{"kind", "kind='other'"}, {"null-restaurant", "restaurant_id=NULL"}, {"null-conversation", "conversation_id=NULL"}, {"null-version", "control_version=NULL"}, {"negative-version", "control_version=-1"}, {"null-generation", "creation_generation=NULL"}, {"zero-generation", "creation_generation=0"}, {"null-attempt", "creation_attempt=NULL"}, {"zero-attempt", "creation_attempt=0"}, {"legacy-typed", "kind='legacy_ack'"}, {"foreign-source", "conversation_id='a0000000-0000-4000-8000-000000000009'"}} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := c.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET `+tc.set+` WHERE id=$1`, oid)
			var constraint *pgconn.PgError
			if !errors.As(err, &constraint) || (constraint.Code != "23514" && constraint.Code != "23503") {
				t.Fatal(err)
			}
		})
	}
	// A legacy row cannot claim either form of known-no-I/O suppression.
	if _, err = c.Exec(t.Context(), `UPDATE app.outbox SET kind='legacy_ack',restaurant_id=NULL,conversation_id=NULL,control_version=NULL,creation_generation=NULL,creation_attempt=NULL WHERE id=$1`, oid); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE app.outbox SET state='suppressed' WHERE id=$1`, `INSERT INTO app.send_attempts(outbox_id,attempt,generation,started_at,outcome) VALUES($1,1,1,clock_timestamp(),'suppressed')`} {
		_, err = c.Exec(t.Context(), sql, oid)
		var constraint *pgconn.PgError
		if !errors.As(err, &constraint) || constraint.Code != "23514" {
			t.Fatal(err)
		}
	}
	var count int
	if err = c.QueryRow(t.Context(), `SELECT count(*) FROM app.send_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func seedAIFormat(t *testing.T, c *pgx.Conn) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO app.restaurants(id) VALUES('a0000000-0000-4000-8000-000000000001')`,
		`INSERT INTO app.cart_conversations(id,restaurant_id,source,business,sender,created_at) VALUES('a0000000-0000-4000-8000-000000000002','a0000000-0000-4000-8000-000000000001','local_fixture','fixture','fixture',clock_timestamp())`,
		`INSERT INTO app.conversation_sequences(sender,next_sequence) VALUES('fixture',1)`,
		`INSERT INTO app.inbound_events(id,provider_event_id,sender,sequence,content,digest,created_at) VALUES('a0000000-0000-4000-8000-000000000003','fixture','fixture',1,'question',decode(repeat('00',32),'hex'),clock_timestamp())`,
		`INSERT INTO app.cart_execution_sources(event_id,restaurant_id,conversation_id,created_at) VALUES('a0000000-0000-4000-8000-000000000003','a0000000-0000-4000-8000-000000000001','a0000000-0000-4000-8000-000000000002',clock_timestamp())`,
		`INSERT INTO app.outbox(id,event_id,content,state,due_at,kind,restaurant_id,conversation_id,control_version,creation_generation,creation_attempt) VALUES('a0000000-0000-4000-8000-000000000004','a0000000-0000-4000-8000-000000000003',' exact AI bytes ','pending',clock_timestamp(),'ai_reply','a0000000-0000-4000-8000-000000000001','a0000000-0000-4000-8000-000000000002',0,1,1)`,
	} {
		if _, err := c.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

// S3/S4/S6: refusal preserves complete suppressed AI history and migration
// bookkeeping. SQL is the public schema format, not a private helper assertion.
func TestOutboxAISuppressedHistoryDown(t *testing.T) {
	ctx := t.Context()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	seedAIFormat(t, c)
	const id = "a0000000-0000-4000-8000-000000000004"
	if _, err = c.Exec(ctx, `UPDATE app.outbox SET state='suppressed',generation=1,attempt=1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, `INSERT INTO app.send_attempts(outbox_id,attempt,generation,started_at,finished_at,outcome) VALUES($1,1,1,clock_timestamp(),clock_timestamp(),'suppressed')`, id); err != nil {
		t.Fatal(err)
	}
	type history struct{ root, attempts, columns, versions string }
	capture := func() history {
		var h history
		queries := []struct {
			sql  string
			dest *string
		}{
			{`SELECT row_to_json(o)::text FROM app.outbox o`, &h.root},
			{`SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY attempt),'[]'::jsonb)::text FROM app.send_attempts a`, &h.attempts},
			{`SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position)::text FROM information_schema.columns c WHERE table_schema='app' AND table_name IN ('outbox','send_attempts')`, &h.columns},
			{`SELECT jsonb_agg(to_jsonb(v) ORDER BY id)::text FROM app.goose_db_version v`, &h.versions},
		}
		for _, q := range queries {
			if err := c.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
				t.Fatal(err)
			}
		}
		return h
	}
	before := capture()
	var kind, content, state, outcome string
	var rootCount, attemptCount, columnCount int
	var applied bool
	assertHistory := func() {
		t.Helper()
		if err := c.QueryRow(ctx, `SELECT kind,content,state FROM app.outbox WHERE id=$1`, id).Scan(&kind, &content, &state); err != nil || kind != "ai_reply" || content != " exact AI bytes " || state != "suppressed" {
			t.Fatal(kind, content, state, err)
		}
		if err := c.QueryRow(ctx, `SELECT outcome FROM app.send_attempts WHERE outbox_id=$1 AND attempt=1 AND generation=1`, id).Scan(&outcome); err != nil || outcome != "suppressed" {
			t.Fatal(outcome, err)
		}
		if err := c.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.outbox),(SELECT count(*) FROM app.send_attempts),(SELECT count(*) FROM information_schema.columns WHERE table_schema='app' AND table_name='outbox' AND column_name IN ('kind','restaurant_id','conversation_id','control_version','creation_generation','creation_attempt')),EXISTS(SELECT 1 FROM app.goose_db_version WHERE version_id=10 AND is_applied)`).Scan(&rootCount, &attemptCount, &columnCount, &applied); err != nil || rootCount != 1 || attemptCount != 1 || columnCount != 6 || !applied {
			t.Fatal(rootCount, attemptCount, columnCount, applied, err)
		}
	}
	assertHistory()
	p, closeDB := aiMigrationProvider(t, dsn)
	defer closeDB()
	// No menu jobs: first remove 11, then prove the original 10 -> 9 refusal.
	if _, err = p.Down(ctx); err != nil {
		t.Fatal(err)
	}
	before = capture()
	if _, err = p.Down(ctx); err == nil || err.Error() == "" {
		t.Fatal("suppressed history did not reject Down", err)
	}
	assertHistory()
	if after := capture(); after != before {
		t.Fatalf("refused Down changed history/schema/bookkeeping: before=%+v after=%+v", before, after)
	}
}

func TestOutboxAIDown(t *testing.T) {
	for _, ai := range []bool{false, true} {
		name := "legacy-only"
		if ai {
			name = "AI-history-refuses"
		}
		t.Run(name, func(t *testing.T) {
			dsn := testpg.Start(t)
			if err := postgres.Migrate(t.Context(), dsn); err != nil {
				t.Fatal(err)
			}
			c, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(t.Context())
			if ai {
				seedAIFormat(t, c)
			} else {
				// Build genuine legacy history through the unchanged public
				// API; never reclassify AI history to make Down succeed.
				store, err := postgres.Open(t.Context(), dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				now := time.Now()
				if fresh, err := store.Accept(t.Context(), foundation.Event{ID: "legacy-down", Sender: "legacy-down", Text: "question"}, now); err != nil || !fresh {
					t.Fatal(fresh, err)
				}
				job, ok, err := store.ClaimJob(t.Context(), now, time.Minute)
				if err != nil || !ok {
					t.Fatal(job, ok, err)
				}
				if err = store.CompleteJob(t.Context(), job, " exact AI bytes ", now); err != nil {
					t.Fatal(err)
				}
			}
			p, closeDB := aiMigrationProvider(t, dsn)
			defer closeDB()
			if _, err = p.Down(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, err = p.Down(t.Context())
			if ai && err == nil {
				t.Fatal("AI history was erased by Down")
			}
			if !ai && err != nil {
				t.Fatal(err)
			}
			var content, state string
			if err = c.QueryRow(t.Context(), `SELECT content,state FROM app.outbox`).Scan(&content, &state); err != nil || content != " exact AI bytes " || state != "pending" {
				t.Fatal(content, state, err)
			}
			if ai {
				var kind string
				if err = c.QueryRow(t.Context(), `SELECT kind FROM app.outbox`).Scan(&kind); err != nil || kind != "ai_reply" {
					t.Fatal(kind, err)
				}
			} else {
				var n int
				if err = c.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema='app' AND table_name='outbox' AND column_name='kind'`).Scan(&n); err != nil || n != 0 {
					t.Fatal(n, err)
				}
			}
		})
	}
}

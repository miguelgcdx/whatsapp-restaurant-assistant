package menugate_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/menugate"
)

type legacyScope struct {
	Restaurant, Conversation string
	EventUUID, ProviderEvent string
	Business, Sender         string
}
type legacyState struct {
	Source         legacyScope
	Job            foundation.Job
	Snapshot       foundation.Snapshot
	Queued         menugate.Receipt
	ControlID      string
	Mode           conversation.Mode
	Version        int64
	Errors         [4]string // Inspect, control, ReadQueued, BindSource; nil is "<nil>".
	Counters       string
	Attempts       int
	AttemptOutcome string
	rows           map[string]string // Within-run proof only: never serialize schema-dependent hashes.
}
type legacyTrace struct {
	Step                      string
	Proposal                  menuengine.Result
	Receipt                   menugate.Receipt
	Error                     string
	Dispatched                bool
	State                     legacyState
	ModelCalls, ProviderCalls int32
}

func legacyRead(t *testing.T, f *fixture, v turn, g *menugate.Gate) (o legacyState) {
	t.Helper()
	snapshot, a := f.jobs.Inspect(t.Context(), v.job.Event.ID)
	control, b := f.controls.Read(t.Context(), f.token, v.local.ConversationID())
	queued, c := g.ReadQueued(t.Context(), v.source)
	source, d := f.cart.BindSource(t.Context(), v.local, v.job.Event.ID)
	o.Source = legacyScope{source.RestaurantID(), source.ConversationID().String(), source.EventUUID(), source.EventProviderID(), source.Business(), source.Sender()}
	o.Job, o.Snapshot, o.Queued = v.job, snapshot, queued
	o.ControlID, o.Mode, o.Version = control.ConversationID.String(), control.Mode, control.Version
	o.Errors = [4]string{fmt.Sprint(a), fmt.Sprint(b), fmt.Sprint(c), fmt.Sprint(d)}
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),coalesce(min(a.outcome),'') FROM app.send_attempts a JOIN app.outbox o ON o.id=a.outbox_id WHERE o.event_id=$1`, v.source.EventUUID()).Scan(&o.Attempts, &o.AttemptOutcome); err != nil {
		t.Fatal(err)
	}
	if err := f.conn.QueryRow(t.Context(), `SELECT COALESCE(json_agg(s ORDER BY sender)::text,'[]') FROM app.conversation_sequences s`).Scan(&o.Counters); err != nil {
		t.Fatal(err)
	}
	o.rows = f.hashes(t)
	return
}

// Preserve every unrelated row and every immutable field of the target job.
func legacyOther(t *testing.T, f *fixture, v turn) map[string]string {
	t.Helper()
	rows := f.hashes(t)
	for table, projection := range map[string]string{
		"jobs":          `SELECT CASE WHEN id=$1 THEN to_jsonb(r)-'state'-'lease_until' ELSE to_jsonb(r) END AS data FROM app.jobs r`,
		"outbox":        `SELECT to_jsonb(r) AS data FROM app.outbox r WHERE event_id<>$2`,
		"send_attempts": `SELECT to_jsonb(r) AS data FROM app.send_attempts r WHERE outbox_id NOT IN (SELECT id FROM app.outbox WHERE event_id=$2)`,
	} {
		var value string
		if err := f.conn.QueryRow(t.Context(), `WITH args AS (SELECT $1::uuid,$2::uuid), r AS (`+projection+`) SELECT count(*)::text||':'||md5(COALESCE(string_agg(data::text,',' ORDER BY data::text),'')) FROM r`, v.job.ID, v.source.EventUUID()).Scan(&value); err != nil {
			t.Fatal(err)
		}
		rows[table] = value
	}
	return rows
}

// S1/S3 legacy preservation; S2 native epochs must not constrain legacy claims.
func TestMenuGateLegacyCompatibility(t *testing.T) {
	f := setup(t)
	v := f.fresh(t, conversation.ModeAI)
	f.mode(t, v, conversation.ModeAI)
	var models, providers atomic.Int32
	const reply = "  Legacy epoch reply\né\n "
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
		models.Add(1)
		return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
	}))
	record := func(step string, p menuengine.Result, r menugate.Receipt, err error, dispatched bool, subject turn) legacyState {
		o := legacyRead(t, f, subject, g)
		wire, marshalErr := json.Marshal(legacyTrace{step, p, r, fmt.Sprint(err), dispatched, o, models.Load(), providers.Load()})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		fmt.Println(string(wire))
		return o
	}
	same := func(want, got legacyState, calls int32) {
		t.Helper()
		if !reflect.DeepEqual(want, got) || models.Load() != calls || providers.Load() != 0 {
			t.Errorf("public state/errors/rows/counters or I/O differ: want=%+v got=%+v model=%d provider=%d", want, got, models.Load(), providers.Load())
		}
	}
	before := record("ready-ai1", menuengine.Result{}, menugate.Receipt{}, nil, false, v)
	var legacy bool
	if err := f.conn.QueryRow(t.Context(), `SELECT
		(NOT (to_jsonb(j)?'route') OR to_jsonb(j)->>'route'='legacy_ack') AND NOT EXISTS
		(SELECT 1 FROM jsonb_each(to_jsonb(j)) p WHERE p.key IN ('restaurant_id','conversation_id','admission_mode','admission_version','no_reply_reason') AND p.value<>'null'::jsonb)
		FROM app.jobs j WHERE id=$1`, v.job.ID).Scan(&legacy); err != nil || !legacy {
		t.Fatal("legacy NULL/absent native metadata readiness", legacy, err)
	}
	if before.Errors != ([4]string{"<nil>", "<nil>", menugate.ErrNotFound.Error(), "<nil>"}) ||
		before.Mode != conversation.ModeAI || before.Version != 1 || before.ControlID != v.local.ConversationID().String() ||
		before.Source != (legacyScope{restaurant, v.local.ConversationID().String(), v.source.EventUUID(), v.job.Event.ID, "fixture-business", v.job.Event.Sender}) ||
		v.job.Generation != 1 || v.job.Attempt != 1 || before.Queued != (menugate.Receipt{}) ||
		before.Snapshot != (foundation.Snapshot{Event: v.job.Event, JobState: "leased", JobAttempt: 1, JobCount: 1}) {
		t.Fatal("exact AI1 legacy readiness", before)
	}
	p, err := g.Run(t.Context(), v.source, v.job)
	after := record("run", p, menugate.Receipt{}, err, false, v)
	same(before, after, 1)
	if err != nil || p != (menuengine.Result{Reply: reply}) || models.Load() != 1 {
		t.Error("wrong entire legacy proposal/model count", p, err, models.Load())
	}
	other := legacyOther(t, f, v)
	r, err := g.RunAndQueue(t.Context(), v.source, v.job)
	after = record("queue", menuengine.Result{}, r, err, false, v)
	afterOther := legacyOther(t, f, v)
	want := menugate.Receipt{OutboxID: r.OutboxID, Content: reply, ControlVersion: 1, State: "pending"}
	if err != nil || want.OutboxID == "" || r != want || models.Load() != 2 {
		t.Fatal("wrong entire legacy AI1 receipt", r, err, models.Load())
	}
	expected := before
	expected.Queued, expected.Errors[2] = want, "<nil>"
	expected.Snapshot = foundation.Snapshot{Event: v.job.Event, JobState: "completed", JobAttempt: 1, OutboxState: "pending", JobCount: 1, OutboxCount: 1}
	expected.rows = after.rows
	same(expected, after, 2)
	if !reflect.DeepEqual(other, afterOther) {
		t.Error("queue changed unrelated rows/immutable job")
	}
	attemptState(t, f, want.OutboxID, "", 0)
	before = after
	r, err = g.RunAndQueue(t.Context(), v.source, v.job)
	after = record("completed-replay", menuengine.Result{}, r, err, false, v)
	same(before, after, 2)
	receiptRejected(t, r, err, foundation.ErrStaleLease)
	expected = after
	f.mode(t, v, conversation.ModeStaff)
	before = record("takeover", menuengine.Result{}, menugate.Receipt{}, nil, false, v)
	expected.Mode, expected.Version = conversation.ModeStaff, 2
	// Only authenticated control and its operation ledger may change here.
	for _, table := range []string{"conversation_controls", "conversation_control_operations"} {
		expected.rows[table] = before.rows[table]
	}
	same(expected, before, 2)
	r, err = g.ReadQueued(t.Context(), v.source)
	after = record("historical-read", menuengine.Result{}, r, err, false, v)
	same(before, after, 2)
	if err != nil || r != want || after.Mode != conversation.ModeStaff || after.Version != 2 {
		t.Fatal("takeover lost exact history/control", r, err, after)
	}
	bad := v.job
	bad.Attempt++
	p, err = g.Run(t.Context(), v.source, bad)
	after = record("paused-run-malformed", p, menugate.Receipt{}, err, false, v)
	same(before, after, 2)
	rejected(t, p, err, menugate.ErrControlChanged)
	r, err = g.RunAndQueue(t.Context(), v.source, bad)
	after = record("paused-queue-malformed", menuengine.Result{}, r, err, false, v)
	same(before, after, 2)
	receiptRejected(t, r, err, foundation.ErrStaleLease)
	u := f.fresh(t, conversation.ModeAI)
	f.mode(t, u, conversation.ModeAI)
	before = record("fresh-ai1", menuengine.Result{}, menugate.Receipt{}, nil, false, u)
	if before.Errors != ([4]string{"<nil>", "<nil>", menugate.ErrNotFound.Error(), "<nil>"}) ||
		before.Mode != conversation.ModeAI || before.Version != 1 || before.ControlID != u.local.ConversationID().String() ||
		before.Source != (legacyScope{restaurant, u.local.ConversationID().String(), u.source.EventUUID(), u.job.Event.ID, "fixture-business", u.job.Event.Sender}) ||
		u.job.ID == v.job.ID || u.source == v.source || u.job.Generation != 1 || u.job.Attempt != 1 || before.Queued != (menugate.Receipt{}) ||
		before.Snapshot != (foundation.Snapshot{Event: u.job.Event, JobState: "leased", JobAttempt: 1, JobCount: 1}) {
		t.Fatal("fresh AI1 readiness", before)
	}
	bad = u.job
	bad.Event.Text += "changed"
	p, err = g.Run(t.Context(), u.source, bad)
	after = record("fresh-ai1-text-mismatch", p, menugate.Receipt{}, err, false, u)
	same(before, after, 2)
	rejected(t, p, err, foundation.ErrStaleLease)
	d := dispatcher(t, f, func(w http.ResponseWriter, _ *http.Request) {
		providers.Add(1)
		fmt.Fprint(w, `{"message_id":"unexpected"}`)
	})
	before, other = record("before-dispatch", menuengine.Result{}, menugate.Receipt{}, nil, false, v), legacyOther(t, f, v)
	ok, err := d.DispatchOne(t.Context(), time.Minute)
	after = record("suppressed", menuengine.Result{}, menugate.Receipt{}, err, ok, v)
	afterOther = legacyOther(t, f, v)
	want.State, want.Attempt = "suppressed", 1
	expected = before
	expected.Queued, expected.Snapshot.OutboxState, expected.rows = want, "suppressed", after.rows
	expected.Attempts, expected.AttemptOutcome = 1, "suppressed"
	same(expected, after, 2)
	if err != nil || !ok || models.Load() != 2 || providers.Load() != 0 || before.rows["jobs"] != after.rows["jobs"] || !reflect.DeepEqual(other, afterOther) {
		t.Error("suppression changed unrelated effects or invoked I/O", ok, err, models.Load(), providers.Load())
	}
	attemptState(t, f, want.OutboxID, "suppressed", 1)
	before = after
	ok, err = d.DispatchOne(t.Context(), time.Minute)
	after = record("no-work", menuengine.Result{}, menugate.Receipt{}, err, ok, v)
	same(before, after, 2)
	if err != nil || ok || models.Load() != 2 || providers.Load() != 0 {
		t.Error("repeat dispatch/model performed work", ok, err, models.Load(), providers.Load())
	}
}

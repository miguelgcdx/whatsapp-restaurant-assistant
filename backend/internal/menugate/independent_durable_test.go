package menugate_test

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
)

// S2/S3: independently derived competing prechecked proposals are not permission.
// Preserve original reply bytes, never reasoning; equal text across events is not
// an operation identity. Uses the project's owned public fixture and real client.
func TestIndependentDurableIdentity(t *testing.T) {
	f := setup(t)
	v := f.fresh(t, conversation.ModeAI)
	const reply = " \tidentical é🍲\n "
	const reasoning = "sensitive reasoning must never become reply"
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		arrived <- struct{}{}
		<-release
		return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply), ReasoningContent: ptr(reasoning)}, "stop"), nil
	}))
	type result struct {
		r menugate.Receipt
		e error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { r, e := g.RunAndQueue(t.Context(), v.source, v.job); done <- result{r, e} }()
	}
	await(t, arrived)
	await(t, arrived)
	// Both prechecks released job/event/authority locks before model transport.
	c := connect(t, f.dsn)
	tx, e := c.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{`SELECT id FROM app.jobs WHERE event_id=$1 FOR UPDATE NOWAIT`, `SELECT id FROM app.inbound_events WHERE id=$1 FOR UPDATE NOWAIT`, `SELECT event_id FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE NOWAIT`, `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=(SELECT conversation_id FROM app.cart_execution_sources WHERE event_id=$1) FOR UPDATE NOWAIT`} {
		if _, e = tx.Exec(t.Context(), sql, v.source.EventUUID()); e != nil {
			t.Fatal("model retained SQL locks", e)
		}
	}
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	close(release)
	accepted, stale := 0, 0
	var receipt menugate.Receipt
	for range 2 {
		got := await(t, done)
		if got.e == nil {
			accepted++
			receipt = got.r
		} else if errors.Is(got.e, foundation.ErrStaleLease) && got.r == (menugate.Receipt{}) {
			stale++
		} else {
			t.Fatal(got)
		}
	}
	if accepted != 1 || stale != 1 || calls.Load() != 2 || receipt.Content != reply {
		t.Fatal(accepted, stale, calls.Load(), receipt)
	}
	if snap := f.inspect(t, v); snap.JobState != "completed" || snap.JobCount != 1 || snap.OutboxCount != 1 {
		t.Fatal(snap)
	}
	before := f.hashes(t)
	r, e := g.RunAndQueue(t.Context(), v.source, v.job)
	receiptRejected(t, r, e, foundation.ErrStaleLease)
	f.unchanged(t, before)
	if calls.Load() != 2 {
		t.Fatal("completed retry ran model")
	}
	// A second real event in the same sender conversation, with identical text.
	event := foundation.Event{ID: v.job.Event.ID + "-second", Sender: v.job.Event.Sender, Text: "second question"}
	if _, e = f.jobs.Accept(t.Context(), event, time.Now()); e != nil {
		t.Fatal(e)
	}
	job, ok, e := f.jobs.ClaimJob(t.Context(), time.Now(), time.Minute)
	if e != nil || !ok || job.Event != event {
		t.Fatal(job, ok, e)
	}
	source, e := f.cart.BindSource(t.Context(), v.local, event.ID)
	if e != nil {
		t.Fatal(e)
	}
	second, e := g.RunAndQueue(t.Context(), source, job)
	if e != nil || second.Content != reply || second.OutboxID == receipt.OutboxID {
		t.Fatal(second, e)
	}
	var stored, leaked int
	if e = f.conn.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE content=$1) FROM app.outbox`, reasoning).Scan(&stored, &leaked); e != nil || stored != 2 || leaked != 0 {
		t.Fatal(stored, leaked, e)
	}
	f.mode(t, v, conversation.ModeStaff)
	before = f.hashes(t)
	historical, e := g.ReadQueued(t.Context(), v.source)
	if e != nil || historical != receipt {
		t.Fatal(historical, e)
	}
	f.unchanged(t, before)
}

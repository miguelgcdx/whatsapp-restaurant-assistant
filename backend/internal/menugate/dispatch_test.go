package menugate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/worker"
)

func dispatcher(t *testing.T, f *fixture, h http.HandlerFunc) *menugate.Dispatcher {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	sender, err := worker.NewFakeSender(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := menugate.OpenDispatcher(t.Context(), f.dsn, restaurant, sender, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}
func queueTurn(t *testing.T, f *fixture) (turn, *menugate.Gate, menugate.Receipt) {
	t.Helper()
	v := f.fresh(t, conversation.ModeAI)
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { return answer(), nil }))
	return v, g, queued(t, f, v, g, "ephemeral proposal")
}
func receiptState(t *testing.T, g *menugate.Gate, v turn, state, provider string, attempt int32) {
	t.Helper()
	r, err := g.ReadQueued(t.Context(), v.source)
	if err != nil || r.State != state || r.ProviderID != provider || r.Attempt != attempt {
		t.Fatal(r, err)
	}
}
func attemptState(t *testing.T, f *fixture, id, state string, n int) {
	t.Helper()
	var count int
	var outcome string
	if err := f.conn.QueryRow(t.Context(), `SELECT count(*),coalesce(min(outcome),'') FROM app.send_attempts WHERE outbox_id=$1`, id).Scan(&count, &outcome); err != nil || count != n || outcome != state {
		t.Fatal(count, outcome, err)
	}
}

// S4: stale suppression is terminal and possible only before provider I/O.
func TestDurableDispatchSuppression(t *testing.T) {
	f := setup(t)
	var calls atomic.Int32
	d := dispatcher(t, f, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"message_id":"unexpected"}`)
	})
	for _, mode := range []string{"takeover", "return", "same-mode"} {
		t.Run(mode, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			if mode == "same-mode" {
				f.mode(t, v, conversation.ModeAI)
			} else {
				f.mode(t, v, conversation.ModeStaff)
				if mode == "return" {
					f.mode(t, v, conversation.ModeAI)
				}
			}
			ok, err := d.DispatchOne(t.Context(), time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			receiptState(t, g, v, "suppressed", "", 1)
			attemptState(t, f, r.OutboxID, "suppressed", 1)
			f.mode(t, v, conversation.ModeAI)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			if err != nil || ok {
				t.Fatal(ok, err)
			}
			receiptState(t, g, v, "suppressed", "", 1)
		})
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
}

func TestDurableDispatchPreflight(t *testing.T) {
	f := setup(t)
	var calls atomic.Int32
	d := dispatcher(t, f, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"message_id":"unexpected"}`)
	})
	for _, name := range []string{"takeover", "provider-event", "reply", "generation", "attempt", "infinite-lease", "pending-revival"} {
		t.Run(name, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			c := connect(t, f.dsn)
			tx, err := c.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if _, err = tx.Exec(t.Context(), `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=$1 FOR UPDATE`, v.local.ConversationID().String()); err != nil {
				t.Fatal(err)
			}
			type result struct {
				ok  bool
				err error
			}
			done := make(chan result, 1)
			go func() { ok, err := d.DispatchOne(t.Context(), time.Minute); done <- result{ok, err} }()
			waitLock(t, f.conn, "SELECT mode,version FROM app.conversation_controls")
			attemptState(t, f, r.OutboxID, "sending", 1)
			switch name {
			case "takeover":
				_, err = tx.Exec(t.Context(), `UPDATE app.conversation_controls SET mode='staff',version=version+1 WHERE conversation_id=$1`, v.local.ConversationID().String())
			case "provider-event":
				_, err = tx.Exec(t.Context(), `UPDATE app.inbound_events SET provider_event_id=provider_event_id||'-drift' WHERE id=$1`, v.source.EventUUID())
			case "reply":
				_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET content='drift' WHERE id=$1`, r.OutboxID)
			case "generation":
				_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET generation=generation+1 WHERE id=$1`, r.OutboxID)
			case "attempt":
				_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET attempt=attempt+1 WHERE id=$1`, r.OutboxID)
			case "infinite-lease":
				_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET lease_until='infinity' WHERE id=$1`, r.OutboxID)
			case "pending-revival":
				_, err = tx.Exec(t.Context(), `UPDATE app.outbox SET state='pending',lease_until=NULL WHERE id=$1`, r.OutboxID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := await(t, done)
			if !got.ok || calls.Load() != 0 {
				t.Fatal(got, calls.Load())
			}
			if name == "takeover" {
				if got.err != nil {
					t.Fatal(got.err)
				}
				receiptState(t, g, v, "suppressed", "", 1)
				attemptState(t, f, r.OutboxID, "suppressed", 1)
			} else {
				want := foundation.ErrStaleLease
				if name == "infinite-lease" || name == "provider-event" || name == "pending-revival" {
					want = menugate.ErrStoredData
				}
				if !errors.Is(got.err, want) {
					t.Fatal(got.err, want)
				}
				attemptState(t, f, r.OutboxID, "sending", 1)
				if name == "pending-revival" {
					before := f.hashes(t)
					ok, err := d.DispatchOne(t.Context(), time.Minute)
					if ok || !errors.Is(err, menugate.ErrStoredData) || calls.Load() != 0 {
						t.Fatal(ok, err, calls.Load())
					}
					f.unchanged(t, before)
				}
			}
		})
	}
}

func TestDurableDispatchOutcomes(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name                  string
		status                int
		body, state, provider string
	}{
		{"accepted", 200, `{"message_id":" exact provider id "}`, "accepted", " exact provider id "},
		{"400", 400, `rejected`, "failed", ""}, {"422", 422, `rejected`, "failed", ""},
		{"500", 500, `maybe`, "unknown", ""}, {"malformed", 200, `{`, "unknown", ""},
		{"blank-id", 200, `{"message_id":" "}`, "unknown", ""},
		{"timeout", 200, ``, "unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			var calls atomic.Int32
			d := dispatcher(t, f, func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				var body struct {
					EventID  string `json:"event_id"`
					EffectID string `json:"effect_id"`
					Text     string `json:"text"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" || body.EventID != v.job.Event.ID || body.EffectID != r.OutboxID || body.Text != r.Content {
					t.Error("wrong provider wire", body)
				}
				if tc.name == "timeout" {
					<-req.Context().Done()
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			ok, err := d.DispatchOne(t.Context(), time.Minute)
			if err != nil || !ok || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			receiptState(t, g, v, tc.state, tc.provider, 1)
			attemptState(t, f, r.OutboxID, tc.state, 1)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			if err != nil || ok || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
		})
	}
}

func TestDurableDispatchInFlight(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name                  string
		status                int
		body, state, provider string
	}{{"accepted", 200, `{"message_id":"in-flight"}`, "accepted", "in-flight"}, {"failed", 422, `no`, "failed", ""}, {"unknown", 500, `maybe`, "unknown", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			entered := make(chan struct{})
			release := make(chan struct{})
			d := dispatcher(t, f, func(w http.ResponseWriter, req *http.Request) {
				close(entered)
				<-release
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			type result struct {
				ok  bool
				err error
			}
			done := make(chan result, 1)
			go func() { ok, err := d.DispatchOne(t.Context(), time.Minute); done <- result{ok, err} }()
			await(t, entered)
			// Corroborate that the sender holds no job/event/outbox/attempt row locks.
			probe := connect(t, f.dsn)
			tx, err := probe.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, sql := range []string{`SELECT id FROM app.jobs WHERE event_id=$1 FOR UPDATE NOWAIT`, `SELECT id FROM app.inbound_events WHERE id=$1 FOR UPDATE NOWAIT`, `SELECT id FROM app.outbox WHERE event_id=$1 FOR UPDATE NOWAIT`, `SELECT a.outbox_id FROM app.send_attempts a JOIN app.outbox o ON o.id=a.outbox_id WHERE o.event_id=$1 FOR UPDATE OF a NOWAIT`} {
				if _, err = tx.Exec(t.Context(), sql, v.source.EventUUID()); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Public takeover must finish while HTTP is blocked: no authority locks held.
			f.mode(t, v, conversation.ModeStaff)
			close(release)
			got := await(t, done)
			if !got.ok || got.err != nil {
				t.Fatal(got)
			}
			receiptState(t, g, v, tc.state, tc.provider, 1)
			attemptState(t, f, r.OutboxID, tc.state, 1)
		})
	}
}

func TestDurableDispatchConcurrency(t *testing.T) {
	f := setup(t)
	v, g, r := queueTurn(t, f)
	var calls atomic.Int32
	d := dispatcher(t, f, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"message_id":"one"}`) })
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	results := make(chan bool, 2)
	for range 2 {
		wg.Go(func() { ok, err := d.DispatchOne(t.Context(), time.Minute); errs <- err; results <- ok })
	}
	wg.Wait()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for range 2 {
		if <-results {
			n++
		}
	}
	if n != 1 || calls.Load() != 1 {
		t.Fatal(n, calls.Load())
	}
	receiptState(t, g, v, "accepted", "one", 1)
	attemptState(t, f, r.OutboxID, "accepted", 1)
}

func TestDurableDispatchLegacyIsolation(t *testing.T) {
	f := setup(t)
	v, g, r := queueTurn(t, f)
	ctx := t.Context()
	now := time.Now()
	delivery, ok, err := f.jobs.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || ok || delivery != (foundation.Delivery{}) {
		t.Fatal(delivery, ok, err)
	}
	if due, ok, err := f.jobs.NextDue(ctx); err != nil || ok || !due.IsZero() {
		t.Fatal(due, ok, err)
	}
	forged := foundation.Delivery{ID: r.OutboxID, EventID: v.job.Event.ID, Text: r.Content, Generation: 1, Attempt: 1}
	before := f.hashes(t)
	err = f.jobs.FinishDelivery(ctx, forged, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "forged"}, now)
	if !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatal(err)
	}
	f.unchanged(t, before)
	var calls atomic.Int32
	d := dispatcher(t, f, func(w http.ResponseWriter, req *http.Request) { calls.Add(1); fmt.Fprint(w, `{"message_id":"native"}`) })
	// Hold preflight, leaving the durable attempt observable to legacy APIs.
	c := connect(t, f.dsn)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=$1 FOR UPDATE`, v.local.ConversationID().String()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() { ok, err := d.DispatchOne(ctx, time.Minute); done <- result{ok, err} }()
	waitLock(t, f.conn, "SELECT mode,version FROM app.conversation_controls")
	if due, ok, err := f.jobs.NextDue(ctx); err != nil || ok || !due.IsZero() {
		t.Fatal(due, ok, err)
	}
	delivery, ok, err = f.jobs.ClaimDelivery(ctx, now.Add(time.Hour), time.Minute)
	if err != nil || ok {
		t.Fatal(delivery, ok, err)
	}
	if s := f.inspect(t, v); s.OutboxState != "sending" {
		t.Fatal(s)
	}
	sendingBefore := f.hashes(t)
	err = f.jobs.FinishDelivery(ctx, forged, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "forged"}, time.Now())
	if !errors.Is(err, foundation.ErrStaleLease) {
		t.Fatal(err)
	}
	f.unchanged(t, sendingBefore)
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got := await(t, done)
	if !got.ok || got.err != nil {
		t.Fatal(got)
	}
	receiptState(t, g, v, "accepted", "native", 1)
}

// S5: earlier pending/sending/unknown work fences later effects, even when
// the two effects belong to different dispatchers. Equal reply text is not dedup.
func TestDurableDispatchOrdering(t *testing.T) {
	f := setup(t)
	for _, firstAI := range []bool{false, true} {
		for _, state := range []string{"pending", "sending", "unknown"} {
			name := "legacy-first-" + state
			if firstAI {
				name = "AI-first-" + state
			}
			t.Run(name, func(t *testing.T) {
				v := f.fresh(t, conversation.ModeAI)
				g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { return answer(), nil }))
				if firstAI {
					queued(t, f, v, g, "ephemeral proposal")
				} else {
					if err := f.jobs.CompleteJob(t.Context(), v.job, "ephemeral proposal", time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				entered := make(chan struct{})
				release := make(chan struct{})
				d := dispatcher(t, f, func(w http.ResponseWriter, r *http.Request) {
					if state == "sending" && firstAI {
						close(entered)
						<-release
					}
					if state == "unknown" {
						w.WriteHeader(500)
					} else {
						fmt.Fprint(w, `{"message_id":"ordered"}`)
					}
				})
				type result struct {
					ok  bool
					err error
				}
				done := make(chan result, 1)
				var legacyClaim foundation.Delivery
				if state != "pending" {
					if firstAI {
						if state == "sending" {
							go func() { ok, err := d.DispatchOne(t.Context(), time.Minute); done <- result{ok, err} }()
							await(t, entered)
						} else {
							ok, err := d.DispatchOne(t.Context(), time.Minute)
							if err != nil || !ok {
								t.Fatal(ok, err)
							}
						}
					} else {
						delivery, ok, err := f.jobs.ClaimDelivery(t.Context(), time.Now(), time.Minute)
						if err != nil || !ok {
							t.Fatal(delivery, ok, err)
						}
						legacyClaim = delivery
						if state == "unknown" {
							if err = f.jobs.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendUnknown}, time.Now()); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				event := foundation.Event{ID: v.job.Event.ID + "-later", Sender: v.job.Event.Sender, Text: "later question"}
				if _, err := f.jobs.Accept(t.Context(), event, time.Now()); err != nil {
					t.Fatal(err)
				}
				job, ok, err := f.jobs.ClaimJob(t.Context(), time.Now(), time.Minute)
				if err != nil || !ok || job.Event.ID != event.ID {
					t.Fatal(job, ok, err)
				}
				source, err := f.cart.BindSource(t.Context(), v.local, event.ID)
				if err != nil {
					t.Fatal(err)
				}
				later := turn{v.local, source, job}
				if firstAI {
					if err = f.jobs.CompleteJob(t.Context(), job, "ephemeral proposal", time.Now()); err != nil {
						t.Fatal(err)
					}
					delivery, ok, err := f.jobs.ClaimDelivery(t.Context(), time.Now(), time.Minute)
					if err != nil || ok || delivery != (foundation.Delivery{}) {
						t.Fatal(delivery, ok, err)
					}
				} else {
					queued(t, f, later, g, "ephemeral proposal")
					ok, err := d.DispatchOne(t.Context(), time.Minute)
					if err != nil || ok {
						t.Fatal(ok, err)
					}
				}
				if s := f.inspect(t, later); s.OutboxState != "pending" || s.OutboxCount != 1 {
					t.Fatal(s)
				}
				if firstAI && state == "sending" {
					close(release)
					got := await(t, done)
					if got.err != nil || !got.ok {
						t.Fatal(got)
					}
				}
				if state != "unknown" {
					if !firstAI && state == "sending" {
						if err = f.jobs.FinishDelivery(t.Context(), legacyClaim, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "ordered"}, time.Now()); err != nil {
							t.Fatal(err)
						}
					}
					if firstAI && state == "pending" {
						ok, err := d.DispatchOne(t.Context(), time.Minute)
						if err != nil || !ok {
							t.Fatal(ok, err)
						}
					}
					if !firstAI && state == "pending" {
						delivery, ok, err := f.jobs.ClaimDelivery(t.Context(), time.Now(), time.Minute)
						if err != nil || !ok {
							t.Fatal(delivery, ok, err)
						}
						if err = f.jobs.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "ordered"}, time.Now()); err != nil {
							t.Fatal(err)
						}
					}
					if firstAI {
						delivery, ok, err := f.jobs.ClaimDelivery(t.Context(), time.Now(), time.Minute)
						if err != nil || !ok {
							t.Fatal(delivery, ok, err)
						}
						if err = f.jobs.FinishDelivery(t.Context(), delivery, foundation.SendResult{State: foundation.SendAccepted, ProviderID: "ordered"}, time.Now()); err != nil {
							t.Fatal(err)
						}
					} else {
						ok, err := d.DispatchOne(t.Context(), time.Minute)
						if err != nil || !ok {
							t.Fatal(ok, err)
						}
					}
				}
			})
		}
	}
}

// Only malformed trusted Sender results use this adapter. All provider-wire
// tests above and below continue to use the actual NewFakeSender implementation.
type resultSender func(context.Context, foundation.Delivery) foundation.SendResult

func (s resultSender) Send(ctx context.Context, d foundation.Delivery) foundation.SendResult {
	return s(ctx, d)
}

func waitDeliveryExpiry(t *testing.T, f *fixture, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var expired bool
		if err := f.conn.QueryRow(ctx, `SELECT lease_until IS NOT NULL AND isfinite(lease_until) AND clock_timestamp()>=lease_until FROM app.outbox WHERE id=$1`, id).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
	}
}
func assertDeliveryGeneration(t *testing.T, f *fixture, id string) {
	t.Helper()
	var generation, attemptGeneration int64
	var attempt int32
	if err := f.conn.QueryRow(t.Context(), `SELECT o.generation,o.attempt,a.generation FROM app.outbox o JOIN app.send_attempts a ON a.outbox_id=o.id AND a.attempt=o.attempt WHERE o.id=$1`, id).Scan(&generation, &attempt, &attemptGeneration); err != nil || generation != 1 || attempt != 1 || attemptGeneration != 1 {
		t.Fatal(generation, attempt, attemptGeneration, err)
	}
}

// S4/S5/S6: invalid injected results never create acceptance or suppression.
func TestDurableDispatchInvalidResults(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name   string
		result foundation.SendResult
	}{
		{"unknown-enum", foundation.SendResult{State: foundation.SendState("invented")}},
		{"suppressed-enum", foundation.SendResult{State: foundation.SendState("suppressed")}},
		{"accepted-empty-id", foundation.SendResult{State: foundation.SendAccepted}},
		{"accepted-invalid-utf8", foundation.SendResult{State: foundation.SendAccepted, ProviderID: "\xff"}},
		{"accepted-129-runes", foundation.SendResult{State: foundation.SendAccepted, ProviderID: strings.Repeat("é", 129)}},
		{"failed-with-id", foundation.SendResult{State: foundation.SendFailed, ProviderID: "not-accepted"}},
		{"unknown-with-id", foundation.SendResult{State: foundation.SendUnknown, ProviderID: "not-accepted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			var calls atomic.Int32
			sender := resultSender(func(ctx context.Context, d foundation.Delivery) foundation.SendResult {
				calls.Add(1)
				if d.ID != r.OutboxID || d.EventID != v.job.Event.ID || d.Text != r.Content || d.Generation != 1 || d.Attempt != 1 {
					t.Error("unbound delivery", d)
				}
				return tc.result
			})
			d, err := menugate.OpenDispatcher(t.Context(), f.dsn, restaurant, sender, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			ok, err := d.DispatchOne(t.Context(), 600*time.Millisecond)
			if !ok || !errors.Is(err, menugate.ErrStoredData) || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			receiptState(t, g, v, "sending", "", 1)
			attemptState(t, f, r.OutboxID, "sending", 1)
			assertDeliveryGeneration(t, f, r.OutboxID)
			before := f.hashes(t)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			if ok || err != nil || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			f.unchanged(t, before)
			waitDeliveryExpiry(t, f, r.OutboxID)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			if !ok || err != nil || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			receiptState(t, g, v, "unknown", "", 1)
			attemptState(t, f, r.OutboxID, "unknown", 1)
			assertDeliveryGeneration(t, f, r.OutboxID)
			snapshot := f.inspect(t, v)
			if snapshot.JobState != "completed" || snapshot.JobCount != 1 || snapshot.OutboxCount != 1 || snapshot.JobAttempt != v.job.Attempt {
				t.Fatal(snapshot)
			}
			before = f.hashes(t)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			if ok || err != nil || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			f.unchanged(t, before)
		})
	}
}

// S1/S4/S5/S6: real elapsed database leases during actual blocked provider HTTP.
// Conditional completion cannot overwrite expiry/unknown, even after takeover.
func TestDurableDispatchExpiredInFlight(t *testing.T) {
	f := setup(t)
	for _, takeover := range []bool{false, true} {
		name := "late-accepted-then-native-expiry"
		if takeover {
			name = "takeover-native-expiry-then-late-accepted"
		}
		t.Run(name, func(t *testing.T) {
			v, g, r := queueTurn(t, f)
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				var body struct {
					EventID  string `json:"event_id"`
					EffectID string `json:"effect_id"`
					Text     string `json:"text"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" || body.EventID != v.job.Event.ID || body.EffectID != r.OutboxID || body.Text != r.Content {
					t.Error("wrong late provider request", body)
				}
				close(entered)
				select {
				case <-release:
					fmt.Fprint(w, `{"message_id":"late-provider-id"}`)
				case <-req.Context().Done():
				}
			}))
			defer server.Close()
			// Unblock before server.Close even on a failed assertion.
			defer unblock()
			sender, err := worker.NewFakeSender(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			d, err := menugate.OpenDispatcher(t.Context(), f.dsn, restaurant, sender, 4*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			type result struct {
				handled bool
				err     error
			}
			done := make(chan result, 1)
			go func() { ok, err := d.DispatchOne(t.Context(), 800*time.Millisecond); done <- result{ok, err} }()
			await(t, entered)
			receiptState(t, g, v, "sending", "", 1)
			attemptState(t, f, r.OutboxID, "sending", 1)
			assertDeliveryGeneration(t, f, r.OutboxID)
			waitDeliveryExpiry(t, f, r.OutboxID)
			if takeover {
				// One later effect of each kind, not another ordering matrix. Both must
				// stay blocked after expiry is recorded while the old HTTP is still live.
				later := func(suffix string) turn {
					event := foundation.Event{ID: v.job.Event.ID + suffix, Sender: v.job.Event.Sender, Text: "later question"}
					if _, err := f.jobs.Accept(t.Context(), event, time.Now()); err != nil {
						t.Fatal(err)
					}
					job, ok, err := f.jobs.ClaimJob(t.Context(), time.Now(), time.Minute)
					if err != nil || !ok || job.Event.ID != event.ID {
						t.Fatal(job, ok, err)
					}
					source, err := f.cart.BindSource(t.Context(), v.local, event.ID)
					if err != nil {
						t.Fatal(err)
					}
					return turn{v.local, source, job}
				}
				ai := later("-AI")
				queued(t, f, ai, g, "ephemeral proposal")
				ack := later("-ACK")
				if err := f.jobs.CompleteJob(t.Context(), ack.job, "legacy acknowledgement", time.Now()); err != nil {
					t.Fatal(err)
				}
				f.mode(t, v, conversation.ModeStaff)
				ok, err := d.DispatchOne(t.Context(), time.Minute)
				if !ok || err != nil || calls.Load() != 1 {
					t.Fatal(ok, err, calls.Load())
				}
				receiptState(t, g, v, "unknown", "", 1)
				attemptState(t, f, r.OutboxID, "unknown", 1)
				before := f.hashes(t)
				ok, err = d.DispatchOne(t.Context(), time.Minute)
				if ok || err != nil || calls.Load() != 1 {
					t.Fatal(ok, err, calls.Load())
				}
				delivery, ok, err := f.jobs.ClaimDelivery(t.Context(), time.Now(), time.Minute)
				if ok || err != nil || delivery != (foundation.Delivery{}) {
					t.Fatal(delivery, ok, err)
				}
				f.unchanged(t, before)
				if f.inspect(t, ai).OutboxState != "pending" || f.inspect(t, ack).OutboxState != "pending" {
					t.Fatal("unknown failed to fence later effects")
				}
			}
			beforeLateCompletion := f.hashes(t)
			unblock()
			got := await(t, done)
			if !got.handled || !errors.Is(got.err, foundation.ErrStaleLease) || calls.Load() != 1 {
				t.Fatal(got, calls.Load())
			}
			f.unchanged(t, beforeLateCompletion)
			if !takeover {
				receiptState(t, g, v, "sending", "", 1)
				attemptState(t, f, r.OutboxID, "sending", 1)
				ok, err := d.DispatchOne(t.Context(), time.Minute)
				if !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			receiptState(t, g, v, "unknown", "", 1)
			attemptState(t, f, r.OutboxID, "unknown", 1)
			assertDeliveryGeneration(t, f, r.OutboxID)
			snapshot := f.inspect(t, v)
			if snapshot.JobState != "completed" || snapshot.JobCount != 1 || snapshot.OutboxCount != 1 || snapshot.JobAttempt != v.job.Attempt || snapshot.ProviderID != "" {
				t.Fatal(snapshot)
			}
			before := f.hashes(t)
			ok, err := d.DispatchOne(t.Context(), time.Minute)
			if ok || err != nil || calls.Load() != 1 {
				t.Fatal(ok, err, calls.Load())
			}
			f.unchanged(t, before)
		})
	}
}

func TestDurableDispatchLease(t *testing.T) {
	f := setup(t)
	var calls atomic.Int32
	d := dispatcher(t, f, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"message_id":"unexpected"}`)
	})
	v, g, r := queueTurn(t, f)
	c := connect(t, f.dsn)
	tx, err := c.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=$1 FOR UPDATE`, v.local.ConversationID().String()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() { ok, err := d.DispatchOne(t.Context(), time.Millisecond); done <- result{ok, err} }()
	waitLock(t, f.conn, "SELECT mode,version FROM app.conversation_controls")
	// Waiting on control outlasted the finite lease; use DB clock, not sleeps.
	for {
		var expired bool
		if err = f.conn.QueryRow(t.Context(), `SELECT clock_timestamp()>=lease_until FROM app.outbox WHERE id=$1`, r.OutboxID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := await(t, done)
	if !errors.Is(got.err, foundation.ErrStaleLease) || calls.Load() != 0 {
		t.Fatal(got, calls.Load())
	}
	ok, err := d.DispatchOne(context.Background(), time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	receiptState(t, g, v, "unknown", "", 1)
	attemptState(t, f, r.OutboxID, "unknown", 1)
	ok, err = d.DispatchOne(t.Context(), time.Minute)
	if err != nil || ok || calls.Load() != 0 {
		t.Fatal(ok, err, calls.Load())
	}
}

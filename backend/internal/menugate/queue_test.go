package menugate_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/worker"
)

func queued(t *testing.T, f *fixture, v turn, g *menugate.Gate, reply string) menugate.Receipt {
	t.Helper()
	got, err := g.RunAndQueue(t.Context(), v.source, v.job)
	if err != nil || got.OutboxID == "" || got.Content != reply || got.ControlVersion != 0 || got.State != "pending" || got.Attempt != 0 || got.ProviderID != "" {
		t.Fatalf("receipt=%+v error=%v", got, err)
	}
	s := f.inspect(t, v)
	if s.JobState != "completed" || s.OutboxState != "pending" || s.JobCount != 1 || s.OutboxCount != 1 {
		t.Fatal(s)
	}
	read, err := g.ReadQueued(t.Context(), v.source)
	if err != nil || read != got {
		t.Fatal(read, err)
	}
	return got
}
func receiptRejected(t *testing.T, r menugate.Receipt, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || r != (menugate.Receipt{}) {
		t.Fatalf("receipt=%+v error=%v want zero/%v", r, err, want)
	}
}

// S2/S3: queue only the final original Reply, never tools or reasoning.
func TestDurableQueue(t *testing.T) {
	f := setup(t)
	for _, tool := range []bool{false, true} {
		name := "direct"
		if tool {
			name = "closed-tool-menu"
		}
		t.Run(name, func(t *testing.T) {
			v := f.fresh(t, conversation.ModeAI)
			var calls atomic.Int32
			const reply = "  Original reply\né\n "
			g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				if tool && n == 1 {
					return menuCall(), nil
				}
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
			}))
			got := queued(t, f, v, g, reply)
			want := int32(1)
			if tool {
				want = 2
			}
			if calls.Load() != want {
				t.Fatal(calls.Load())
			}
			before := f.hashes(t)
			r, err := g.RunAndQueue(t.Context(), v.source, v.job)
			receiptRejected(t, r, err, foundation.ErrStaleLease)
			if calls.Load() != want {
				t.Fatal("retry called model")
			}
			f.unchanged(t, before)
			f.mode(t, v, conversation.ModeStaff)
			historical, err := g.ReadQueued(t.Context(), v.source)
			if err != nil || historical != got {
				t.Fatal(historical, err)
			}
			afterTakeover := f.hashes(t)
			r, err = g.RunAndQueue(t.Context(), v.source, v.job)
			receiptRejected(t, r, err, foundation.ErrStaleLease)
			if calls.Load() != want {
				t.Fatal("completed takeover retry called model")
			}
			f.unchanged(t, afterTakeover)
		})
	}
}
func ptr(s string) *string { return &s }

func TestDurableQueueRejects(t *testing.T) {
	f := setup(t)
	for _, name := range []string{"staff", "missing-control", "generation", "attempt", "event", "sender", "text", "cancel", "elapsed", "foreign", "post-takeover", "post-return", "post-same-mode", "post-claim", "post-source"} {
		t.Run(name, func(t *testing.T) {
			mode := conversation.ModeAI
			if name == "staff" {
				mode = conversation.ModeStaff
			}
			if name == "missing-control" {
				mode = ""
			}
			v := f.fresh(t, mode)
			job := v.job
			ctx := t.Context()
			want := foundation.ErrStaleLease
			switch name {
			case "staff":
				want = menugate.ErrControlChanged
			case "missing-control":
				want = menugate.ErrNotFound
			case "generation":
				job.Generation++
			case "attempt":
				job.Attempt++
			case "event":
				job.Event.ID += "changed"
			case "sender":
				job.Event.Sender += "changed"
			case "text":
				job.Event.Text += "changed"
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
				want = context.Canceled
			case "elapsed":
				ctx = lateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}
				want = context.DeadlineExceeded
			case "foreign":
				want = menugate.ErrNotFound
			default:
				want = menugate.ErrControlChanged
			}
			var calls atomic.Int32
			var after map[string]string
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				switch name {
				case "post-takeover":
					f.mode(t, v, conversation.ModeStaff)
				case "post-return":
					f.mode(t, v, conversation.ModeStaff)
					f.mode(t, v, conversation.ModeAI)
				case "post-same-mode":
					f.mode(t, v, conversation.ModeAI)
				case "post-claim":
					f.exec(t, `UPDATE app.jobs SET generation=generation+1 WHERE id=$1`, job.ID)
				case "post-source":
					f.exec(t, `UPDATE app.cart_conversations SET business='changed' WHERE id=$1`, v.local.ConversationID().String())
				}
				after = f.hashes(t)
				return answer(), nil
			}))
			if name == "foreign" {
				var err error
				g, err = menugate.Open(t.Context(), f.dsn, foreignRestaurant, client(t, func(*http.Request) (*http.Response, error) { t.Error("foreign HTTP"); return answer(), nil }), 321)
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
			}
			if name == "post-claim" {
				want = foundation.ErrStaleLease
			}
			if name == "post-source" {
				want = menugate.ErrNotFound
			}
			before := f.hashes(t)
			r, err := g.RunAndQueue(ctx, v.source, job)
			receiptRejected(t, r, err, want)
			expected := int32(0)
			if strings.HasPrefix(name, "post-") {
				expected = 1
				before = after
			}
			if calls.Load() != expected {
				t.Fatal(calls.Load())
			}
			f.unchanged(t, before)
			s := f.inspect(t, v)
			if s.OutboxCount != 0 || s.JobState != "leased" {
				t.Fatal(s)
			}
		})
	}
}

func TestDurableQueueReplyBoundary(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name, reply string
		valid       bool
	}{{"4000", " \n" + strings.Repeat("é", 3997) + " ", true}, {"4001", strings.Repeat("é", 4001), false}, {"blank", " \n ", false}} {
		t.Run(tc.name, func(t *testing.T) {
			v := f.fresh(t, conversation.ModeAI)
			calls := 0
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(tc.reply)}, "stop"), nil
			}))
			before := f.hashes(t)
			if tc.valid {
				queued(t, f, v, g, tc.reply)
			} else {
				r, err := g.RunAndQueue(t.Context(), v.source, v.job)
				receiptRejected(t, r, err, menugate.ErrReply)
				f.unchanged(t, before)
			}
			if calls != 1 {
				t.Fatal(calls)
			}
		})
	}
}

// S2/S3: lease expires while the event-unique INSERT is blocked, after the
// ordinary source/control/job locks. The final canonical guard must still fail.
func TestDurableQueueClaims(t *testing.T) {
	f := setup(t)
	v := f.fresh(t, conversation.ModeAI)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return answer(), nil
	}))
	type result struct {
		r   menugate.Receipt
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := g.RunAndQueue(t.Context(), v.source, v.job); done <- result{r, err} }()
	await(t, entered)
	f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, v.job.ID)
	c := connect(t, f.dsn)
	tx, err := c.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO app.outbox(id,event_id,content,state,due_at) VALUES('a0000000-0000-4000-8000-000000000099',$1,'blocker','pending',clock_timestamp())`, v.source.EventUUID()); err != nil {
		t.Fatal(err)
	}
	before := f.hashes(t)
	close(release)
	waitLock(t, f.conn, "-- name: InsertOutbox")
	expired(t, f, v)
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := await(t, done)
	receiptRejected(t, got.r, got.err, foundation.ErrStaleLease)
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	f.unchanged(t, before)
	if s := f.inspect(t, v); s.JobState != "leased" || s.OutboxCount != 0 {
		t.Fatal(s)
	}
}

// S3/S6 correction: observe the real concrete-client/engine/queue boundary.
// The initial requested ErrReply expectation was disproved by the concrete
// client's existing cc.ErrProtocol rejection. Option A preserves that identity;
// these current GREEN assertions never repair missing implementation-time RED.
func TestDurableQueueEmptyAndNUL(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct{ name, reply string }{{"empty", ""}, {"NUL", "before\x00after"}} {
		t.Run(tc.name, func(t *testing.T) {
			v := f.fresh(t, conversation.ModeAI)
			var calls atomic.Int32
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(tc.reply)}, "stop"), nil
			}))
			before := f.hashes(t)
			snapshot := f.inspect(t, v)
			r, err := g.RunAndQueue(t.Context(), v.source, v.job)
			if r != (menugate.Receipt{}) || calls.Load() != 1 {
				t.Fatal(r, err, calls.Load())
			}
			if got := f.inspect(t, v); got != snapshot || got.JobState != "leased" || got.OutboxCount != 0 {
				t.Fatal(got, snapshot)
			}
			f.unchanged(t, before)
			receiptRejected(t, r, err, cc.ErrProtocol)
		})
	}
}

// S2/S3/S6: bounded facade rejection classes, not combinatorial validation.
func TestDurableFacadeErrors(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	v := f.fresh(t, conversation.ModeAI)
	h, hg, historical := queueTurn(t, f)
	var modelCalls, providerCalls atomic.Int32
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { modelCalls.Add(1); return answer(), nil }))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		t.Error("unexpected provider call")
	}))
	defer server.Close()
	sender, err := worker.NewFakeSender(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := menugate.OpenDispatcher(ctx, f.dsn, restaurant, sender, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	elapsed := lateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}
	var nilGate *menugate.Gate
	var nilDispatcher *menugate.Dispatcher
	// A real source bound to another configured restaurant, not caller-authored IDs.
	foreignCatalog, err := catalog.OpenStore(ctx, f.dsn, foreignRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignCatalog.Close()
	if err = foreignCatalog.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: false}, nil); err != nil {
		t.Fatal(err)
	}
	foreign, err := cart.OpenStore(ctx, f.dsn, foreignRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	local, err := foreign.ProvisionLocal(ctx, "foreign-business", "foreign-sender")
	if err != nil {
		t.Fatal(err)
	}
	event := foundation.Event{ID: "foreign-facade", Sender: "foreign-sender", Text: "foreign question"}
	if _, err = f.jobs.Accept(ctx, event, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	source, err := foreign.BindSource(ctx, local, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := f.hashes(t)
	leasedSnapshot := f.inspect(t, v)
	historicalSnapshot := f.inspect(t, h)
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		f.unchanged(t, before)
		if f.inspect(t, v) != leasedSnapshot || f.inspect(t, h) != historicalSnapshot || modelCalls.Load() != 0 || providerCalls.Load() != 0 {
			t.Fatal("facade rejection changed public state or invoked I/O")
		}
	}
	receiptCases := []struct {
		name string
		call func() (menugate.Receipt, error)
		want error
	}{
		{"nil-gate-queue", func() (menugate.Receipt, error) { return nilGate.RunAndQueue(ctx, v.source, v.job) }, menugate.ErrInput},
		{"nil-gate-read", func() (menugate.Receipt, error) { return nilGate.ReadQueued(ctx, h.source) }, menugate.ErrInput},
		{"nil-context-queue", func() (menugate.Receipt, error) { return g.RunAndQueue(nil, v.source, v.job) }, menugate.ErrInput},
		{"nil-context-read", func() (menugate.Receipt, error) { return g.ReadQueued(nil, h.source) }, menugate.ErrInput},
		{"invalid-source-queue", func() (menugate.Receipt, error) { return g.RunAndQueue(ctx, cart.Source{}, v.job) }, menugate.ErrInput},
		{"invalid-source-read", func() (menugate.Receipt, error) { return g.ReadQueued(ctx, cart.Source{}) }, menugate.ErrInput},
		{"foreign-source-queue", func() (menugate.Receipt, error) { return g.RunAndQueue(ctx, source, v.job) }, menugate.ErrNotFound},
		{"foreign-source-read", func() (menugate.Receipt, error) { return g.ReadQueued(ctx, source) }, menugate.ErrNotFound},
		{"wrong-event-binding", func() (menugate.Receipt, error) { return g.RunAndQueue(ctx, h.source, v.job) }, foundation.ErrStaleLease},
		{"canceled-queue", func() (menugate.Receipt, error) { return g.RunAndQueue(canceled, v.source, v.job) }, context.Canceled},
		{"canceled-historical-read", func() (menugate.Receipt, error) { return g.ReadQueued(canceled, h.source) }, context.Canceled},
		{"elapsed-queue", func() (menugate.Receipt, error) { return g.RunAndQueue(elapsed, v.source, v.job) }, context.DeadlineExceeded},
		{"elapsed-historical-read", func() (menugate.Receipt, error) { return g.ReadQueued(elapsed, h.source) }, context.DeadlineExceeded},
	}
	for _, tc := range receiptCases {
		t.Run(tc.name, func(t *testing.T) { r, err := tc.call(); receiptRejected(t, r, err, tc.want); assertUnchanged(t) })
	}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		dsn, id string
		sender  worker.Sender
		timeout time.Duration
		want    error
	}{
		{"open-nil-context", nil, f.dsn, restaurant, sender, time.Second, menugate.ErrInput},
		{"open-invalid-dsn", ctx, "postgres://fixture@127.0.0.1/invalid", restaurant, sender, time.Second, menugate.ErrInput},
		{"open-invalid-restaurant", ctx, f.dsn, strings.ToUpper(restaurant), sender, time.Second, menugate.ErrInput},
		{"open-nil-sender", ctx, f.dsn, restaurant, nil, time.Second, menugate.ErrInput},
		{"open-zero-timeout", ctx, f.dsn, restaurant, sender, 0, menugate.ErrInput},
		{"open-canceled", canceled, f.dsn, restaurant, sender, time.Second, context.Canceled},
		{"open-elapsed", elapsed, f.dsn, restaurant, sender, time.Second, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := menugate.OpenDispatcher(tc.ctx, tc.dsn, tc.id, tc.sender, tc.timeout)
			if got != nil || !errors.Is(err, tc.want) {
				if got != nil {
					got.Close()
				}
				t.Fatal(got, err, tc.want)
			}
			assertUnchanged(t)
		})
	}
	for _, tc := range []struct {
		name  string
		d     *menugate.Dispatcher
		ctx   context.Context
		lease time.Duration
		want  error
	}{
		{"dispatch-nil-receiver", nilDispatcher, ctx, time.Second, menugate.ErrInput},
		{"dispatch-zero-receiver", &menugate.Dispatcher{}, ctx, time.Second, menugate.ErrInput},
		{"dispatch-nil-context", d, nil, time.Second, menugate.ErrInput},
		{"dispatch-zero-lease", d, ctx, 0, menugate.ErrInput},
		{"dispatch-negative-lease", d, ctx, -time.Second, menugate.ErrInput},
		{"dispatch-canceled", d, canceled, time.Second, context.Canceled},
		{"dispatch-elapsed", d, elapsed, time.Second, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := tc.d.DispatchOne(tc.ctx, tc.lease)
			if ok || !errors.Is(err, tc.want) {
				t.Fatal(ok, err, tc.want)
			}
			assertUnchanged(t)
		})
	}
	t.Run("historical-read-still-valid", func(t *testing.T) {
		r, err := hg.ReadQueued(ctx, h.source)
		if err != nil || r != historical {
			t.Fatal(r, err)
		}
		assertUnchanged(t)
	})
	t.Run("wrong-fixture-ownership", func(t *testing.T) {
		f.exec(t, `UPDATE app.cart_conversations SET business='changed' WHERE id=$1`, v.local.ConversationID().String())
		before = f.hashes(t)
		r, err := g.RunAndQueue(ctx, v.source, v.job)
		receiptRejected(t, r, err, menugate.ErrNotFound)
		r, err = g.ReadQueued(ctx, v.source)
		receiptRejected(t, r, err, menugate.ErrNotFound)
		assertUnchanged(t)
	})
	t.Run("closed-pools-and-context-priority", func(t *testing.T) {
		g.Close()
		d.Close()
		r, err := g.RunAndQueue(ctx, v.source, v.job)
		receiptRejected(t, r, err, menugate.ErrUnavailable)
		r, err = g.ReadQueued(ctx, h.source)
		receiptRejected(t, r, err, menugate.ErrUnavailable)
		r, err = g.ReadQueued(canceled, h.source)
		receiptRejected(t, r, err, context.Canceled)
		ok, err := d.DispatchOne(ctx, time.Second)
		if ok || !errors.Is(err, menugate.ErrUnavailable) {
			t.Fatal(ok, err)
		}
		ok, err = d.DispatchOne(canceled, time.Second)
		if ok || !errors.Is(err, context.Canceled) {
			t.Fatal(ok, err)
		}
		assertUnchanged(t)
	})
}

func TestDurableQueueReceipts(t *testing.T) {
	f := setup(t)
	v := f.fresh(t, conversation.ModeAI)
	g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { return answer(), nil }))
	r, err := g.ReadQueued(t.Context(), v.source)
	receiptRejected(t, r, err, menugate.ErrNotFound)
	got := queued(t, f, v, g, "ephemeral proposal")
	before := f.hashes(t)
	f.exec(t, `UPDATE app.jobs SET generation=generation+1 WHERE id=$1`, v.job.ID)
	r, err = g.ReadQueued(t.Context(), v.source)
	receiptRejected(t, r, err, menugate.ErrStoredData)
	if reflect.DeepEqual(before, f.hashes(t)) {
		t.Fatal("test drift not installed")
	}
	f.exec(t, `UPDATE app.jobs SET generation=generation-1 WHERE id=$1`, v.job.ID)
	r, err = g.ReadQueued(t.Context(), v.source)
	if err != nil || r != got {
		t.Fatal(r, err)
	}
}

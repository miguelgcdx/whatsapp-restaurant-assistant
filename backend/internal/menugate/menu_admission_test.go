package menugate_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/cart"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/menugate"
)

func nativeTurn(t *testing.T, f *fixture) (turn, cart.Admission) {
	t.Helper()
	local, control := nativeLocal(t, f)
	return nativeAdmit(t, f, local, control, time.Minute)
}

func nativeLocal(t *testing.T, f *fixture) (cart.LocalContext, conversation.State) {
	t.Helper()
	ctx := t.Context()
	f.next++
	name := fmt.Sprintf("native-%d", f.next)
	local, err := f.cart.ProvisionLocal(ctx, "fixture-business", name)
	if err != nil {
		t.Fatal(err)
	}
	control := conversation.State{ConversationID: local.ConversationID(), Mode: conversation.ModeAI}
	if got, err := f.controls.Initialize(ctx, f.token, local.ConversationID(), control.Mode, "init-"+name); err != nil || got != control {
		t.Fatalf("authenticated AI0 readiness: %+v %v", got, err)
	}
	return local, control
}

func nativeAdmit(t *testing.T, f *fixture, local cart.LocalContext, control conversation.State, lease time.Duration) (turn, cart.Admission) {
	t.Helper()
	ctx := t.Context()
	name := fmt.Sprintf("native-%d", f.next)
	event := foundation.Event{ID: name, Sender: name, Text: "  Stored native question\né  "}
	admission, created, err := f.cart.AcceptMenu(ctx, local, event, time.Now().UTC())
	if err != nil || !created || admission.JobID == "" {
		t.Fatalf("actual native admission readiness: %+v %v %v", admission, created, err)
	}
	source := admission.Source
	want := cart.Admission{Source: source, JobID: admission.JobID, Mode: "ai", Version: control.Version, State: "queued"}
	if admission != want || source.RestaurantID() != restaurant || source.ConversationID() != local.ConversationID() || source.EventUUID() == "" || source.EventProviderID() != event.ID || source.Business() != "fixture-business" || source.Sender() != event.Sender {
		t.Fatalf("exact source/immutable AI0 admission readiness: %+v source=%+v", admission, source)
	}
	claim, ok, err := f.cart.ClaimMenu(ctx, local, lease)
	job := foundation.Job{ID: admission.JobID, Event: event, Generation: 1, Attempt: 1}
	if err != nil || !ok || claim.Source() != source || claim.Job() != job {
		t.Fatalf("actual full native claim readiness: %+v %v %v", claim, ok, err)
	}
	v := turn{local, source, job}
	want.State = "leased"
	if got, err := f.cart.ReadAdmission(ctx, local, event.ID); err != nil || got != want {
		t.Fatalf("public leased admission readiness: %+v %v", got, err)
	}
	if got := f.inspect(t, v); got != (foundation.Snapshot{Event: event, JobState: "leased", JobAttempt: 1, JobCount: 1}) {
		t.Fatalf("public exact lease/no-effect readiness: %+v", got)
	}
	var deadline pgtype.Timestamptz
	var live bool
	if err := f.conn.QueryRow(ctx, `SELECT lease_until,lease_until>clock_timestamp() FROM app.jobs WHERE id=$1 AND event_id=$2 AND route='menu_message' AND restaurant_id=$3 AND conversation_id=$4 AND admission_mode='ai' AND admission_version=$5 AND generation=1 AND attempt=1`, job.ID, source.EventUUID(), restaurant, local.ConversationID().String(), control.Version).Scan(&deadline, &live); err != nil || !deadline.Valid || deadline.InfinityModifier != pgtype.Finite || !live {
		t.Fatalf("finite live full-tuple lease readiness: %+v %v %v", deadline, live, err)
	}
	if got, err := f.controls.Read(ctx, f.token, local.ConversationID()); err != nil || got != control {
		t.Fatalf("current authenticated AI0 readiness: %+v %v", got, err)
	}
	var sequence, next int64
	if err := f.conn.QueryRow(ctx, `SELECT e.sequence,s.next_sequence FROM app.inbound_events e JOIN app.conversation_sequences s ON s.sender=e.sender WHERE e.id=$1`, source.EventUUID()).Scan(&sequence, &next); err != nil || sequence != 1 || next != 1 {
		t.Fatalf("single public allocation counters readiness: %d %d %v", sequence, next, err)
	}
	t.Logf("READY: actual authenticated Initialize/AcceptMenu/ReadAdmission/ClaimMenu; exact owned source/event/job; immutable AI%d; generation1/attempt1; finite live lease; zero effects; sequence1/next1", control.Version)
	return v, want
}

func nativeCounters(t *testing.T, f *fixture) string {
	t.Helper()
	var counters string
	if err := f.conn.QueryRow(t.Context(), `SELECT COALESCE(json_agg(s ORDER BY sender)::text,'[]') FROM app.conversation_sequences s`).Scan(&counters); err != nil {
		t.Fatal(err)
	}
	return counters
}

// S1/S3 route integration: a real native leased claim must use its native guard.
// S2 epoch changes are a later independent RED, not the target of this test.
func TestMenuGateNativeRoute(t *testing.T) {
	f := setup(t)
	for _, method := range []string{"Run", "RunAndQueue"} {
		t.Run(method, func(t *testing.T) {
			v, admission := nativeTurn(t, f)
			var calls atomic.Int32
			const reply = "  Original native reply\né\n "
			g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
			}))
			before, counters, snapshot := f.hashes(t), nativeCounters(t, f), f.inspect(t, v)
			var proposal menuengine.Result
			var receipt menugate.Receipt
			var err error
			if method == "Run" {
				proposal, err = g.Run(t.Context(), v.source, v.job)
			} else {
				receipt, err = g.RunAndQueue(t.Context(), v.source, v.job)
			}
			t.Logf("TARGET %s: error=%v stale=%v model_calls=%d proposal=%+v receipt=%+v", method, err, errors.Is(err, foundation.ErrStaleLease), calls.Load(), proposal, receipt)
			if err != nil {
				if method == "Run" {
					rejected(t, proposal, err, foundation.ErrStaleLease)
				} else {
					receiptRejected(t, receipt, err, foundation.ErrStaleLease)
				}
				if calls.Load() != 0 || f.inspect(t, v) != snapshot {
					t.Fatal("native rejection invoked model or changed public processing state")
				}
				if got, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID); err != nil || got != admission {
					t.Fatalf("rejection changed public admission: %+v %v", got, err)
				}
				r, readErr := g.ReadQueued(t.Context(), v.source)
				receiptRejected(t, r, readErr, menugate.ErrNotFound)
				if got, err := f.controls.Read(t.Context(), f.token, v.local.ConversationID()); err != nil || got != (conversation.State{ConversationID: v.local.ConversationID(), Mode: conversation.ModeAI}) {
					t.Fatalf("rejection changed authenticated control: %+v %v", got, err)
				}
				f.unchanged(t, before)
				if nativeCounters(t, f) != counters {
					t.Fatal("rejection changed allocation counters")
				}
				t.Log("OBSERVED: stale rejection before model; zero output; public Inspect/ReadAdmission/ReadQueued/control, full rows and all sequence counters unchanged")
			}
			if err != nil || calls.Load() != 1 {
				t.Fatalf("native %s must accept current AI0 full claim and produce exact reply: error=%v model_calls=%d", method, err, calls.Load())
			}
			if method == "Run" {
				if proposal != (menuengine.Result{Reply: reply}) || f.inspect(t, v) != snapshot {
					t.Fatalf("exact ephemeral reply/public state: %+v", proposal)
				}
				f.unchanged(t, before)
			} else {
				if receipt.OutboxID == "" || receipt != (menugate.Receipt{OutboxID: receipt.OutboxID, Content: reply, State: "pending"}) {
					t.Fatalf("exact original Reply/AI0/pending receipt: %+v", receipt)
				}
				admission.State, admission.OutboxID, admission.Reply = "completed", receipt.OutboxID, reply
				if got := f.inspect(t, v); got != (foundation.Snapshot{Event: v.job.Event, JobState: "completed", JobAttempt: 1, OutboxState: "pending", JobCount: 1, OutboxCount: 1}) {
					t.Fatalf("exact one public completed effect: %+v", got)
				}
				if got, err := g.ReadQueued(t.Context(), v.source); err != nil || got != receipt {
					t.Fatalf("exact historical queue receipt: %+v %v", got, err)
				}
			}
			if got, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID); err != nil || got != admission || nativeCounters(t, f) != counters {
				t.Fatalf("exact final admission/counter invariance: %+v %v", got, err)
			}
		})
	}
}

// S2: current AI permission cannot revive an older immutable admission epoch.
func TestMenuGateAdmissionEpoch(t *testing.T) {
	f := setup(t)
	for _, method := range []string{"Run", "RunAndQueue"} {
		for _, action := range []string{"takeover-return", "same-mode"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				v, admission := nativeTurn(t, f)
				version := int64(1)
				if action == "takeover-return" {
					f.mode(t, v, conversation.ModeStaff)
					version = 2
				}
				f.mode(t, v, conversation.ModeAI)
				control := conversation.State{ConversationID: v.local.ConversationID(), Mode: conversation.ModeAI, Version: version}
				if got, err := f.controls.Read(t.Context(), f.token, v.local.ConversationID()); err != nil || got != control {
					t.Fatalf("authenticated current AI%d readiness: %+v %v", version, got, err)
				}
				if got, err := f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID); err != nil || got != admission || admission.Version != 0 {
					t.Fatalf("same immutable admission0 readiness: %+v %v", got, err)
				}
				snapshot := foundation.Snapshot{Event: v.job.Event, JobState: "leased", JobAttempt: 1, JobCount: 1}
				if got := f.inspect(t, v); got != snapshot || v.job.Generation != 1 || v.job.Attempt != 1 {
					t.Fatalf("same issued leased claim/no effects readiness: %+v job=%+v", got, v.job)
				}
				var calls atomic.Int32
				g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return answer(), nil }))
				r, readErr := g.ReadQueued(t.Context(), v.source)
				receiptRejected(t, r, readErr, menugate.ErrNotFound)
				before := nativeObserve(t, f, v, g)
				t.Logf("READY EPOCH: %s/%s immutable admission0/current AI%d; same issued source/job generation1/attempt1; actions complete before target; leased/no reply", method, action, version)
				got := nativeRun(t.Context(), g, v, method)
				after := nativeObserve(t, f, v, g)
				nativeRejected(t, got, menugate.ErrControlChanged, calls.Load(), 0)
				nativeSame(t, before, after)
			})
		}
	}
}

// Keep read errors as target evidence, never abort observation on corrupt history.
type nativeObservation struct {
	admission                                      cart.Admission
	snapshot                                       foundation.Snapshot
	control                                        conversation.State
	queued                                         menugate.Receipt
	admissionErr, inspectErr, controlErr, queueErr error
	rows                                           map[string]string
	counters                                       string
}

func nativeObserve(t *testing.T, f *fixture, v turn, g *menugate.Gate) (o nativeObservation) {
	t.Helper()
	o.admission, o.admissionErr = f.cart.ReadAdmission(t.Context(), v.local, v.job.Event.ID)
	o.snapshot, o.inspectErr = f.jobs.Inspect(t.Context(), v.job.Event.ID)
	o.control, o.controlErr = f.controls.Read(t.Context(), f.token, v.local.ConversationID())
	o.queued, o.queueErr = g.ReadQueued(t.Context(), v.source)
	o.rows, o.counters = f.hashes(t), nativeCounters(t, f)
	t.Logf("OBSERVED: admission=%+v/%v Inspect=%+v/%v control=%+v/%v queue=%+v/%v rows=%v counters=%s", o.admission, o.admissionErr, o.snapshot, o.inspectErr, o.control, o.controlErr, o.queued, o.queueErr, o.rows, o.counters)
	return
}

func nativeSame(t *testing.T, before, after nativeObservation) {
	t.Helper()
	if after.admissionErr != nil || after.admission != before.admission {
		t.Errorf("admission/history changed: %+v/%v want %+v", after.admission, after.admissionErr, before.admission)
	}
	if after.inspectErr != nil || after.snapshot != before.snapshot {
		t.Errorf("processing changed: %+v/%v want %+v", after.snapshot, after.inspectErr, before.snapshot)
	}
	if after.controlErr != nil || after.control != before.control {
		t.Errorf("control changed: %+v/%v want %+v", after.control, after.controlErr, before.control)
	}
	if !errors.Is(after.queueErr, before.queueErr) || after.queued != before.queued {
		t.Errorf("queue changed: %+v/%v want %+v/%v", after.queued, after.queueErr, before.queued, before.queueErr)
	}
	if !reflect.DeepEqual(before.rows, after.rows) {
		t.Error("full persisted rows changed")
	}
	if after.counters != before.counters {
		t.Errorf("allocation counters changed: %s want %s", after.counters, before.counters)
	}
}

type nativeResult struct {
	proposal menuengine.Result
	receipt  menugate.Receipt
	err      error
}

func nativeRun(ctx context.Context, g *menugate.Gate, v turn, method string) (out nativeResult) {
	if method == "Run" {
		out.proposal, out.err = g.Run(ctx, v.source, v.job)
	} else {
		out.receipt, out.err = g.RunAndQueue(ctx, v.source, v.job)
	}
	return
}

func nativeRejected(t *testing.T, got nativeResult, want error, calls, expected int32) {
	t.Helper()
	t.Logf("TARGET: error=%v model_calls=%d proposal=%+v receipt=%+v", got.err, calls, got.proposal, got.receipt)
	if !errors.Is(got.err, want) {
		t.Errorf("error=%v want %v", got.err, want)
	}
	if got.proposal != (menuengine.Result{}) || got.receipt != (menugate.Receipt{}) {
		t.Errorf("not whole zero: %+v", got)
	}
	if calls != expected {
		t.Errorf("model_calls=%d want%d", calls, expected)
	}
}

// Fingerprint all unrelated rows, not just whole tables other than jobs/outbox.
func nativeOtherRows(t *testing.T, f *fixture, v turn) map[string]string {
	t.Helper()
	rows := f.hashes(t)
	for table, projection := range map[string]string{
		"jobs":          `SELECT CASE WHEN id=$1 THEN to_jsonb(r)-'state'-'lease_until' ELSE to_jsonb(r) END AS data FROM app.jobs r`,
		"outbox":        `SELECT to_jsonb(r) AS data FROM app.outbox r WHERE event_id<>$2`,
		"send_attempts": `SELECT to_jsonb(r) AS data FROM app.send_attempts r WHERE outbox_id NOT IN (SELECT id FROM app.outbox WHERE event_id=$2)`,
	} {
		var value string
		sql := `WITH args AS (SELECT $1::uuid,$2::uuid), r AS (` + projection + `) SELECT count(*)::text||':'||md5(COALESCE(string_agg(data::text,',' ORDER BY data::text),'')) FROM r`
		if err := f.conn.QueryRow(t.Context(), sql, v.job.ID, v.source.EventUUID()).Scan(&value); err != nil {
			t.Fatal(err)
		}
		rows[table] = value
	}
	return rows
}

func TestMenuGatePostModelEpoch(t *testing.T) {
	f := setup(t)
	for _, method := range []string{"Run", "RunAndQueue"} {
		for _, action := range []string{"takeover-return", "same-mode"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				v, admission := nativeTurn(t, f)
				entered, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					close(entered)
					select {
					case <-release:
						return answer(), nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}))
				ctx, cancel := context.WithCancel(t.Context())
				var once sync.Once
				free := func() { once.Do(func() { close(release) }) }
				done, joined := make(chan nativeResult, 1), make(chan struct{})
				t.Cleanup(func() {
					cancel()
					free()
					select {
					case <-joined:
						t.Log("JOINED: model worker before gate close")
					case <-time.After(3 * time.Second):
						t.Error("model worker failed bounded join")
					}
				})
				go func() { defer close(joined); done <- nativeRun(ctx, g, v, method) }()
				await(t, entered)
				version := int64(1)
				if action == "takeover-return" {
					f.mode(t, v, conversation.ModeStaff)
					version = 2
				}
				f.mode(t, v, conversation.ModeAI)
				before := nativeObserve(t, f, v, g)
				if before.controlErr != nil || before.control != (conversation.State{ConversationID: v.local.ConversationID(), Mode: conversation.ModeAI, Version: version}) || before.admissionErr != nil || before.admission != admission || before.inspectErr != nil || before.snapshot != (foundation.Snapshot{Event: v.job.Event, JobState: "leased", JobAttempt: 1, JobCount: 1}) || !errors.Is(before.queueErr, menugate.ErrNotFound) || before.queued != (menugate.Receipt{}) {
					t.Fatal("authenticated post-action readiness", before)
				}
				t.Log("READY: authenticated actions completed while actual transport blocked; baseline after actions")
				free()
				got := await(t, done)
				after := nativeObserve(t, f, v, g)
				nativeRejected(t, got, menugate.ErrControlChanged, calls.Load(), 1)
				nativeSame(t, before, after)
			})
		}
	}
}

func TestMenuGateFreshEpoch(t *testing.T) {
	f := setup(t)
	for _, method := range []string{"Run", "RunAndQueue"} {
		t.Run(method, func(t *testing.T) {
			local, control := nativeLocal(t, f)
			f.mode(t, turn{local: local}, conversation.ModeAI)
			control.Version = 1
			v, admission := nativeAdmit(t, f, local, control, time.Minute)
			var calls, provider atomic.Int32
			const reply = "  Fresh epoch reply\né\n "
			c := client(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
			})
			g := openGate(t, f, c)
			before, other := nativeObserve(t, f, v, g), nativeOtherRows(t, f, v)
			receiptRejected(t, before.queued, before.queueErr, menugate.ErrNotFound)
			got := nativeRun(t.Context(), g, v, method)
			after, afterOther := nativeObserve(t, f, v, g), nativeOtherRows(t, f, v)
			t.Logf("TARGET FRESH: error=%v model_calls=%d proposal=%+v receipt=%+v", got.err, calls.Load(), got.proposal, got.receipt)
			if got.err != nil || calls.Load() != 1 {
				t.Fatal("fresh AI1 rejected", got.err, calls.Load())
			}
			if method == "Run" {
				if got.proposal != (menuengine.Result{Reply: reply}) || got.receipt != (menugate.Receipt{}) {
					t.Error("wrong entire ephemeral output", got)
				}
				nativeSame(t, before, after)
				return
			}
			r := menugate.Receipt{OutboxID: got.receipt.OutboxID, Content: reply, ControlVersion: 1, State: "pending"}
			admission.State, admission.OutboxID, admission.Reply = "completed", r.OutboxID, reply
			if r.OutboxID == "" || got.proposal != (menuengine.Result{}) || got.receipt != r || after.queued != r || after.queueErr != nil || after.admissionErr != nil || after.admission != admission || after.inspectErr != nil || after.snapshot != (foundation.Snapshot{Event: v.job.Event, JobState: "completed", JobAttempt: 1, OutboxState: "pending", JobCount: 1, OutboxCount: 1}) {
				t.Fatal("wrong exact queue/history/processing", got, after)
			}
			if after.controlErr != nil || after.control != control || after.counters != before.counters || !reflect.DeepEqual(other, afterOther) {
				t.Error("queue changed control/counters/unrelated rows")
			}
			g.Close()
			g = openGate(t, f, c)
			for _, takeover := range []bool{false, true} {
				if takeover {
					f.mode(t, v, conversation.ModeStaff)
				}
				b := nativeObserve(t, f, v, g)
				if b.admissionErr != nil || b.admission != admission || b.queueErr != nil || b.queued != r {
					t.Fatal("reopened historical readiness", b)
				}
				replay := nativeRun(t.Context(), g, v, "RunAndQueue")
				a := nativeObserve(t, f, v, g)
				nativeRejected(t, replay, foundation.ErrStaleLease, calls.Load(), 1)
				nativeSame(t, b, a)
			}
			d := dispatcher(t, f, func(w http.ResponseWriter, _ *http.Request) {
				provider.Add(1)
				fmt.Fprint(w, `{"message_id":"unexpected"}`)
			})
			other, counters := nativeOtherRows(t, f, v), nativeCounters(t, f)
			ok, err := d.DispatchOne(t.Context(), time.Minute)
			suppressed, suppressedOther := nativeObserve(t, f, v, g), nativeOtherRows(t, f, v)
			r.State, r.Attempt = "suppressed", 1
			if err != nil || !ok || provider.Load() != 0 || suppressed.queued != r || suppressed.queueErr != nil || suppressed.admissionErr != nil || suppressed.admission != admission || suppressed.inspectErr != nil || suppressed.snapshot != (foundation.Snapshot{Event: v.job.Event, JobState: "completed", JobAttempt: 1, OutboxState: "suppressed", JobCount: 1, OutboxCount: 1}) {
				t.Fatal("wrong suppression/history", ok, err, provider.Load(), suppressed)
			}
			if suppressed.controlErr != nil || suppressed.control != (conversation.State{ConversationID: local.ConversationID(), Mode: conversation.ModeStaff, Version: 2}) || suppressed.counters != counters || !reflect.DeepEqual(other, suppressedOther) {
				t.Error("suppression changed unrelated authority/data")
			}
			attemptState(t, f, r.OutboxID, "suppressed", 1)
			ok, err = d.DispatchOne(t.Context(), time.Minute)
			final := nativeObserve(t, f, v, g)
			if err != nil || ok || provider.Load() != 0 || calls.Load() != 1 {
				t.Error("repeat dispatch performed work", ok, err, provider.Load(), calls.Load())
			}
			nativeSame(t, suppressed, final)
			t.Log("OBSERVED: reopened completed replay before/after takeover stale/model unchanged; suppression once then no work/provider0; exact history and unrelated rows/counters retained")
		})
	}
}

func TestMenuGateNativeFences(t *testing.T) {
	f := setup(t)
	for _, method := range []string{"Run", "RunAndQueue"} {
		for _, field := range []string{"attempt", "text"} {
			t.Run(method+"/"+field, func(t *testing.T) {
				v, _ := nativeTurn(t, f)
				var calls atomic.Int32
				g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return answer(), nil }))
				before := nativeObserve(t, f, v, g)
				caller := v
				if field == "attempt" {
					caller.job.Attempt++
				} else {
					caller.job.Event.Text += "changed"
				}
				got := nativeRun(t.Context(), g, caller, method)
				after := nativeObserve(t, f, v, g)
				nativeRejected(t, got, foundation.ErrStaleLease, calls.Load(), 0)
				nativeSame(t, before, after)
			})
		}
	}
}

// Cleanup owns cancellation, release, blocker rollback and every launched join.
func nativeJoin(t *testing.T, cancel context.CancelFunc, release func(), tx pgx.Tx, joined <-chan struct{}, workers *int) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		release()
		if tx != nil {
			ctx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Error(err)
			}
		}
		for range *workers {
			select {
			case <-joined:
				t.Log("JOINED: native worker before gate close")
			case <-time.After(3 * time.Second):
				t.Error("native worker failed bounded join")
			}
		}
	})
}

// S1/S3: native authority and the final insert must use a fresh database clock.
func TestMenuGateLeaseWait(t *testing.T) {
	f := setup(t)
	for _, phase := range []string{"Run", "RunAndQueue", "final-insert"} {
		t.Run(phase, func(t *testing.T) {
			v, _ := nativeTurn(t, f)
			final := phase == "final-insert"
			method := phase
			if final {
				method = "RunAndQueue"
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var calls atomic.Int32
			g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if final {
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return answer(), nil
			}))
			ctx, cancel := context.WithCancel(t.Context())
			var once sync.Once
			free := func() { once.Do(func() { close(release) }) }
			done, joined := make(chan nativeResult, 1), make(chan struct{}, 1)
			workers := 0
			c := connect(t, f.dsn)
			tx, err := c.Begin(t.Context())
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			nativeJoin(t, cancel, free, tx, joined, &workers)
			start := func() {
				workers++
				go func() { done <- nativeRun(ctx, g, v, method); joined <- struct{}{} }()
			}
			if final {
				start()
				await(t, entered)
			}
			f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '400 milliseconds' WHERE id=$1`, v.job.ID)
			var finiteLive bool
			if err := f.conn.QueryRow(t.Context(), `SELECT isfinite(lease_until) AND lease_until>clock_timestamp() FROM app.jobs WHERE id=$1`, v.job.ID).Scan(&finiteLive); err != nil || !finiteLive {
				t.Fatal("short finite live lease readiness", finiteLive, err)
			}
			before := nativeObserve(t, f, v, g)
			sql, arg, prefix := `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant, "SELECT id::text FROM app.restaurants"
			if final {
				sql, arg, prefix = `INSERT INTO app.outbox(id,event_id,content,state,due_at) VALUES('a0000000-0000-4000-8000-000000000099',$1,'blocker','pending',clock_timestamp())`, v.source.EventUUID(), "-- name: InsertOutbox"
			}
			if _, err = tx.Exec(t.Context(), sql, arg); err != nil {
				t.Fatal(err)
			}
			if final {
				free()
			} else {
				start()
			}
			waitLock(t, f.conn, prefix)
			expired(t, f, v)
			t.Log("READY: observed expected query lock wait then actual DB-clock expiry")
			if err = tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := await(t, done)
			after := nativeObserve(t, f, v, g)
			wantCalls := int32(0)
			if final {
				wantCalls = 1
			}
			nativeRejected(t, got, foundation.ErrStaleLease, calls.Load(), wantCalls)
			nativeSame(t, before, after)
		})
	}
}

// S1/S3: two model proposals for one issued claim yield exactly one durable effect.
func TestMenuGateConcurrentClaim(t *testing.T) {
	f := setup(t)
	v, admission := nativeTurn(t, f)
	const reply = "  Concurrent native reply\né\n "
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		select {
		case <-release:
			return response(cc.Message{Role: cc.RoleAssistant, Content: ptr(reply)}, "stop"), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}))
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	done, joined := make(chan nativeResult, 2), make(chan struct{}, 2)
	workers := 0
	nativeJoin(t, cancel, free, nil, joined, &workers)
	before, other := nativeObserve(t, f, v, g), nativeOtherRows(t, f, v)
	for range 2 {
		workers++
		go func() { done <- nativeRun(ctx, g, v, "RunAndQueue"); joined <- struct{}{} }()
	}
	await(t, entered)
	await(t, entered)
	// Authenticated idempotent Initialize takes restaurant/control UPDATE locks,
	// but reuses its original operation; it cannot change the admission epoch.
	auth, stop := context.WithTimeout(t.Context(), time.Second)
	control, err := f.controls.Initialize(auth, f.token, v.local.ConversationID(), conversation.ModeAI, fmt.Sprintf("init-native-%d", f.next))
	stop()
	if err != nil || control != before.control {
		t.Fatal("model retained authority or replay changed epoch", control, err)
	}
	t.Log("READY: both transports entered; authenticated Initialize replay acquired authority locks without epoch change")
	free()
	winner, loser := await(t, done), await(t, done)
	after, afterOther := nativeObserve(t, f, v, g), nativeOtherRows(t, f, v)
	t.Logf("TARGET CONCURRENT: first=%+v second=%+v model_calls=%d", winner, loser, calls.Load())
	if winner.err != nil {
		winner, loser = loser, winner
	}
	nativeRejected(t, loser, foundation.ErrStaleLease, calls.Load(), 2)
	want := menugate.Receipt{OutboxID: winner.receipt.OutboxID, Content: reply, State: "pending"}
	if winner.err != nil || winner.proposal != (menuengine.Result{}) || want.OutboxID == "" || winner.receipt != want {
		t.Error("wrong entire winning output", winner)
	}
	admission.State, admission.OutboxID, admission.Reply = "completed", want.OutboxID, reply
	if after.admissionErr != nil || after.admission != admission || after.queueErr != nil || after.queued != want {
		t.Error("wrong exact native completed history", after)
	}
	if after.inspectErr != nil || after.snapshot != (foundation.Snapshot{Event: v.job.Event, JobState: "completed", JobAttempt: 1, OutboxState: "pending", JobCount: 1, OutboxCount: 1}) {
		t.Error("not exactly one completed job/effect", after.snapshot, after.inspectErr)
	}
	if after.controlErr != nil || after.control != before.control || after.counters != before.counters || !reflect.DeepEqual(other, afterOther) {
		t.Error("concurrent completion changed control/counters/unrelated rows")
	}
	attemptState(t, f, want.OutboxID, "", 0)
}

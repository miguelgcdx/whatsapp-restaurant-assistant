package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

func previewCall(t *testing.T, s cart.Snapshot) cart.Call {
	t.Helper()
	c, e := cart.CaptureSummaryPreviewCall(s.ID, s.Version)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func previewReadCall(t *testing.T, id cart.SummaryPreviewID) cart.Call {
	t.Helper()
	c, e := cart.ReadSummaryPreviewCall(id)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func previewRun(t *testing.T, f *draftFixture, p cart.Execution) cart.SummaryPreviewResult {
	t.Helper()
	r, e := f.s.ExecuteSummaryPreview(t.Context(), f.local, p, f.job)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func previewState(t *testing.T, f *draftFixture) string {
	t.Helper()
	var s string
	if e := f.conn.QueryRow(t.Context(), `SELECT json_build_array((SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.cart_summary_previews r),(SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.inbound_events r),(SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.jobs r))::text`).Scan(&s); e != nil {
		t.Fatal(e)
	}
	return f.state(t) + s
}
func previewRejected(t *testing.T, f *draftFixture, p cart.Execution, job foundation.Job, want error, s cart.Snapshot) {
	t.Helper()
	before := previewState(t, f)
	r, e := f.s.ExecuteSummaryPreview(t.Context(), f.local, p, job)
	if !errors.Is(e, want) || !reflect.DeepEqual(r, cart.SummaryPreviewResult{}) {
		t.Fatalf("rejected=%+v %v want %v", r, e, want)
	}
	read, e := f.s.Read(t.Context(), f.local, s.ID)
	if e != nil || !reflect.DeepEqual(read, s) || previewState(t, f) != before {
		t.Fatalf("rejection changed state %+v %v", read, e)
	}
}
func previewSelected(t *testing.T, f *draftFixture, units int64, choice cart.Fulfillment) cart.DraftResult {
	t.Helper()
	b := create(t, f.fixture)
	if units > 0 {
		b = apply(t, f.fixture, b, add(t, units))
	}
	return f.execute(t, setCall(t, b.Cart, choice))
}
func previewAssert(t *testing.T, f *draftFixture, p cart.Execution, s cart.DraftResult, r cart.SummaryPreviewResult, names []string, exact string) {
	t.Helper()
	units := make([]cart.SummaryPreviewUnit, len(s.Cart.Units))
	for i, u := range s.Cart.Units {
		units[i] = cart.SummaryPreviewUnit{Unit: u, Name: names[i]}
	}
	want := cart.SummaryPreviewResult{OperationID: p.OperationID(), Preview: cart.SummaryPreview{
		ID: r.Preview.ID, SourceOperationID: p.OperationID(), CartID: s.Cart.ID, ConversationID: s.Cart.ConversationID,
		SourceCartVersion: s.Cart.Version, CapturedAt: r.Preview.CapturedAt, Currency: s.Cart.Currency, Exponent: s.Cart.Exponent,
		Units: units, UnitCount: len(units), Fulfillment: s.Fulfillment, Quote: s.Quote, NumericEstimate: nil,
		EstimateReason: cart.WorkloadNotConnected, Text: exact,
	}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("complete preview\ngot=%+v\nwant=%+v", r, want)
	}
	if r.Preview.CapturedAt.IsZero() || r.Preview.CapturedAt.Location() != time.UTC {
		t.Fatalf("capture time %v", r.Preview.CapturedAt)
	}
	if _, e := cart.ParseSummaryPreviewID(r.Preview.ID.String()); e != nil {
		t.Fatal(e)
	}
	readPlan := f.plan(t, previewReadCall(t, r.Preview.ID))
	before := previewState(t, f)
	read := previewRun(t, f, readPlan)
	want.OperationID = readPlan.OperationID()
	if !reflect.DeepEqual(read, want) || before != previewState(t, f) {
		t.Fatalf("immutable read=%+v", read)
	}
	current, e := f.s.Read(t.Context(), f.local, s.Cart.ID)
	if e != nil || !reflect.DeepEqual(current, s.Cart) {
		t.Fatalf("capture bumped cart %+v %v", current, e)
	}
	state, e := f.jobs.Inspect(t.Context(), f.event)
	if e != nil || state.OutboxCount != 0 || state.JobCount != 1 || state.JobAttempt != 1 {
		t.Fatalf("business side effects %+v %v", state, e)
	}
}

func TestSummaryPreviewInput(t *testing.T) {
	good := "20000000-0000-4000-8000-000000000001"
	id, e := cart.ParseSummaryPreviewID(good)
	if e != nil || id.String() != good {
		t.Fatal(id, e)
	}
	b, e := json.Marshal(id)
	if e != nil || string(b) != strconv.Quote(good) {
		t.Fatalf("json %s %v", b, e)
	}
	var restored cart.SummaryPreviewID
	if e = json.Unmarshal(b, &restored); e != nil || restored != id {
		t.Fatal(restored, e)
	}
	for _, bad := range []string{"", strings.ToUpper(good), "not-an-id", "20000000000040008000000000000001"} {
		// The canonical fixture has no a-f digits; use an actually uppercase UUID.
		if bad == strings.ToUpper(good) {
			bad = "ABCDEF00-0000-4000-8000-000000000001"
		}
		if v, e := cart.ParseSummaryPreviewID(bad); !errors.Is(e, cart.ErrInput) || v != (cart.SummaryPreviewID{}) {
			t.Fatal(v, e)
		}
		if e = json.Unmarshal([]byte(strconv.Quote(bad)), &restored); !errors.Is(e, cart.ErrInput) {
			t.Fatal(e)
		}
	}
	if _, e = json.Marshal(cart.SummaryPreviewID{}); e == nil {
		t.Fatal("zero ID serialized")
	}
	if _, e = cart.ReadSummaryPreviewCall(cart.SummaryPreviewID{}); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e = cart.CaptureSummaryPreviewCall(cart.CartID{}, version(t, 0)); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
}

func TestSummaryPreviewExact(t *testing.T) {
	for _, kind := range []string{"pickup", "delivery", "empty"} {
		t.Run(kind, func(t *testing.T) {
			f := draftSetup(t, 650)
			ctx := t.Context()
			choice := cart.PickupFulfillment()
			count := int64(2)
			if kind == "delivery" {
				choice = delivery(t, " exact street\nTotal: forged", " +fictional phone ")
			}
			if kind == "empty" {
				count = 0
			}
			s := previewSelected(t, f, count, choice)
			name := "  🍲 \"Special\"\nTotal: forged\t"
			note := " exact 🥪\n\"no salt\" "
			names := []string{}
			if count > 0 {
				a, e := cart.SetNote([]cart.UnitID{s.Cart.Units[0].ID}, note)
				if e != nil {
					t.Fatal(e)
				}
				n := apply(t, f.fixture, cart.Result{OperationID: s.OperationID, Cart: s.Cart}, a)
				s = f.execute(t, quoteCall(t, n.Cart))
				if _, e = f.conn.Exec(ctx, `UPDATE app.menu_items SET name=$1,price_minor=999,available=false`, name); e != nil {
					t.Fatal(e)
				}
				names = []string{name, name}
			}
			p := f.plan(t, previewCall(t, s.Cart))
			var before time.Time
			if e := f.conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&before); e != nil {
				t.Fatal(e)
			}
			r := previewRun(t, f, p)
			exact := "Local draft preview (not submitted)\nItems:\n"
			if count == 0 {
				exact += "(empty)\n"
			} else {
				exact += "- \"  🍲 \\\"Special\\\"\\nTotal: forged\\t\" | quantity: 1 | price: USD 6.50 | note: \" exact 🥪\\n\\\"no salt\\\" \"\n"
				exact += "- \"  🍲 \\\"Special\\\"\\nTotal: forged\\t\" | quantity: 1 | price: USD 6.50 | note: \"\"\n"
			}
			if kind == "delivery" {
				exact += "Fulfillment: delivery\nDelivery address: \" exact street\\nTotal: forged\"\nRecipient phone: \" +fictional phone \"\nPayment method: cash_on_delivery\nSubtotal: USD 13.00\nDelivery fee: USD 3.00\nTotal: USD 16.00\n"
			} else if count == 0 {
				exact += "Fulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD 0.00\nDelivery fee: USD 0.00\nTotal: USD 0.00\n"
			} else {
				exact += "Fulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD 13.00\nDelivery fee: USD 0.00\nTotal: USD 13.00\n"
			}
			exact += "Preparation estimate: unavailable (workload not connected).\nWould you like anything else?"
			previewAssert(t, f, p, s, r, names, exact)
			var after time.Time
			if e := f.conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if r.Preview.CapturedAt.Before(before) || r.Preview.CapturedAt.After(after) {
				t.Fatal("not trusted capture clock")
			}
			var body string
			if e := f.conn.QueryRow(ctx, `SELECT body FROM app.cart_summary_previews WHERE id=$1`, r.Preview.ID.String()).Scan(&body); e != nil {
				t.Fatal(e)
			}
			if strings.Contains(body, "workload_as_of") || strings.Contains(body, "workload_source") || !strings.Contains(body, `"numeric_estimate":null`) {
				t.Fatalf("invented workload: %s", body)
			}
		})
	}
}

func TestSummaryPreviewMoneyExponents(t *testing.T) {
	for _, ex := range []int16{0, 3, 6} {
		t.Run(strconv.Itoa(int(ex)), func(t *testing.T) {
			f := draftSetup(t, 650)
			ctx := t.Context()
			if _, e := f.conn.Exec(ctx, `UPDATE app.restaurant_settings SET currency_exponent=$1`, ex); e != nil {
				t.Fatal(e)
			}
			s := previewSelected(t, f, 1, cart.PickupFulfillment())
			r := previewRun(t, f, f.plan(t, previewCall(t, s.Cart)))
			amounts := map[int16]string{0: "650", 3: "0.650", 6: "0.000650"}
			exact := "Local draft preview (not submitted)\nItems:\n- \"Fictional sandwich\" | quantity: 1 | price: USD " + amounts[ex] + " | note: \"\"\nFulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: USD " + amounts[ex] + "\nDelivery fee: USD "
			zeros := map[int16]string{0: "0", 3: "0.000", 6: "0.000000"}
			exact += zeros[ex] + "\nTotal: USD " + amounts[ex] + "\nPreparation estimate: unavailable (workload not connected).\nWould you like anything else?"
			if r.Preview.Text != exact || r.Preview.Quote != s.Quote || r.Preview.NumericEstimate != nil {
				t.Fatalf("money %+v", r)
			}
		})
	}
}

func TestSummaryPreviewHistoryAndNamespaces(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	s := previewSelected(t, f, 2, delivery(t, "a", "p"))
	c := previewCall(t, s.Cart)
	p := f.plan(t, c)
	old := previewRun(t, f, p)
	// Detached snapshots and mutable source rows do not authorize historical output.
	old.Preview.Units[0].Name = "caller changed"
	old = previewRun(t, f, p)
	rm, e := cart.Remove([]cart.UnitID{s.Cart.Units[0].ID})
	if e != nil {
		t.Fatal(e)
	}
	current := apply(t, f.fixture, cart.Result{OperationID: s.OperationID, Cart: s.Cart}, rm)
	if _, e = f.conn.Exec(ctx, `UPDATE app.menu_items SET name='new name',price_minor=777,available=false; UPDATE app.restaurant_settings SET flat_fee_minor=9,version=version+1`); e != nil {
		t.Fatal(e)
	}
	freshPlan := f.plan(t, previewCall(t, current.Cart))
	fresh := previewRun(t, f, freshPlan)
	if fresh.Preview.ID == old.Preview.ID || fresh.Preview.Units[0].Name != "new name" || fresh.Preview.Units[0].PriceMinor != 650 || fresh.Preview.Quote.Fee != 9 || fresh.Preview.Quote.SettingsVersion != 1 || fresh.Preview.SourceCartVersion != current.Cart.Version {
		t.Fatalf("fresh %+v", fresh)
	}
	readPlan := f.plan(t, previewReadCall(t, old.Preview.ID))
	before := previewState(t, f)
	for _, plan := range []cart.Execution{p, readPlan} {
		r := previewRun(t, f, plan)
		want := old
		want.OperationID = plan.OperationID()
		if !reflect.DeepEqual(r, want) {
			t.Fatalf("history %+v", r)
		}
	}
	if previewState(t, f) != before {
		t.Fatal("history wrote state")
	}
	reopened, e := cart.OpenStore(ctx, f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	local, e := reopened.LoadLocal(ctx, f.local.ConversationID(), "fictional-business", "fictional-sender")
	if e != nil {
		t.Fatal(e)
	}
	r, e := reopened.ExecuteSummaryPreview(ctx, local, p, f.job)
	if e != nil || !reflect.DeepEqual(r, old) {
		t.Fatalf("reopen %+v %v", r, e)
	}
	for _, plan := range []cart.Execution{p, readPlan} {
		before = previewState(t, f)
		a, e := f.s.Execute(ctx, f.local, plan, f.job)
		if !errors.Is(e, cart.ErrInput) || !reflect.DeepEqual(a, cart.Result{}) {
			t.Fatal(a, e)
		}
		b, e := f.s.ExecuteDraft(ctx, f.local, plan, f.job)
		if !errors.Is(e, cart.ErrInput) || !reflect.DeepEqual(b, cart.DraftResult{}) || before != previewState(t, f) {
			t.Fatal(b, e)
		}
	}
	for _, call := range []cart.Call{cart.CreateCall(), quoteCall(t, current.Cart), setCall(t, current.Cart, cart.PickupFulfillment())} {
		previewRejected(t, f, f.plan(t, call), f.job, cart.ErrInput, current.Cart)
	}
	// Direct old mutation under a committed preview ID must conflict and preserve it.
	before = previewState(t, f)
	a, e := f.s.Create(ctx, f.local, p.OperationID())
	if !errors.Is(e, cart.ErrConflict) || !reflect.DeepEqual(a, cart.Result{}) || before != previewState(t, f) {
		t.Fatal(a, e)
	}
	collision := f.plan(t, previewCall(t, current.Cart))
	legacy, e := f.s.Create(ctx, f.local, collision.OperationID())
	if e != nil {
		t.Fatal(e)
	}
	previewRejected(t, f, collision, f.job, cart.ErrConflict, current.Cart)
	legacyAgain, e := f.s.Create(ctx, f.local, collision.OperationID())
	if e != nil || !reflect.DeepEqual(legacyAgain, legacy) {
		t.Fatal(legacyAgain, e)
	}
	// Slot intent is frozen both across result families and within capture versions.
	for _, call := range []cart.Call{cart.CreateCall(), previewCall(t, current.Cart), previewReadCall(t, old.Preview.ID)} {
		before = previewState(t, f)
		// Original selection is slot 1, capture is slot 2.
		x, e := f.s.RegisterExecution(ctx, f.local, f.event, 2, call)
		if !errors.Is(e, cart.ErrConflict) || x != (cart.Execution{}) || before != previewState(t, f) {
			t.Fatalf("slot intent %+v %v", x, e)
		}
	}
	retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
	if e != nil || !ok {
		t.Fatal(e)
	}
	previewRejected(t, f, p, f.job, foundation.ErrStaleLease, current.Cart)
	r, e = f.s.ExecuteSummaryPreview(ctx, f.local, p, retry)
	if e != nil || !reflect.DeepEqual(r, old) {
		t.Fatalf("reclaim %+v %v", r, e)
	}
}

func TestSummaryPreviewRejections(t *testing.T) {
	for _, scenario := range []string{"unselected", "stale", "foreign-cart", "foreign-context", "unknown-preview", "foreign-preview", "name-blank", "name-long", "settings", "settings-fee", "settings-version", "selection", "overflow", "oversize", "body", "metadata", "metadata-time", "result", "result-id", "estimate", "reason", "text", "duplicate-unit", "source-plan", "copies"} {
		t.Run(scenario, func(t *testing.T) {
			price := int64(650)
			if scenario == "overflow" {
				price = math.MaxInt64
			}
			f := draftSetup(t, price)
			ctx := t.Context()
			b := create(t, f.fixture)
			if scenario != "unselected" {
				b = apply(t, f.fixture, b, add(t, 1))
			}
			var selected cart.DraftResult
			if scenario != "unselected" {
				selected = f.execute(t, setCall(t, b.Cart, cart.PickupFulfillment()))
				b.Cart = selected.Cart
			}
			p := f.plan(t, previewCall(t, b.Cart))
			want := cart.ErrStoredData
			switch scenario {
			case "unselected":
				want = cart.ErrFulfillmentRequired
			case "stale":
				a, e := cart.SetNote([]cart.UnitID{b.Cart.Units[0].ID}, "new")
				if e != nil {
					t.Fatal(e)
				}
				b = apply(t, f.fixture, b, a)
				want = cart.ErrStale
			case "foreign-cart", "foreign-context", "foreign-preview":
				other, e := f.s.ProvisionLocal(ctx, "other", "other")
				if e != nil {
					t.Fatal(e)
				}
				if scenario == "foreign-cart" {
					foreign := create(t, fixture{s: f.s, local: other})
					p = f.plan(t, previewCall(t, foreign.Cart))
					want = cart.ErrNotFound
				} else {
					job := f.job
					if scenario == "foreign-preview" {
						saved := previewRun(t, f, p)
						event := foundation.Event{ID: "foreign-preview", Sender: "other", Text: "opaque"}
						now := time.Now()
						if _, e = f.jobs.Accept(ctx, event, now); e != nil {
							t.Fatal(e)
						}
						var ok bool
						job, ok, e = f.jobs.ClaimJob(ctx, now, time.Minute)
						if e != nil || !ok || job.Event.ID != event.ID {
							t.Fatal(job, e)
						}
						p, e = f.s.RegisterExecution(ctx, other, event.ID, 0, previewReadCall(t, saved.Preview.ID))
						if e != nil {
							t.Fatal(e)
						}
					}
					before := previewState(t, f)
					r, e := f.s.ExecuteSummaryPreview(ctx, other, p, job)
					if !errors.Is(e, cart.ErrNotFound) || !reflect.DeepEqual(r, cart.SummaryPreviewResult{}) || before != previewState(t, f) {
						t.Fatal(r, e)
					}
					current, e := f.s.Read(ctx, f.local, b.Cart.ID)
					if e != nil || !reflect.DeepEqual(current, b.Cart) {
						t.Fatal(current, e)
					}
					return
				}
			case "unknown-preview":
				id, e := cart.ParseSummaryPreviewID("20000000-0000-4000-8000-000000000099")
				if e != nil {
					t.Fatal(e)
				}
				p = f.plan(t, previewReadCall(t, id))
				want = cart.ErrNotFound
			case "name-blank", "name-long":
				name := " \n\u2003"
				if scenario == "name-long" {
					name = strings.Repeat("🥪", 201)
				}
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.menu_items DROP CONSTRAINT menu_items_name_check`); e != nil {
					t.Fatal(e)
				}
				if _, e := f.conn.Exec(ctx, `UPDATE app.menu_items SET name=$1`, name); e != nil {
					t.Fatal(e)
				}
			case "settings":
				if _, e := f.conn.Exec(ctx, `UPDATE app.restaurant_settings SET currency_exponent=3`); e != nil {
					t.Fatal(e)
				}
			case "settings-fee":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.restaurant_settings DROP CONSTRAINT restaurant_settings_flat_fee_minor_check; UPDATE app.restaurant_settings SET flat_fee_minor=-1`); e != nil {
					t.Fatal(e)
				}
			case "settings-version":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.restaurant_settings DROP CONSTRAINT restaurant_settings_version_check; UPDATE app.restaurant_settings SET version=-1`); e != nil {
					t.Fatal(e)
				}
			case "selection":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.cart_fulfillments DROP CONSTRAINT cart_fulfillments_selection_check; UPDATE app.cart_fulfillments SET kind='invalid'`); e != nil {
					t.Fatal(e)
				}
			case "overflow":
				if _, e := f.conn.Exec(ctx, `UPDATE app.cart_fulfillments SET kind='delivery',address='a',recipient_phone='p'`); e != nil {
					t.Fatal(e)
				}
				want = cart.ErrLimit
			case "oversize":
				// Valid direct-cart state fits its budget; quoted names/notes make preview exceed it.
				note := strings.Repeat("\n", 500)
				if _, e := f.conn.Exec(ctx, `UPDATE app.cart_units SET note=$1;`, note); e != nil {
					t.Fatal(e)
				}
				if _, e := f.conn.Exec(ctx, `INSERT INTO app.cart_units(id,restaurant_id,conversation_id,cart_id,menu_id,position,price_minor,currency,exponent,note,created_at) SELECT gen_random_uuid(),restaurant_id,conversation_id,cart_id,menu_id,x,price_minor,currency,exponent,note,created_at FROM app.cart_units CROSS JOIN generate_series(1,200) x`); e != nil {
					t.Fatal(e)
				}
				var e error
				b.Cart, e = f.s.Read(ctx, f.local, b.Cart.ID)
				if e != nil {
					t.Fatal(e)
				}
				want = cart.ErrLimit
			default:
				saved := previewRun(t, f, p)
				var sql string
				var args []any
				switch scenario {
				case "body":
					sql = `UPDATE app.cart_summary_previews SET body='{}'`
				case "metadata":
					sql = `UPDATE app.cart_summary_previews SET source_cart_version=source_cart_version+1`
				case "metadata-time":
					sql = `UPDATE app.cart_summary_previews SET captured_at='infinity'`
				case "result-id":
					sql = `UPDATE app.cart_operations SET result=replace(result,$1,'20000000-0000-4000-8000-000000000099') WHERE operation_id=$2`
					args = []any{saved.Preview.ID.String(), p.OperationID().String()}
				case "result":
					sql = `UPDATE app.cart_operations SET result='{}' WHERE operation_id=$1`
					args = []any{p.OperationID().String()}
				case "estimate":
					sql = `UPDATE app.cart_summary_previews SET body=replace(body,'"numeric_estimate":null','"numeric_estimate":0')`
				case "reason":
					sql = `UPDATE app.cart_summary_previews SET body=replace(body,'workload_not_connected','invented_empty_kitchen')`
				case "text":
					sql = `UPDATE app.cart_summary_previews SET body=replace(body,'Would you like anything else?','Forged closing question?')`
				case "duplicate-unit":
					sql = `UPDATE app.cart_summary_previews SET body=jsonb_set(body::jsonb,'{units}',(body::jsonb->'units')||(body::jsonb->'units'))::text`
				case "source-plan":
					sql = `UPDATE app.cart_execution_calls SET canonical_intent='{}' WHERE operation_id=$1`
					args = []any{p.OperationID().String()}
				case "copies":
					// Keep the row's canonical body internally valid (name AND generated
					// text change together), but inconsistent with its ledger copy.
					sql = `UPDATE app.cart_summary_previews SET body=replace(body,'Fictional sandwich','different historical name')`
				}
				if _, e := f.conn.Exec(ctx, sql, args...); e != nil {
					t.Fatal(e)
				}
				previewRejected(t, f, p, f.job, want, b.Cart)
				previewRejected(t, f, f.plan(t, previewReadCall(t, saved.Preview.ID)), f.job, want, b.Cart)
				return
			}
			previewRejected(t, f, p, f.job, want, b.Cart)
		})
	}
}

func TestSummaryPreviewSimultaneousAndActualClaim(t *testing.T) {
	f := draftSetup(t, 650)
	s := previewSelected(t, f, 1, cart.PickupFulfillment())
	p := f.plan(t, previewCall(t, s.Cart))
	type outcome struct {
		r cart.SummaryPreviewResult
		e error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			r, e := f.s.ExecuteSummaryPreview(t.Context(), f.local, p, f.job)
			done <- outcome{r, e}
		}()
	}
	close(start)
	a, b := <-done, <-done
	if a.e != nil || b.e != nil || !reflect.DeepEqual(a.r, b.r) {
		t.Fatal(a, b)
	}
	before := previewState(t, f)
	current, e := f.s.Read(t.Context(), f.local, s.Cart.ID)
	if e != nil || !reflect.DeepEqual(current, s.Cart) || before != previewState(t, f) {
		t.Fatal(current, e)
	}
	for _, field := range []string{"id", "generation", "attempt", "event", "sender", "text"} {
		bad := f.job
		switch field {
		case "id":
			bad.ID = "20000000-0000-4000-8000-000000000099"
		case "generation":
			bad.Generation++
		case "attempt":
			bad.Attempt++
		case "event":
			bad.Event.ID = "wrong"
		case "sender":
			bad.Event.Sender = "wrong"
		case "text":
			bad.Event.Text = "wrong"
		}
		previewRejected(t, f, p, bad, foundation.ErrStaleLease, s.Cart)
	}
}

func TestSummaryPreviewBlockingAuthority(t *testing.T) {
	for _, resource := range []string{"cart", "settings", "catalog", "preview-insert", "result-insert", "cached", "read"} {
		for _, mode := range []string{"expiry", "cancel"} {
			t.Run(resource+"-"+mode, func(t *testing.T) {
				f := draftSetup(t, 650)
				ctx := t.Context()
				s := previewSelected(t, f, 1, delivery(t, "a", "p"))
				p := f.plan(t, previewCall(t, s.Cart))
				var saved cart.SummaryPreviewResult
				if resource == "cached" || resource == "read" {
					saved = previewRun(t, f, p)
					if resource == "read" {
						p = f.plan(t, previewReadCall(t, saved.Preview.ID))
					}
				}
				if _, e := f.conn.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '400 milliseconds' WHERE id=$1`, f.job.ID); e != nil {
					t.Fatal(e)
				}
				before := previewState(t, f)
				gate, e := pgx.Connect(ctx, f.dsn)
				if e != nil {
					t.Fatal(e)
				}
				defer gate.Close(ctx)
				tx, e := gate.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback(ctx)
				var sql, prefix string
				switch resource {
				case "cart":
					sql = `SELECT id FROM app.carts FOR UPDATE`
					prefix = "SELECT currency,exponent,version"
				case "settings":
					sql = `SELECT restaurant_id FROM app.restaurant_settings FOR UPDATE`
					prefix = "SELECT currency,currency_exponent,flat_fee_minor,version"
				case "catalog":
					sql = `SELECT id FROM app.menu_items FOR UPDATE`
					prefix = "SELECT name FROM app.menu_items"
				case "preview-insert":
					sql = `LOCK TABLE app.cart_summary_previews IN SHARE MODE`
					prefix = "INSERT INTO app.cart_summary_previews"
				case "result-insert":
					sql = `LOCK TABLE app.cart_operations IN SHARE MODE`
					prefix = "INSERT INTO app.cart_operations"
				case "cached":
					sql = `LOCK TABLE app.cart_operations IN ACCESS EXCLUSIVE MODE`
					prefix = "SELECT action,fingerprint,result"
				case "read":
					sql = `SELECT id FROM app.cart_summary_previews FOR UPDATE`
					prefix = "SELECT id::text,restaurant_id::text"
				}
				if _, e = tx.Exec(ctx, sql); e != nil {
					t.Fatal(e)
				}
				type outcome struct {
					r cart.SummaryPreviewResult
					e error
				}
				done := make(chan outcome, 1)
				live, cancel := context.WithCancel(ctx)
				defer cancel()
				go func() { r, e := f.s.ExecuteSummaryPreview(live, f.local, p, f.job); done <- outcome{r, e} }()
				executionWait(t, f.conn, prefix)
				want := foundation.ErrStaleLease
				if mode == "cancel" {
					cancel()
					want = cart.ErrUnavailable
				} else {
					executionExpired(t, f.conn, f.job)
				}
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				got := <-done
				if !errors.Is(got.e, want) || !reflect.DeepEqual(got.r, cart.SummaryPreviewResult{}) {
					t.Fatalf("blocked %+v %v want %v", got.r, got.e, want)
				}
				current, e := f.s.Read(ctx, f.local, s.Cart.ID)
				if e != nil || !reflect.DeepEqual(current, s.Cart) || before != previewState(t, f) {
					t.Fatalf("atomic rollback %+v %v", current, e)
				}
				retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
				if e != nil || !ok {
					t.Fatal(e)
				}
				r, e := f.s.ExecuteSummaryPreview(ctx, f.local, p, retry)
				if e != nil || r.OperationID != p.OperationID() || r.Preview.SourceCartVersion != s.Cart.Version {
					t.Fatalf("same identity retry %+v %v", r, e)
				}
				if resource == "cached" || resource == "read" {
					saved.OperationID = p.OperationID()
					if !reflect.DeepEqual(r, saved) {
						t.Fatal("history changed after blocked retry")
					}
				}
			})
		}
	}
}

func TestSummaryPreviewCaptureClockAfterLocks(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	s := previewSelected(t, f, 1, cart.PickupFulfillment())
	p := f.plan(t, previewCall(t, s.Cart))
	gate, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer gate.Close(ctx)
	tx, e := gate.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT id FROM app.menu_items FOR UPDATE`); e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		r cart.SummaryPreviewResult
		e error
	}
	done := make(chan outcome, 1)
	go func() { r, e := f.s.ExecuteSummaryPreview(ctx, f.local, p, f.job); done <- outcome{r, e} }()
	executionWait(t, f.conn, "SELECT name FROM app.menu_items")
	var released time.Time
	if e = f.conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&released); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.e != nil || got.r.Preview.CapturedAt.Before(released) {
		t.Fatalf("capture clock before resource acquisition %+v %v", got.r, got.e)
	}
}

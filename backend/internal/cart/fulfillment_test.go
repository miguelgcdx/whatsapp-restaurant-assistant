package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

type draftFixture struct {
	fixture
	jobs  *postgres.Store
	job   foundation.Job
	event string
	slot  int64
	conn  *pgx.Conn
}

func draftSetup(t *testing.T, price int64) *draftFixture {
	t.Helper()
	f := setupWithPrice(t, price)
	ctx := t.Context()
	jobs, e := postgres.Open(ctx, f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(jobs.Close)
	now := time.Now()
	event := foundation.Event{ID: "fulfillment", Sender: "fictional-sender", Text: "opaque"}
	if _, e = jobs.Accept(ctx, event, now); e != nil {
		t.Fatal(e)
	}
	job, ok, e := jobs.ClaimJob(ctx, now, time.Minute)
	if e != nil || !ok {
		t.Fatalf("claim %v %v", ok, e)
	}
	conn, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return &draftFixture{fixture: f, jobs: jobs, job: job, event: event.ID, conn: conn}
}
func (f *draftFixture) plan(t *testing.T, c cart.Call) cart.Execution {
	t.Helper()
	f.slot++
	p, e := f.s.RegisterExecution(t.Context(), f.local, f.event, f.slot, c)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func delivery(t *testing.T, address, phone string) cart.Fulfillment {
	t.Helper()
	f, e := cart.DeliveryFulfillment(address, phone)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func setCall(t *testing.T, s cart.Snapshot, f cart.Fulfillment) cart.Call {
	t.Helper()
	c, e := cart.SetFulfillmentCall(s.ID, s.Version, f)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func quoteCall(t *testing.T, s cart.Snapshot) cart.Call {
	t.Helper()
	c, e := cart.QuoteCall(s.ID)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *draftFixture) execute(t *testing.T, c cart.Call) cart.DraftResult {
	t.Helper()
	r, e := f.s.ExecuteDraft(t.Context(), f.local, f.plan(t, c), f.job)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *draftFixture) state(t *testing.T) string {
	t.Helper()
	var s string
	// Hashes supplement public cart readback and replay. Include setup/counters to
	// catch hidden writes on rejected calls as well as price/note/version changes.
	e := f.conn.QueryRow(t.Context(), `SELECT json_build_array((SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.carts r),(SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.cart_units r),(SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.cart_fulfillments r),(SELECT md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.cart_operations r),(SELECT count(*) FROM app.cart_execution_sources),(SELECT count(*) FROM app.cart_execution_calls),(SELECT count(*) FROM app.outbox),(SELECT count(*) FROM app.send_attempts))::text`).Scan(&s)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *draftFixture) rejected(t *testing.T, p cart.Execution, job foundation.Job, want error, expected cart.Snapshot) {
	t.Helper()
	before := f.state(t)
	r, e := f.s.ExecuteDraft(t.Context(), f.local, p, job)
	if !errors.Is(e, want) || !reflect.DeepEqual(r, cart.DraftResult{}) {
		t.Fatalf("rejected=%+v %v want %v", r, e, want)
	}
	got, e := f.s.Read(t.Context(), f.local, expected.ID)
	if e != nil || !reflect.DeepEqual(got, expected) || before != f.state(t) {
		t.Fatalf("rejection changed stored state %+v %v", got, e)
	}
}

func TestDraftFulfillmentValidation(t *testing.T) {
	for _, v := range []struct {
		name, address, phone string
		valid                bool
	}{
		{"exact", " \n🍲 street \t", " +fictional phone ", true},
		{"boundaries", strings.Repeat("🍲", 1000), strings.Repeat("☎", 128), true},
		{"one", "x", "y", true}, {"address-long", strings.Repeat("🍲", 1001), "p", false},
		{"phone-long", "a", strings.Repeat("☎", 129), false}, {"address-empty", "", "p", false},
		{"phone-empty", "a", "", false}, {"address-blank", " \t\n\u2003", "p", false}, {"phone-blank", "a", " \u2003", false},
		{"address-nul", "a\x00", "p", false}, {"phone-nul", "a", "p\x00", false},
		{"address-utf8", "\xff", "p", false}, {"phone-utf8", "a", "\xff", false},
	} {
		t.Run(v.name, func(t *testing.T) {
			got, e := cart.DeliveryFulfillment(v.address, v.phone)
			if v.valid {
				if e != nil || got.Kind() != "delivery" || got.Address() != v.address || got.RecipientPhone() != v.phone {
					t.Fatalf("exact value %+v %v", got, e)
				}
			} else if !errors.Is(e, cart.ErrInput) || got != (cart.Fulfillment{}) {
				t.Fatalf("invalid %+v %v", got, e)
			}
		})
	}
	id, _ := cart.ParseCartID("20000000-0000-4000-8000-000000000001")
	if _, e := cart.SetFulfillmentCall(id, version(t, 0), cart.Fulfillment{}); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e := cart.SetFulfillmentCall(cart.CartID{}, version(t, 0), cart.PickupFulfillment()); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	if _, e := cart.QuoteCall(cart.CartID{}); !errors.Is(e, cart.ErrInput) {
		t.Fatal(e)
	}
	p := cart.PickupFulfillment()
	if p.Kind() != "pickup" || p.Address() != "" || p.RecipientPhone() != "" {
		t.Fatal(p)
	}
}

func TestDraftSelectionQuoteReplayAndLegacyPreservation(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	original := create(t, f.fixture)
	unselected := f.plan(t, quoteCall(t, original.Cart))
	f.rejected(t, unselected, f.job, cart.ErrFulfillmentRequired, original.Cart)
	added := apply(t, f.fixture, original, add(t, 2))
	note, e := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, " exact 🍲\n")
	if e != nil {
		t.Fatal(e)
	}
	noted := apply(t, f.fixture, added, note)
	d := delivery(t, " exact fictional address\n", " exact fictional phone ")
	call := setCall(t, noted.Cart, d)
	plan := f.plan(t, call)
	before := f.state(t)
	old, e := f.s.Execute(ctx, f.local, plan, f.job)
	if !errors.Is(e, cart.ErrInput) || !reflect.DeepEqual(old, cart.Result{}) || before != f.state(t) {
		t.Fatalf("legacy accepted rich %+v %v", old, e)
	}
	selected, e := f.s.ExecuteDraft(ctx, f.local, plan, f.job)
	if e != nil {
		t.Fatal(e)
	}
	if selected.OperationID != plan.OperationID() || selected.Fulfillment != d || selected.Cart.Version.Int64() != 3 || selected.Cart.Subtotal != 1300 || selected.Quote.Subtotal != 1300 || selected.Quote.Fee != 300 || selected.Quote.Total != 1600 || selected.Quote.PaymentMethod != "cash_on_delivery" || selected.Quote.Currency != "USD" || selected.Quote.Exponent != 2 || selected.Quote.SettingsVersion != 0 || selected.Quote.ConfiguredDeliveryFee != 300 || !reflect.DeepEqual(selected.Cart.Units, noted.Cart.Units) {
		t.Fatalf("selected %+v", selected)
	}
	freshPlan := f.plan(t, quoteCall(t, selected.Cart))
	before = f.state(t)
	q, e := f.s.ExecuteDraft(ctx, f.local, freshPlan, f.job)
	if e != nil || !reflect.DeepEqual(q.Cart, selected.Cart) || q.Fulfillment != d || q.Quote != selected.Quote || before != f.state(t) {
		t.Fatalf("fresh %+v %v", q, e)
	}
	// Results are detached. Mutation of exported snapshots cannot change storage.
	selected.Cart.Units[0].Note = "caller modified"
	replay, e := f.s.ExecuteDraft(ctx, f.local, plan, f.job)
	if e != nil || !reflect.DeepEqual(replay.Cart.Units, noted.Cart.Units) {
		t.Fatalf("detached %+v %v", replay, e)
	}
	selected = replay
	f.rejected(t, f.plan(t, setCall(t, noted.Cart, cart.PickupFulfillment())), f.job, cart.ErrStale, selected.Cart)
	stale, e := f.s.Apply(ctx, f.local, operation(t), noted.Cart.ID, noted.Cart.Version, add(t, 1))
	if !errors.Is(e, cart.ErrStale) || !reflect.DeepEqual(stale, cart.Result{}) {
		t.Fatalf("shared CAS %+v %v", stale, e)
	}
	legacyPlan := f.plan(t, cart.CreateCall())
	f.rejected(t, legacyPlan, f.job, cart.ErrInput, selected.Cart)
	same := f.execute(t, setCall(t, selected.Cart, d))
	if same.Cart.Version.Int64() != 4 {
		t.Fatal(same)
	}
	pickup := f.execute(t, setCall(t, same.Cart, cart.PickupFulfillment()))
	if pickup.Quote.Fee != 0 || pickup.Quote.Total != 1300 || pickup.Quote.PaymentMethod != "pay_at_pickup" || pickup.Fulfillment.Address() != "" || !reflect.DeepEqual(pickup.Cart.Units, noted.Cart.Units) {
		t.Fatalf("switch %+v", pickup)
	}
	// Old direct edits preserve selection and exact unit identity, even if the menu
	// price and availability change after the price snapshot was taken.
	if _, e = f.conn.Exec(ctx, `UPDATE app.menu_items SET price_minor=999,available=false`); e != nil {
		t.Fatal(e)
	}
	edited := apply(t, f.fixture, cart.Result{OperationID: pickup.OperationID, Cart: pickup.Cart}, note)
	current := f.execute(t, quoteCall(t, edited.Cart))
	if current.Fulfillment != cart.PickupFulfillment() || current.Quote.Total != 1300 || !reflect.DeepEqual(current.Cart, edited.Cart) {
		t.Fatalf("legacy edit %+v", current)
	}
	newlyCreated, e := f.s.Create(ctx, f.local, operation(t))
	if e != nil || !reflect.DeepEqual(newlyCreated.Cart, edited.Cart) {
		t.Fatalf("fresh create %+v %v", newlyCreated, e)
	}
	oldCreate, e := f.s.Create(ctx, f.local, original.OperationID)
	if e != nil || !reflect.DeepEqual(oldCreate, original) {
		t.Fatalf("old bytes %+v %v", oldCreate, e)
	}
	// Both directions share the SAME operation namespace. Registered IDs are
	// retained, not reallocated, after a direct operation uses the identity first.
	if _, e = f.s.Apply(ctx, f.local, plan.OperationID(), edited.Cart.ID, edited.Cart.Version, note); !errors.Is(e, cart.ErrConflict) {
		t.Fatal(e)
	}
	collision := f.plan(t, setCall(t, edited.Cart, d))
	legacy, e := f.s.Create(ctx, f.local, collision.OperationID())
	if e != nil {
		t.Fatal(e)
	}
	f.rejected(t, collision, f.job, cart.ErrConflict, edited.Cart)
	legacyReplay, e := f.s.Create(ctx, f.local, collision.OperationID())
	if e != nil || !reflect.DeepEqual(legacyReplay, legacy) {
		t.Fatalf("legacy conflict %+v %v", legacyReplay, e)
	}
	// Fresh settings basis differs from immutable historical mutation result.
	if _, e = f.conn.Exec(ctx, `UPDATE app.restaurant_settings SET flat_fee_minor=0,version=version+1`); e != nil {
		t.Fatal(e)
	}
	delivered := f.execute(t, setCall(t, edited.Cart, d))
	if delivered.Quote.Fee != 0 || delivered.Quote.Total != 1300 || delivered.Quote.SettingsVersion != 1 || delivered.Quote.ConfiguredDeliveryFee != 0 {
		t.Fatal(delivered)
	}
	historical, e := f.s.ExecuteDraft(ctx, f.local, plan, f.job)
	if e != nil || !reflect.DeepEqual(historical, selected) {
		t.Fatalf("history %+v %v", historical, e)
	}
	quote := f.execute(t, quoteCall(t, delivered.Cart))
	if quote.Quote != delivered.Quote {
		t.Fatal(quote)
	}
	reopened, e := cart.OpenStore(ctx, f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	restored, e := reopened.LoadLocal(ctx, f.local.ConversationID(), "fictional-business", "fictional-sender")
	if e != nil {
		t.Fatal(e)
	}
	reopenedQuote, e := reopened.ExecuteDraft(ctx, restored, freshPlan, f.job)
	if e != nil || reopenedQuote.Fulfillment != d || !reflect.DeepEqual(reopenedQuote.Cart, delivered.Cart) {
		t.Fatalf("reopen %+v %v", reopenedQuote, e)
	}
}

func TestDraftEmptyMoneyAndStoredValidation(t *testing.T) {
	for _, scenario := range []string{"empty-pickup", "empty-delivery", "overflow", "bad-currency", "bad-exponent", "bad-version", "bad-fee", "bad-selection", "bad-result", "bad-fingerprint", "bad-plan", "result-budget"} {
		t.Run(scenario, func(t *testing.T) {
			price := int64(650)
			if scenario == "overflow" {
				price = math.MaxInt64
			}
			f := draftSetup(t, price)
			ctx := t.Context()
			base := create(t, f.fixture)
			if scenario == "overflow" {
				base = apply(t, f.fixture, base, add(t, 1))
			}
			if scenario == "result-budget" {
				// Owned valid stored units make the legacy result fit while the richer
				// result exceeds the same 512 KiB budget. No product quantity cap is inferred.
				base = apply(t, f.fixture, base, add(t, 1))
				n := strings.Repeat("🥪", 500)
				if _, e := f.conn.Exec(ctx, `UPDATE app.cart_units SET note=$1`, n); e != nil {
					t.Fatal(e)
				}
				sizing := base
				sizing.Cart.Units[0].Note = n
				unit := sizing.Cart.Units[0]
				count := 0
				for {
					sizing.Cart.Units = append(sizing.Cart.Units, unit)
					b, e := json.Marshal(sizing)
					if e != nil {
						t.Fatal(e)
					}
					if len(b) > 512*1024 {
						break
					}
					count++
				}
				if _, e := f.conn.Exec(ctx, `INSERT INTO app.cart_units(id,restaurant_id,conversation_id,cart_id,menu_id,position,price_minor,currency,exponent,note,created_at) SELECT gen_random_uuid(),restaurant_id,conversation_id,cart_id,menu_id,x,price_minor,currency,exponent,note,created_at FROM app.cart_units CROSS JOIN generate_series(1,$1::integer) x`, count); e != nil {
					t.Fatal(e)
				}
				s, e := f.s.Read(ctx, f.local, base.Cart.ID)
				if e != nil {
					t.Fatal(e)
				}
				base.Cart = s
			}
			choice := delivery(t, "a", "p")
			if scenario == "empty-pickup" {
				choice = cart.PickupFulfillment()
			}
			if scenario == "result-budget" {
				choice = delivery(t, strings.Repeat("🍲", 1000), strings.Repeat("☎", 128))
			}
			p := f.plan(t, setCall(t, base.Cart, choice))
			want := cart.ErrStoredData
			switch scenario {
			case "empty-pickup", "empty-delivery":
				r, e := f.s.ExecuteDraft(ctx, f.local, p, f.job)
				fee := int64(300)
				if scenario == "empty-pickup" {
					fee = 0
				}
				if e != nil || r.Quote.Subtotal != 0 || r.Quote.Total != fee || r.Quote.Fee != fee || len(r.Cart.Units) != 0 {
					t.Fatalf("empty %+v %v", r, e)
				}
				return
			case "overflow", "result-budget":
				want = cart.ErrLimit
			case "bad-currency":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.menu_items DROP CONSTRAINT menu_items_restaurant_id_currency_fkey; UPDATE app.restaurant_settings SET currency='EUR'`); e != nil {
					t.Fatal(e)
				}
			case "bad-exponent":
				if _, e := f.conn.Exec(ctx, `UPDATE app.restaurant_settings SET currency_exponent=3`); e != nil {
					t.Fatal(e)
				}
			case "bad-version":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.restaurant_settings DROP CONSTRAINT restaurant_settings_version_check; UPDATE app.restaurant_settings SET version=-1`); e != nil {
					t.Fatal(e)
				}
			case "bad-fee":
				if _, e := f.conn.Exec(ctx, `ALTER TABLE app.restaurant_settings DROP CONSTRAINT restaurant_settings_flat_fee_minor_check; UPDATE app.restaurant_settings SET flat_fee_minor=-1`); e != nil {
					t.Fatal(e)
				}
			case "bad-selection":
				selected, e := f.s.ExecuteDraft(ctx, f.local, p, f.job)
				if e != nil {
					t.Fatal(e)
				}
				base.Cart = selected.Cart
				p = f.plan(t, quoteCall(t, base.Cart))
				if _, e = f.conn.Exec(ctx, `ALTER TABLE app.cart_fulfillments DROP CONSTRAINT cart_fulfillments_selection_check; UPDATE app.cart_fulfillments SET kind='pickup'`); e != nil {
					t.Fatal(e)
				}
			case "bad-result":
				r, e := f.s.ExecuteDraft(ctx, f.local, p, f.job)
				if e != nil {
					t.Fatal(e)
				}
				base.Cart = r.Cart
				if _, e = f.conn.Exec(ctx, `UPDATE app.cart_operations SET result='{}' WHERE operation_id=$1`, p.OperationID().String()); e != nil {
					t.Fatal(e)
				}
			case "bad-fingerprint":
				if _, e := f.conn.Exec(ctx, `UPDATE app.cart_execution_calls SET fingerprint=decode(repeat('00',32),'hex')`); e != nil {
					t.Fatal(e)
				}
			case "bad-plan":
				if _, e := f.conn.Exec(ctx, `UPDATE app.cart_execution_calls SET canonical_intent='{}'`); e != nil {
					t.Fatal(e)
				}
			}
			f.rejected(t, p, f.job, want, base.Cart)
		})
	}
}

func TestDraftClaimBeforeCacheAndCAS(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	base := create(t, f.fixture)
	a := f.plan(t, setCall(t, base.Cart, cart.PickupFulfillment()))
	b := f.plan(t, setCall(t, base.Cart, delivery(t, "a", "p")))
	type result struct {
		r cart.DraftResult
		e error
	}
	done := make(chan result, 2)
	start := make(chan struct{})
	for _, p := range []cart.Execution{a, b} {
		go func() { <-start; r, e := f.s.ExecuteDraft(ctx, f.local, p, f.job); done <- result{r, e} }()
	}
	close(start)
	x, y := <-done, <-done
	if x.e != nil {
		x, y = y, x
	}
	if x.e != nil || !errors.Is(y.e, cart.ErrStale) || !reflect.DeepEqual(y.r, cart.DraftResult{}) || x.r.Cart.Version.Int64() != 1 {
		t.Fatalf("CAS %+v %+v", x, y)
	}
	winner := a
	if x.r.OperationID == b.OperationID() {
		winner = b
	}
	for _, field := range []string{"id", "generation", "attempt", "event", "sender", "text"} {
		bad := f.job
		switch field {
		case "id":
			bad.ID = "00000000-0000-4000-8000-000000000099"
		case "generation":
			bad.Generation++
		case "attempt":
			bad.Attempt++
		case "event":
			bad.Event.ID = "other"
		case "sender":
			bad.Event.Sender = "other"
		case "text":
			bad.Event.Text = "other"
		}
		f.rejected(t, winner, bad, foundation.ErrStaleLease, x.r.Cart)
	}
	retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
	if e != nil || !ok {
		t.Fatal(e)
	}
	f.rejected(t, winner, f.job, foundation.ErrStaleLease, x.r.Cart)
	r, e := f.s.ExecuteDraft(ctx, f.local, winner, retry)
	if e != nil || !reflect.DeepEqual(r, x.r) {
		t.Fatalf("reclaim %+v %v", r, e)
	}
	if e = f.jobs.CompleteJob(ctx, retry, "ack", time.Now().Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	f.rejected(t, winner, retry, foundation.ErrStaleLease, x.r.Cart)
}

func TestDraftBlockingAuthority(t *testing.T) {
	for _, scenario := range []string{"cart", "settings", "selection", "result", "cached", "quote-settings", "cancel-result"} {
		t.Run(scenario, func(t *testing.T) {
			f := draftSetup(t, 650)
			ctx := t.Context()
			base := create(t, f.fixture)
			plan := f.plan(t, setCall(t, base.Cart, delivery(t, "a", "p")))
			expected := base.Cart
			var saved cart.DraftResult
			if scenario == "cached" || scenario == "quote-settings" || scenario == "selection" {
				var e error
				saved, e = f.s.ExecuteDraft(ctx, f.local, plan, f.job)
				if e != nil {
					t.Fatal(e)
				}
				expected = saved.Cart
				if scenario != "cached" {
					plan = f.plan(t, quoteCall(t, expected))
				}
			}
			before := f.state(t)
			if _, e := f.conn.Exec(ctx, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '400 milliseconds' WHERE id=$1`, f.job.ID); e != nil {
				t.Fatal(e)
			}
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
			switch scenario {
			case "cart":
				sql = `SELECT id FROM app.carts FOR UPDATE`
				prefix = "SELECT currency,exponent,version"
			case "settings", "quote-settings":
				sql = `SELECT restaurant_id FROM app.restaurant_settings FOR UPDATE`
				prefix = "SELECT currency,currency_exponent,flat_fee_minor,version"
			case "selection":
				sql = `SELECT cart_id FROM app.cart_fulfillments FOR UPDATE`
				prefix = "SELECT kind,address,recipient_phone"
			case "cached":
				sql = `LOCK TABLE app.cart_operations IN ACCESS EXCLUSIVE MODE`
				prefix = "SELECT action,fingerprint,result"
			default:
				sql = `LOCK TABLE app.cart_operations IN SHARE MODE`
				prefix = "INSERT INTO app.cart_operations"
			}
			if _, e = tx.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			type outcome struct {
				r cart.DraftResult
				e error
			}
			done := make(chan outcome, 1)
			live, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() { r, e := f.s.ExecuteDraft(live, f.local, plan, f.job); done <- outcome{r, e} }()
			executionWait(t, f.conn, prefix)
			want := foundation.ErrStaleLease
			if scenario == "cancel-result" {
				cancel()
				want = cart.ErrUnavailable
			} else {
				executionExpired(t, f.conn, f.job)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if !errors.Is(got.e, want) || !reflect.DeepEqual(got.r, cart.DraftResult{}) {
				t.Fatalf("blocked %+v %v", got.r, got.e)
			}
			read, e := f.s.Read(ctx, f.local, expected.ID)
			if e != nil || !reflect.DeepEqual(read, expected) || before != f.state(t) {
				t.Fatalf("rollback %+v %v", read, e)
			}
			retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
			if e != nil || !ok {
				t.Fatal(e)
			}
			r, e := f.s.ExecuteDraft(ctx, f.local, plan, retry)
			if e != nil || r.OperationID != plan.OperationID() || r.Cart.Version.Int64() != 1 || r.Fulfillment.Kind() != "delivery" {
				t.Fatalf("retry %+v %v", r, e)
			}
			if scenario == "cached" && !reflect.DeepEqual(r, saved) {
				t.Fatal("cached retry changed")
			}
			state, e := f.jobs.Inspect(ctx, f.event)
			if e != nil || state.OutboxCount != 0 || state.JobAttempt != 2 {
				t.Fatalf("foundation %+v %v", state, e)
			}
		})
	}
}

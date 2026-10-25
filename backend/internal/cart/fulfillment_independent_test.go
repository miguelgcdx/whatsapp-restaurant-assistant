package cart_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/cart"
)

// S2/S3/S5, L7-L9: the same read plan samples new settings, never old cache.
func TestIndependentDraftFreshSettings(t *testing.T) {
	f := draftSetup(t, 650)
	base := apply(t, f.fixture, create(t, f.fixture), add(t, 2))
	selection := f.plan(t, setCall(t, base.Cart,
		delivery(t, " exact address\n", " exact phone ")))
	historical, err := f.s.ExecuteDraft(t.Context(), f.local, selection, f.job)
	if err != nil {
		t.Fatal(err)
	}
	plan := f.plan(t, quoteCall(t, historical.Cart))
	before := f.state(t)
	first, err := f.s.ExecuteDraft(t.Context(), f.local, plan, f.job)
	if err != nil || first.Quote != historical.Quote || before != f.state(t) {
		t.Fatalf("initial quote: %+v %v", first, err)
	}
	if _, err = f.conn.Exec(t.Context(),
		`UPDATE app.restaurant_settings SET flat_fee_minor=777,version=version+1 WHERE restaurant_id=$1`,
		restaurant); err != nil {
		t.Fatal(err)
	}
	before = f.state(t)
	fresh, err := f.s.ExecuteDraft(t.Context(), f.local, plan, f.job)
	if err != nil ||
		!reflect.DeepEqual(fresh.Cart, historical.Cart) ||
		fresh.Fulfillment != historical.Fulfillment ||
		fresh.OperationID != plan.OperationID() ||
		fresh.Quote.Subtotal != 1300 || fresh.Quote.Fee != 777 ||
		fresh.Quote.Total != 2077 ||
		fresh.Quote.ConfiguredDeliveryFee != 777 ||
		fresh.Quote.SettingsVersion != 1 ||
		fresh.Quote.PaymentMethod != "cash_on_delivery" ||
		fresh.Quote.Currency != "USD" || fresh.Quote.Exponent != 2 ||
		before != f.state(t) {
		t.Fatalf("fresh quote: %+v %v", fresh, err)
	}
	replay, err := f.s.ExecuteDraft(t.Context(), f.local, selection, f.job)
	if err != nil || !reflect.DeepEqual(replay, historical) || before != f.state(t) {
		t.Fatalf("historical quote: %+v %v", replay, err)
	}
}

// S4, L6-L8: replay parses the entire canonical variant and derived money.
func TestIndependentDraftRichVariantTampering(t *testing.T) {
	f := draftSetup(t, 650)
	base := apply(t, f.fixture, create(t, f.fixture), add(t, 1))
	plan := f.plan(t, setCall(t, base.Cart, cart.PickupFulfillment()))
	original, err := f.s.ExecuteDraft(t.Context(), f.local, plan, f.job)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err = f.conn.QueryRow(t.Context(),
		`SELECT result FROM app.cart_operations WHERE operation_id=$1`,
		plan.OperationID().String()).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"empty-address-present",
			`"kind":"pickup","payment_method":"pay_at_pickup"`,
			`"kind":"pickup","payment_method":"pay_at_pickup","address":""`},
		{"phone-present",
			`"kind":"pickup","payment_method":"pay_at_pickup"`,
			`"kind":"pickup","payment_method":"pay_at_pickup","recipient_phone":"p"`},
		{"wrong-variant-payment",
			`"kind":"pickup","payment_method":"pay_at_pickup"`,
			`"kind":"pickup","payment_method":"cash_on_delivery"`},
		{"missing-delivery-fields",
			`"kind":"pickup","payment_method":"pay_at_pickup"`,
			`"kind":"delivery","payment_method":"cash_on_delivery"`},
		{"unknown-variant-field",
			`"kind":"pickup","payment_method":"pay_at_pickup"`,
			`"kind":"pickup","payment_method":"pay_at_pickup","extra":true`},
		{"duplicate-kind", `"kind":"pickup"`,
			`"kind":"pickup","kind":"pickup"`},
		{"wrong-quote-payment",
			`"payment_method":"pay_at_pickup","settings_version"`,
			`"payment_method":"cash_on_delivery","settings_version"`},
		{"pickup-fee", `"fee":0,"total":650`, `"fee":1,"total":651`},
		{"quote-currency", `"quote":{"currency":"USD"`,
			`"quote":{"currency":"EUR"`},
		{"negative-fee-basis", `"configured_delivery_fee":300`,
			`"configured_delivery_fee":-1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(body, tc.from) != 1 {
				t.Fatalf("nonunique fixture target %q", tc.from)
			}
			bad := strings.Replace(body, tc.from, tc.to, 1)
			if _, err := f.conn.Exec(t.Context(),
				`UPDATE app.cart_operations SET result=$1 WHERE operation_id=$2`,
				bad, plan.OperationID().String()); err != nil {
				t.Fatal(err)
			}
			before := f.state(t)
			got, err := f.s.ExecuteDraft(t.Context(), f.local, plan, f.job)
			if !errors.Is(err, cart.ErrStoredData) ||
				err.Error() != "invalid stored cart data" ||
				!reflect.DeepEqual(got, cart.DraftResult{}) {
				t.Fatalf("bad replay: %+v %v", got, err)
			}
			read, err := f.s.Read(t.Context(), f.local, original.Cart.ID)
			if err != nil || !reflect.DeepEqual(read, original.Cart) ||
				before != f.state(t) {
				t.Fatalf("rejection changed state: %+v %v", read, err)
			}
		})
		if t.Failed() {
			return
		}
	}
	if _, err = f.conn.Exec(t.Context(),
		`UPDATE app.cart_operations SET result=$1 WHERE operation_id=$2`,
		body, plan.OperationID().String()); err != nil {
		t.Fatal(err)
	}
	before := f.state(t)
	replay, err := f.s.ExecuteDraft(t.Context(), f.local, plan, f.job)
	if err != nil || !reflect.DeepEqual(replay, original) ||
		before != f.state(t) {
		t.Fatalf("valid replay: %+v %v", replay, err)
	}
}

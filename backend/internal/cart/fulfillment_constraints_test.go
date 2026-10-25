package cart_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/cart"
)

// The schema is a second persistence boundary; SQL fixtures supplement the
// public constructor checks, readback and unselected-quote behavior.
func TestDraftSchemaRejectsUnicodeBlankSelection(t *testing.T) {
	f := draftSetup(t, 650)
	base := create(t, f.fixture)
	q := f.plan(t, quoteCall(t, base.Cart))
	before := f.state(t)
	for _, blank := range []string{" \t\n", "\u0085", "\u00a0", "\u1680", "\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a", "\u2028\u2029", "\u202f\u205f\u3000"} {
		for _, field := range []string{"address", "phone"} {
			address, phone := blank, "p"
			if field == "phone" {
				address, phone = "a", blank
			}
			_, e := f.conn.Exec(t.Context(), `INSERT INTO app.cart_fulfillments(restaurant_id,conversation_id,cart_id,kind,address,recipient_phone) VALUES($1,$2,$3,'delivery',$4,$5)`, restaurant, f.local.ConversationID().String(), base.Cart.ID.String(), address, phone)
			if e == nil {
				t.Fatalf("schema accepted blank %s %q", field, blank)
			}
			r, e := f.s.ExecuteDraft(t.Context(), f.local, q, f.job)
			if !errors.Is(e, cart.ErrFulfillmentRequired) || !reflect.DeepEqual(r, cart.DraftResult{}) || before != f.state(t) {
				t.Fatalf("schema rejection changed state %+v %v", r, e)
			}
		}
	}
	// Exact Unicode codepoint endpoints survive persistence, not just construction.
	selected := f.execute(t, setCall(t, base.Cart, delivery(t, strings.Repeat("🍲", 1000), strings.Repeat("☎", 128))))
	read := f.execute(t, quoteCall(t, selected.Cart))
	if read.Fulfillment != selected.Fulfillment || !reflect.DeepEqual(read.Cart, selected.Cart) || read.Quote != selected.Quote {
		t.Fatal("boundary read changed")
	}
}

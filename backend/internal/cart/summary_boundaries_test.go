package cart_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/foundation"
)

// Supplemental GREEN-only endpoint/schema probes, not initial behavior RED.
func TestSummaryPreviewSchemaScopeAndTextBoundaries(t *testing.T) {
	f := draftSetup(t, 650)
	ctx := t.Context()
	s := previewSelected(t, f, 1, cart.PickupFulfillment())
	name := strings.Repeat("🥪", 200)
	note := strings.Repeat("🍲", 500)
	if _, e := f.conn.Exec(ctx, `UPDATE app.menu_items SET name=$1`, name); e != nil {
		t.Fatal(e)
	}
	action, e := cart.SetNote([]cart.UnitID{s.Cart.Units[0].ID}, note)
	if e != nil {
		t.Fatal(e)
	}
	changed := apply(t, f.fixture, cart.Result{OperationID: s.OperationID, Cart: s.Cart}, action)
	p := f.plan(t, previewCall(t, changed.Cart))
	saved := previewRun(t, f, p)
	if saved.Preview.Units[0].Name != name || saved.Preview.Units[0].Note != note || saved.Preview.Quote.Total != 650 || saved.Preview.UnitCount != 1 {
		t.Fatal(saved)
	}
	other, e := f.s.ProvisionLocal(ctx, "other", "other")
	if e != nil {
		t.Fatal(e)
	}
	foreign := create(t, fixture{s: f.s, local: other})
	event := foundation.Event{ID: "other-plan", Sender: "other", Text: "opaque"}
	if _, e = f.jobs.Accept(ctx, event, time.Now()); e != nil {
		t.Fatal(e)
	}
	foreignPlan, e := f.s.RegisterExecution(ctx, other, event.ID, 0, previewCall(t, foreign.Cart))
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, sql, state string
		args             []any
	}{
		{"scoped-cart", `UPDATE app.cart_summary_previews SET cart_id=$1`, "23503", []any{foreign.Cart.ID.String()}},
		{"scoped-source", `UPDATE app.cart_summary_previews SET source_operation_id=$1`, "23503", []any{foreignPlan.OperationID().String()}},
		{"one-per-operation", `INSERT INTO app.cart_summary_previews SELECT gen_random_uuid(),restaurant_id,conversation_id,cart_id,source_cart_version,source_operation_id,captured_at,body FROM app.cart_summary_previews`, "23505", nil},
		{"version", `UPDATE app.cart_summary_previews SET source_cart_version=-1`, "23514", nil},
		{"body-size", `UPDATE app.cart_summary_previews SET body=repeat(' ',524288)||'{}'`, "23514", nil},
		{"body-object", `UPDATE app.cart_summary_previews SET body='[]'`, "23514", nil},
		{"explicit-clock", `UPDATE app.cart_summary_previews SET captured_at=NULL`, "23502", nil},
		{"no-read-mutation", `UPDATE app.cart_operations SET action='read_preview' WHERE operation_id=$1`, "23514", []any{p.OperationID().String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := previewState(t, f)
			_, e := f.conn.Exec(ctx, tc.sql, tc.args...)
			var pg *pgconn.PgError
			if !errors.As(e, &pg) || pg.Code != tc.state {
				t.Fatalf("constraint err=%v want %s", e, tc.state)
			}
			if before != previewState(t, f) {
				t.Fatal("rejected fixture constraint changed rows")
			}
			r := previewRun(t, f, p)
			if !reflect.DeepEqual(r, saved) {
				t.Fatal("constraint changed replay")
			}
			current, e := f.s.Read(ctx, f.local, changed.Cart.ID)
			if e != nil || !reflect.DeepEqual(current, changed.Cart) {
				t.Fatal(current, e)
			}
		})
	}
}

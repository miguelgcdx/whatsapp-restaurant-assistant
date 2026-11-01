package stafforders_test

import (
	"reflect"
	"testing"

	"restaurant-assistant/backend/internal/cart"
)

func TestPickupCapabilitiesProjection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actions []cart.PickupStaffAction
	}{
		{"subset_server_order", []cart.PickupStaffAction{cart.PickupMarkPaid, cart.PickupAccept, cart.PickupCancel}},
		{"all_seven_server_order", []cart.PickupStaffAction{cart.PickupMarkPaid, cart.PickupCancel, cart.PickupMarkPickedUp, cart.PickupMarkReady, cart.PickupStartPreparing, cart.PickupReject, cart.PickupAccept}},
		{"empty", []cart.PickupStaffAction{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, h, token := setup(t)
			p.order.PermittedActions = tc.actions
			before := fixtureOrder(t)
			before.PermittedActions = append([]cart.PickupStaffAction{}, tc.actions...)
			want := expectedDetail()
			values := make([]any, len(tc.actions))
			for i, a := range tc.actions {
				values[i] = string(a)
			}
			want["permitted_actions"] = values
			// Empty hints do not block POST; a subset need not contain the request.
			body := `{"action":"reject","expected_version":"3"}`
			for _, req := range []struct{ method, path, body string }{{"GET", detail, ""}, {"POST", action, body}, {"POST", action, body}} {
				calls := p.calls
				obj := response(t, request(h, req.method, req.path, req.body, token, nil), 200, "")
				if !reflect.DeepEqual(obj["data"], want) {
					t.Errorf("%s detail = %+v; want %+v", req.method, obj["data"], want)
				}
				if p.calls != calls+1 || p.subject != "verified-alex" || p.orderID.String() != oid || !reflect.DeepEqual(p.order, before) {
					t.Fatal("projection changed port data or bypassed current port call")
				}
				if req.method == "POST" && (p.intent.Action != cart.PickupReject || p.intent.ExpectedVersion.Int64() != 3 || p.intent.IdempotencyKey != "intent") {
					t.Fatal(p.intent)
				}
			}
			// Headers neither acquire capability hints nor confirmed identity.
			obj := response(t, request(h, "GET", root, "", token, nil), 200, "")
			if !reflect.DeepEqual(obj["data"], map[string]any{"items": []any{header()}, "next_cursor": nil}) {
				t.Fatal(obj)
			}
			// Hints cannot override the port's current authorization or transition result.
			for _, denial := range []struct {
				err    error
				status int
				code   string
			}{{cart.ErrPickupForbidden, 403, "forbidden"}, {cart.ErrPickupInvalidTransition, 409, "invalid_transition"}} {
				p.err = denial.err
				calls := p.calls
				response(t, request(h, "POST", action, body, token, nil), denial.status, denial.code)
				if p.calls != calls+1 || !reflect.DeepEqual(p.order, before) {
					t.Fatal("denial changed port data or skipped port")
				}
			}
		})
	}
}

func TestPickupCapabilitiesInvalidOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actions []cart.PickupStaffAction
	}{
		{"nil", nil},
		{"duplicate", []cart.PickupStaffAction{cart.PickupAccept, cart.PickupReject, cart.PickupAccept}},
		{"unknown", []cart.PickupStaffAction{cart.PickupAccept, "private-unknown-action"}},
		{"empty_code", []cart.PickupStaffAction{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, h, token := setup(t)
			p.order.PermittedActions = tc.actions
			before := fixtureOrder(t)
			if tc.actions == nil {
				before.PermittedActions = nil
			} else {
				before.PermittedActions = append([]cart.PickupStaffAction{}, tc.actions...)
			}
			for _, req := range []struct{ method, path, body string }{{"GET", detail, ""}, {"POST", action, `{"action":"accept","expected_version":"3"}`}} {
				calls := p.calls
				t.Run(req.method, func(t *testing.T) {
					response(t, request(h, req.method, req.path, req.body, token, nil), 500, "internal_error")
				})
				if p.calls != calls+1 || p.subject != "verified-alex" || p.orderID.String() != oid || !reflect.DeepEqual(p.order, before) {
					t.Fatal("projection failure changed fake data or skipped port")
				}
			}
		})
	}
}

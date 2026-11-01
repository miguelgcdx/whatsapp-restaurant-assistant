package stafforders_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/stafforders"
)

const oid = "10000000-0000-4000-8000-000000000001"
const sid = "20000000-0000-4000-8000-000000000002"
const uid = "30000000-0000-4000-8000-000000000003"
const mid = "40000000-0000-4000-8000-000000000004"
const root = "/v1/staff/orders"
const detail = root + "/" + oid
const units = detail + "/summaries/" + sid + "/units"
const action = detail + "/actions"

type fakePort struct {
	calls     int
	subject   cart.PickupStaffSubject
	orderID   cart.PickupOrderID
	summaryID cart.PickupCandidateID
	page      cart.PickupPageRequest
	intent    cart.PickupStaffActionRequest
	order     cart.PickupOrder
	list      cart.PickupOrderPage
	units     cart.PickupOrderUnitPage
	err       error
}

func (p *fakePort) call(ctx context.Context, sub cart.PickupStaffSubject) {
	p.calls++
	p.subject = sub
	if ctx == nil {
		panic("nil context")
	}
}
func (p *fakePort) ListOrders(ctx context.Context, s cart.PickupStaffSubject, q cart.PickupPageRequest) (cart.PickupOrderPage, error) {
	p.call(ctx, s)
	p.page = q
	return p.list, p.err
}
func (p *fakePort) ReadOrder(ctx context.Context, s cart.PickupStaffSubject, id cart.PickupOrderID) (cart.PickupOrder, error) {
	p.call(ctx, s)
	p.orderID = id
	return p.order, p.err
}
func (p *fakePort) ReadUnits(ctx context.Context, s cart.PickupStaffSubject, id cart.PickupOrderID, sum cart.PickupCandidateID, q cart.PickupPageRequest) (cart.PickupOrderUnitPage, error) {
	p.call(ctx, s)
	p.orderID = id
	p.summaryID = sum
	p.page = q
	return p.units, p.err
}
func (p *fakePort) ApplyAction(ctx context.Context, s cart.PickupStaffSubject, id cart.PickupOrderID, a cart.PickupStaffActionRequest) (cart.PickupOrder, error) {
	p.call(ctx, s)
	p.orderID = id
	p.intent = a
	return p.order, p.err
}
func parse[T interface{ UnmarshalText([]byte) error }](t *testing.T, v T, s string) {
	t.Helper()
	if err := v.UnmarshalText([]byte(s)); err != nil {
		t.Fatal(err)
	}
}
func fixtureOrder(t *testing.T) cart.PickupOrder {
	var id cart.PickupOrderID
	parse(t, &id, oid)
	var sum cart.PickupCandidateID
	parse(t, &sum, sid)
	v, _ := cart.ParseVersion(9223372036854775807)
	m := cart.PickupMoney{AmountMinor: 12345, Currency: "KWD", Exponent: 3}
	return cart.PickupOrder{PickupOrderHeader: cart.PickupOrderHeader{ID: id, Version: v, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600)), Status: cart.PickupSubmitted, Payment: cart.PickupPayment{State: cart.PickupUnpaid}, Total: m}, Confirmed: cart.PickupConfirmedSnapshot{ID: sum, UnitCount: 1, Subtotal: m, DeliveryFee: cart.PickupMoney{Currency: "KWD", Exponent: 3}, Total: m}, PermittedActions: []cart.PickupStaffAction{}}
}
func setup(t *testing.T) (*fakePort, http.Handler, string) {
	t.Helper()
	p := &fakePort{order: fixtureOrder(t)}
	p.list.Items = []cart.PickupOrderHeader{p.order.PickupOrderHeader}
	var u cart.UnitID
	parse(t, &u, uid)
	var m cart.MenuID
	parse(t, &m, mid)
	p.units = cart.PickupOrderUnitPage{Items: []cart.PickupOrderUnit{{ID: u, MenuID: m, Name: "料理 <&>", Note: "  no onions\nplease  ", Price: p.order.Total}}, SummaryID: p.order.Confirmed.ID, UnitCount: 1}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":%q}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(keys.Close)
	v, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "sub": "verified-alex", "iat": time.Now().Unix(), "exp": time.Now().Add(59 * time.Second).Unix()})
	token.Header["kid"] = "test"
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	h, err := stafforders.NewHandler(p, v)
	if err != nil {
		t.Fatal(err)
	}
	return p, h, raw
}
func request(h http.Handler, method, path, body, token string, headers map[string][]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "intent")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func response(t *testing.T, w *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" || w.Body.Len() > 512*1024 {
		t.Fatalf("response %d %v %s; want %d", w.Code, w.Header(), w.Body.String(), status)
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	id, ok := obj["request_id"].(string)
	if !ok || len(id) != 32 || len(obj) != 2 {
		t.Fatalf("envelope %+v", obj)
	}
	if status != 200 {
		messages := map[string]string{"invalid_input": "Invalid request.", "unauthenticated": "Authentication is required.", "forbidden": "Staff access is not available.", "not_found": "Order resource was not found.", "version_conflict": "Refresh this order before trying again.", "idempotency_conflict": "Idempotency key was already used for a different request.", "invalid_transition": "The order action is not available.", "policy_unresolved": "This order action requires an unresolved policy.", "temporarily_unavailable": "Orders are temporarily unavailable.", "internal_error": "Orders could not be completed.", "unsupported_media_type": "Content-Type must be application/json.", "payload_too_large": "Request body is too large.", "method_not_allowed": "The method is not allowed."}
		want := map[string]any{"code": code, "message": messages[code]}
		if !reflect.DeepEqual(obj["error"], want) {
			t.Fatalf("error %+v want %+v", obj, want)
		}
	}
	return obj
}
func money() map[string]any {
	return map[string]any{"amount_minor": "12345", "currency": "KWD", "exponent": float64(3)}
}
func header() map[string]any {
	return map[string]any{"id": oid, "version": "9223372036854775807", "created_at": "2026-01-02T02:04:05Z", "fulfillment_status": "submitted", "payment_status": "unpaid", "fulfillment_kind": "pickup", "total": money()}
}
func expectedDetail() map[string]any {
	d := header()
	d["confirmed"] = map[string]any{"id": sid, "unit_count": float64(1), "units_href": "/api/staff/orders/" + oid + "/summaries/" + sid + "/units", "fulfillment": map[string]any{"kind": "pickup", "payment_method": "pay_at_pickup"}, "subtotal": money(), "delivery_fee": map[string]any{"amount_minor": "0", "currency": "KWD", "exponent": float64(3)}, "total": money(), "fee_basis": "independent", "fee_source_order_id": nil}
	d["payment"] = map[string]any{"state": "unpaid", "collected_at": nil}
	d["estimate"] = nil
	d["preparation_started_at"] = nil
	d["permitted_actions"] = []any{}
	return d
}
func TestRoutesAndProjection(t *testing.T) {
	p, h, token := setup(t)
	for _, path := range []string{root, detail, units} {
		obj := response(t, request(h, "GET", path, "", token, map[string][]string{"X-Auth-User-Id": {"forged"}, "X-Restaurant-Id": {"forged"}, "X-Role": {"owner"}}), 200, "")
		var want any
		switch path {
		case root:
			want = map[string]any{"items": []any{header()}, "next_cursor": nil}
			if p.page.Limit != 50 {
				t.Fatal(p.page)
			}
		case detail:
			want = expectedDetail()
			if p.orderID.String() != oid {
				t.Fatal(p.orderID)
			}
		case units:
			want = map[string]any{"items": []any{map[string]any{"id": uid, "menu_item_id": mid, "name": "料理 <&>", "note": "  no onions\nplease  ", "unit_price": money()}}, "summary_id": sid, "unit_count": float64(1), "next_cursor": nil}
			if p.summaryID.String() != sid || p.page.Limit != 50 {
				t.Fatal(p.summaryID, p.page)
			}
		}
		if !reflect.DeepEqual(obj["data"], want) {
			t.Fatalf("%s data %+v want %+v", path, obj["data"], want)
		}
		if p.subject != "verified-alex" {
			t.Fatal(p.subject)
		}
	}
	for _, path := range []string{root, units} {
		response(t, request(h, "GET", path+"?limit=100&cursor=opaque.signed", "", token, nil), 200, "")
		if p.page != (cart.PickupPageRequest{Limit: 100, Cursor: "opaque.signed"}) {
			t.Fatal(p.page)
		}
	}
	for _, a := range []cart.PickupStaffAction{cart.PickupAccept, cart.PickupReject, cart.PickupStartPreparing, cart.PickupMarkReady, cart.PickupMarkPickedUp, cart.PickupCancel, cart.PickupMarkPaid} {
		first := response(t, request(h, "POST", action, `{"action":"`+string(a)+`","expected_version":"3"}`, token, nil), 200, "")
		second := response(t, request(h, "POST", action, ` { "expected_version" : "3", "action" : "`+string(a)+`" } `, token, nil), 200, "")
		if !reflect.DeepEqual(first["data"], expectedDetail()) || !reflect.DeepEqual(first["data"], second["data"]) {
			t.Fatal("replay projection changed")
		}
		if p.intent.Action != a || p.intent.ExpectedVersion.Int64() != 3 || p.intent.IdempotencyKey != "intent" || p.subject != "verified-alex" || p.orderID.String() != oid {
			t.Fatal(p.intent)
		}
	}
	if p.calls != 19 {
		t.Fatal(p.calls)
	}
	// Nullable metadata and UTC conversion are deliberate, not zero placeholders.
	stamp := time.Date(2026, 2, 3, 4, 5, 6, 0, time.FixedZone("east", 3600))
	p.order.Payment = cart.PickupPayment{State: cart.PickupPaid, CollectedAt: &stamp}
	p.order.Status = cart.PickupPreparing
	p.order.PreparationStartedAt = &stamp
	p.order.Estimate = &cart.PickupEstimate{Minutes: 10, AsOf: stamp, Provisional: false}
	d := response(t, request(h, "GET", detail, "", token, nil), 200, "")["data"].(map[string]any)
	if !reflect.DeepEqual(d["payment"], map[string]any{"state": "paid", "collected_at": "2026-02-03T03:05:06Z"}) || !reflect.DeepEqual(d["estimate"], map[string]any{"minutes": float64(10), "as_of": "2026-02-03T03:05:06Z", "provisional": false}) || d["preparation_started_at"] != "2026-02-03T03:05:06Z" {
		t.Fatal(d)
	}
	p.list = cart.PickupOrderPage{}
	d = response(t, request(h, "GET", root, "", token, nil), 200, "")["data"].(map[string]any)
	if !reflect.DeepEqual(d, map[string]any{"items": []any{}, "next_cursor": nil}) {
		t.Fatal(d)
	}
}
func TestRejectedWireHasNoPortEffects(t *testing.T) {
	p, h, token := setup(t)
	type tc struct {
		name, method, path, body string
		headers                  map[string][]string
		status                   int
		code                     string
	}
	cases := []tc{}
	for _, body := range []string{`{}`, `null`, `[]`, `{"action":"accept"}`, `{"expected_version":"0"}`, `{"action":null,"expected_version":"0"}`, `{"action":3,"expected_version":"0"}`, `{"action":"set_status","expected_version":"0"}`, `{"action":"mark_delivered","expected_version":"0"}`, `{"action":"accept","expected_version":null}`, `{"action":"accept","expected_version":0}`, `{"action":"accept","expected_version":"00"}`, `{"action":"accept","expected_version":"-1"}`, `{"action":"accept","expected_version":"+1"}`, `{"action":"accept","expected_version":"1.0"}`, `{"action":"accept","expected_version":"9223372036854775808"}`, `{"action":"accept","expected_version":"0","status":"paid"}`, `{"action":"accept","expected_version":"0","subject":"forged"}`, `{"action":"accept","expected_version":"0","action":"reject"}`, `{"action":"accept","expected_version":"0","\u0061ction":"reject"}`, `{"action":"accept","expected_version":"0"} {}`, "{\"action\":\"\xff\",\"expected_version\":\"0\"}"} {
		cases = append(cases, tc{name: body, method: "POST", path: action, body: body})
	}
	good := `{"action":"accept","expected_version":"0"}`
	for _, key := range [][]string{nil, {""}, {"a", "b"}, {"a b"}, {"a\t"}, {"é"}, {strings.Repeat("x", 129)}} {
		cases = append(cases, tc{name: fmt.Sprint(key), method: "POST", path: action, body: good, headers: map[string][]string{"Idempotency-Key": key}})
	}
	for _, media := range [][]string{nil, {"text/plain"}, {"application/json; charset=latin1"}, {"application/json; x=y"}, {"application/json", "application/json"}} {
		cases = append(cases, tc{name: fmt.Sprint(media), method: "POST", path: action, body: good, headers: map[string][]string{"Content-Type": media}, status: 415, code: "unsupported_media_type"})
	}
	cases = append(cases, tc{name: "oversize", method: "POST", path: action, body: strings.Repeat(" ", 65537), status: 413, code: "payload_too_large"})
	for _, path := range []string{root, units} {
		for _, q := range []string{"?", "?limit=0", "?limit=101", "?limit=-1", "?limit=1&limit=2", "?other=x", "?cursor=", "?cursor=a&cursor=b", "?cursor=%ff", "?cursor=" + strings.Repeat("x", 2049), "?limit=%zz", "?limit=1;cursor=x"} {
			cases = append(cases, tc{name: path + q, method: "GET", path: path + q})
		}
	}
	for _, path := range []string{detail + "?", detail + "?limit=1", action + "?x=1", strings.Replace(detail, "-4000-", "-1000-", 1), strings.Replace(units, sid, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", 1)} {
		method := "GET"
		if strings.Contains(path, "actions") {
			method = "POST"
		}
		cases = append(cases, tc{name: path, method: method, path: path, body: map[bool]string{true: good}[method == "POST"]})
	}
	for _, path := range []string{root, detail, units} {
		cases = append(cases, tc{name: "get body", method: "GET", path: path, body: "x"})
		for _, framing := range []string{"Content-Length", "Transfer-Encoding", "Trailer"} {
			cases = append(cases, tc{name: framing, method: "GET", path: path, headers: map[string][]string{framing: {"0"}}})
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.status == 0 {
				c.status = 400
				c.code = "invalid_input"
			}
			response(t, request(h, c.method, c.path, c.body, token, c.headers), c.status, c.code)
			if p.calls != 0 {
				t.Fatalf("rejection called port %d", p.calls)
			}
		})
	}
	for _, path := range []string{root, detail, units, action} {
		method, body := "GET", ""
		if path == action {
			method = "POST"
			body = good
		}
		for _, auth := range [][]string{nil, {"Bearer forged"}, {"Bearer "}, {"Bearer a b"}, {"Bearer forged", "Bearer forged"}} {
			response(t, request(h, method, path, body, "", map[string][]string{"Authorization": auth, "X-Auth-User-Id": {"verified-alex"}}), 401, "unauthenticated")
			if p.calls != 0 {
				t.Fatal("unauthenticated port effect")
			}
		}
	}
	for _, path := range []string{root, detail, units, action} {
		method := "POST"
		allow := "GET"
		if path == action {
			method = "GET"
			allow = "POST"
		}
		w := request(h, method, path, "", token, nil)
		response(t, w, 405, "method_not_allowed")
		if w.Header().Get("Allow") != allow {
			t.Fatal(w.Header())
		}
	}
	if p.calls != 0 {
		t.Fatal(p.calls)
	}
}
func TestDomainErrorsAndRevokedReplay(t *testing.T) {
	p, h, token := setup(t)
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{{cart.ErrInput, 400, "invalid_input"}, {cart.ErrPickupForbidden, 403, "forbidden"}, {cart.ErrNotFound, 404, "not_found"}, {cart.ErrConflict, 409, "idempotency_conflict"}, {cart.ErrStale, 409, "version_conflict"}, {cart.ErrPickupInvalidTransition, 409, "invalid_transition"}, {cart.ErrPickupPolicyUnresolved, 422, "policy_unresolved"}, {cart.ErrUnavailable, 503, "temporarily_unavailable"}, {staff.ErrUnavailable, 503, "temporarily_unavailable"}, {cart.ErrStoredData, 500, "internal_error"}, {cart.ErrLimit, 500, "internal_error"}, {errors.New("SQL raw-token private message"), 500, "internal_error"}} {
		for _, path := range []string{root, detail, units, action} {
			p.err = fmt.Errorf("private SQL: %w", c.err)
			method, body := "GET", ""
			if path == action {
				method = "POST"
				body = `{"action":"accept","expected_version":"0"}`
			}
			before := p.calls
			response(t, request(h, method, path, body, token, nil), c.status, c.code)
			if p.calls != before+1 || p.subject != "verified-alex" {
				t.Fatal("port auth/call mismatch")
			}
		}
	}
	p.err = nil
	response(t, request(h, "POST", action, `{"action":"accept","expected_version":"0"}`, token, nil), 200, "")
	before := p.calls
	p.err = cart.ErrPickupForbidden
	response(t, request(h, "POST", action, `{"action":"accept","expected_version":"0"}`, token, nil), 403, "forbidden")
	if p.calls != before+1 {
		t.Fatal("replay bypassed current port authorization")
	}
}
func TestInvalidDomainOutputFailsClosed(t *testing.T) {
	for _, mutate := range []func(*fakePort){
		func(p *fakePort) { p.order.ID = cart.PickupOrderID{} }, func(p *fakePort) { p.order.Status = "delivered" }, func(p *fakePort) { p.order.CreatedAt = time.Time{} }, func(p *fakePort) { p.order.Total.AmountMinor = -1 }, func(p *fakePort) { p.order.Total.Currency = "usd" }, func(p *fakePort) { p.order.Total.Exponent = 7 }, func(p *fakePort) { p.order.Confirmed.Total.AmountMinor++ }, func(p *fakePort) { p.order.Confirmed.DeliveryFee.AmountMinor = 1 }, func(p *fakePort) { p.order.Confirmed.UnitCount = 0 }, func(p *fakePort) { p.order.Payment.State = cart.PickupPaid }, func(p *fakePort) { p.order.Estimate = &cart.PickupEstimate{Minutes: -1, AsOf: time.Now()} }, func(p *fakePort) { p.order.Estimate = &cart.PickupEstimate{Minutes: 1} },
	} {
		p, h, token := setup(t)
		mutate(p)
		response(t, request(h, "GET", detail, "", token, nil), 500, "internal_error")
	}
	p, h, token := setup(t)
	bad := ""
	p.list.NextCursor = &bad
	response(t, request(h, "GET", root, "", token, nil), 500, "internal_error")
	p.list.NextCursor = nil
	p.list.Items = append(p.list.Items, p.list.Items[0])
	response(t, request(h, "GET", root, "", token, nil), 500, "internal_error")
	p, h, token = setup(t)
	var other cart.PickupCandidateID
	parse(t, &other, oid)
	p.units.SummaryID = other
	response(t, request(h, "GET", units, "", token, nil), 500, "internal_error")
	p, h, token = setup(t)
	p.units.Items[0].Note = "\xff"
	response(t, request(h, "GET", units, "", token, nil), 500, "internal_error")
	p, h, token = setup(t)
	p.units.Items = append(p.units.Items, p.units.Items[0])
	response(t, request(h, "GET", units, "", token, nil), 500, "internal_error")
	p, h, token = setup(t)
	p.list.Items = make([]cart.PickupOrderHeader, 101)
	response(t, request(h, "GET", root+"?limit=100", "", token, nil), 500, "internal_error")
	// Worst-case JSON escaping within scalar/count bounds remains complete and bounded.
	p, h, token = setup(t)
	p.units.UnitCount = 100
	p.units.Items = nil
	for i := range 100 {
		u := cart.PickupOrderUnit{Name: strings.Repeat("<", 200), Note: strings.Repeat("<&", 250), Price: p.order.Total}
		parse(t, &u.ID, fmt.Sprintf("30000000-0000-4000-8000-%012d", i+1))
		parse(t, &u.MenuID, mid)
		p.units.Items = append(p.units.Items, u)
	}
	obj := response(t, request(h, "GET", units+"?limit=100", "", token, nil), 200, "")
	items := obj["data"].(map[string]any)["items"].([]any)
	if len(items) != 100 || items[99].(map[string]any)["note"] != strings.Repeat("<&", 250) {
		t.Fatal("content clipped")
	}
}
func TestCursorAndKeyBoundaries(t *testing.T) {
	p, h, token := setup(t)
	for _, n := range []int{1, 2048} {
		c := strings.Repeat("x", n)
		response(t, request(h, "GET", root+"?limit=1&cursor="+url.QueryEscape(c), "", token, nil), 200, "")
		if p.page.Cursor != c || p.page.Limit != 1 {
			t.Fatal(p.page)
		}
	}
	for _, n := range []int{1, 128} {
		k := strings.Repeat("!", n)
		response(t, request(h, "POST", action, `{"action":"accept","expected_version":"0"}`, token, map[string][]string{"Idempotency-Key": {k}}), 200, "")
		if p.intent.IdempotencyKey != k {
			t.Fatal(p.intent)
		}
	}
}
func TestCursorASCIIAndDomainCurrency(t *testing.T) {
	p, h, token := setup(t)
	// The HTTP contract bounds ASCII, not a particular MAC/base64 alphabet.
	// Authenticity and scoped cursor validation remain in the domain port.
	cursor := "opaque \x7f"
	response(t, request(h, "GET", root+"?cursor="+url.QueryEscape(cursor), "", token, nil), 200, "")
	if p.page.Cursor != cursor {
		t.Fatalf("cursor rewritten %q", p.page.Cursor)
	}
	p.err = cart.ErrInput
	response(t, request(h, "GET", units+"?cursor=wrong-resource", "", token, nil), 400, "invalid_input")
	if p.page.Cursor != "wrong-resource" || p.summaryID.String() != sid {
		t.Fatal("cursor scope not passed")
	}
	p.err = nil
	second := p.list.Items[0]
	parse(t, &second.ID, uid)
	second.Total.Currency = "JPY"
	p.list.Items = append(p.list.Items, second)
	response(t, request(h, "GET", root, "", token, nil), 500, "internal_error")
}

func TestPublicBoundaryPreservesPagesAndExactBodyLimit(t *testing.T) {
	p, h, token := setup(t)
	next := "signed.next"
	p.list.NextCursor = &next
	d := response(t, request(h, "GET", root, "", token, nil), 200, "")["data"].(map[string]any)
	if d["next_cursor"] != next {
		t.Fatal(d)
	}
	p.units.UnitCount = 101
	p.units.NextCursor = &next
	d = response(t, request(h, "GET", units, "", token, nil), 200, "")["data"].(map[string]any)
	if d["next_cursor"] != next || d["unit_count"] != float64(101) {
		t.Fatal(d)
	}
	p.units.NextCursor = nil
	p.units.Items[0].Note = "last exact note\n"
	d = response(t, request(h, "GET", units+"?cursor="+next, "", token, nil), 200, "")["data"].(map[string]any)
	if d["next_cursor"] != nil || d["unit_count"] != float64(101) || d["items"].([]any)[0].(map[string]any)["note"] != "last exact note\n" || p.page.Cursor != next {
		t.Fatal(d)
	}
	body := `{"action":"accept","expected_version":"9223372036854775807"}`
	body += strings.Repeat(" ", 65536-len(body))
	response(t, request(h, "POST", action, body, token, map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}}), 200, "")
	if p.intent.ExpectedVersion.Int64() != 9223372036854775807 {
		t.Fatal(p.intent)
	}
	before := p.calls
	parts := strings.Split(token, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	response(t, request(h, "GET", root, "", strings.Join(parts, "."), nil), 401, "unauthenticated")
	if p.calls != before {
		t.Fatal("invalid signature reached domain")
	}
	for _, path := range []string{"/unknown", detail + "/revisions", detail + "/control", root + "//other"} {
		w := request(h, "GET", path, "", token, nil)
		response(t, w, 404, "not_found")
		if w.Header().Get("Location") != "" {
			t.Fatal("redirected")
		}
	}
	if p.calls != before {
		t.Fatal("unknown route reached domain")
	}
}

func TestVerifierUnavailable(t *testing.T) {
	p := &fakePort{}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer keys.Close()
	v, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	h, err := stafforders.NewHandler(p, v)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "sub": "alex", "iat": time.Now().Unix(), "exp": time.Now().Add(50 * time.Second).Unix()})
	tok.Header["kid"] = "test"
	raw, _ := tok.SignedString(priv)
	response(t, request(h, "GET", root, "", raw, nil), 503, "temporarily_unavailable")
	if p.calls != 0 {
		t.Fatal(p.calls)
	}
}
func TestConstructor(t *testing.T) {
	if h, err := stafforders.NewHandler(nil, nil); h != nil || !errors.Is(err, staff.ErrInput) {
		t.Fatal(h, err)
	}
}

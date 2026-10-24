package staffavailability_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffavailability"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "11111111-1111-4111-8111-111111111111"
const other = "22222222-2222-4222-8222-222222222222"
const itemID = "10000000-0000-4000-8000-000000000001"
const itemPath = "/v1/staff/catalog/items/" + itemID + "/availability"
const settingsPath = "/v1/staff/restaurant/availability"

type fixture struct {
	store    *catalog.Store
	members  *staff.Store
	verifier *staff.Verifier
	handler  http.Handler
	token    string
	fetches  atomic.Int32
	pool     *pgxpool.Pool
	base     *postgres.Store
	baseline foundation.Snapshot
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = catalog.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.store.Close)
	f.members, err = staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.members.Close)
	for _, sub := range []string{"alex", "sam"} {
		if _, err = f.members.EnsureMember(t.Context(), sub); err != nil {
			t.Fatal(err)
		}
	}
	cfg := catalog.Settings{Currency: "KWD", CurrencyExponent: 3, FlatFeeMinor: 17, Open: true}
	items := []catalog.Item{{ID: itemID, Name: "料理 <&>", PriceMinor: 9223372036854775807, Currency: "KWD", Available: true}}
	if err = f.store.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatal(err)
	}
	f.base, err = postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.base.Close)
	if _, err = f.base.Accept(t.Context(), foundation.Event{ID: "availability-preservation", Sender: "fictional", Text: "preserved"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.baseline, err = f.base.Inspect(t.Context(), "availability-preservation")
	if err != nil {
		t.Fatal(err)
	}
	config, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	f.pool, err = pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.pool.Close)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":%q}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(keys.Close)
	f.verifier, err = staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "sub": "alex", "iat": time.Now().Unix(), "exp": time.Now().Add(59 * time.Second).Unix()})
	token.Header["kid"] = "test"
	f.token, err = token.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = staffavailability.NewHandler(f.store, f.members, f.verifier)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func request(t *testing.T, h http.Handler, ctx context.Context, method, path, body, token string, headers map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
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
func response(t *testing.T, w *httptest.ResponseRecorder, status int, code, message string) map[string]any {
	t.Helper()
	if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" || w.Body.Len() > 512*1024 {
		t.Fatalf("response %d %v %s want %d", w.Code, w.Header(), w.Body.String(), status)
	}
	var obj map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	id, ok := obj["request_id"].(string)
	if !ok || len(id) != 32 || len(obj) != 2 {
		t.Fatalf("envelope: %v", obj)
	}
	if status != 200 {
		want := map[string]any{"code": code, "message": message}
		if !reflect.DeepEqual(obj["error"], want) {
			t.Fatalf("error: %v want %v", obj, want)
		}
	}
	return obj
}
func TestAvailabilityHTTP(t *testing.T) {
	f := setup(t)
	// The fixture pool is used only for counters, locks and isolated corruption.
	baseBefore := func() (catalog.Settings, []catalog.Item) {
		s, err := f.store.Settings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m, err := f.store.Menu(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return s, m
	}
	initial, menu := baseBefore()
	unchanged := func() {
		t.Helper()
		s, m := baseBefore()
		if s != initial || !reflect.DeepEqual(m, menu) {
			t.Fatalf("rejection changed state: %+v %+v", s, m)
		}
		var count int
		if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM app.operations").Scan(&count); err != nil || count != 0 {
			t.Fatalf("operation count %d %v", count, err)
		}
		after, err := f.base.Inspect(t.Context(), "availability-preservation")
		if err != nil || !reflect.DeepEqual(after, f.baseline) {
			t.Fatal("foundation/outbound changed", err)
		}
	}
	cases := []struct {
		name, body    string
		headers       map[string][]string
		status        int
		code, message string
	}{
		{name: "missing", body: `{"available":false}`},
		{name: "extra", body: `{"available":false,"expected_version":"0","restaurant_id":"` + other + `"}`},
		{name: "null", body: `{"available":null,"expected_version":"0"}`},
		{name: "null version", body: `{"available":false,"expected_version":null}`},
		{name: "duplicate", body: `{"available":false,"expected_version":"0","available":true}`},
		{name: "escaped duplicate", body: `{"available":false,"expected_version":"0","\u0061vailable":true}`},
		{name: "number version", body: `{"available":false,"expected_version":0}`},
		{name: "leading zero", body: `{"available":false,"expected_version":"00"}`},
		{name: "negative", body: `{"available":false,"expected_version":"-1"}`},
		{name: "overflow", body: `{"available":false,"expected_version":"9223372036854775808"}`},
		{name: "plus", body: `{"available":false,"expected_version":"+1"}`},
		{name: "fraction", body: `{"available":false,"expected_version":"1.0"}`},
		{name: "wrong boolean", body: `{"available":"false","expected_version":"0"}`},
		{name: "trailing", body: `{"available":false,"expected_version":"0"} {}`},
		{name: "array", body: `[]`},
		{name: "invalid utf8", body: "{\"available\":false,\"expected_version\":\"\xff\"}"},
		{name: "empty key", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Idempotency-Key": {}}},
		{name: "duplicate key header", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Idempotency-Key": {"a", "b"}}},
		{name: "whitespace key", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Idempotency-Key": {"a b"}}},
		{name: "unicode key", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Idempotency-Key": {"é"}}},
		{name: "duplicate authorization", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Authorization": {"Bearer forged", "Bearer forged"}}, status: 401, code: "unauthenticated", message: "Authentication is required."},
		{name: "long key", body: `{"available":false,"expected_version":"0"}`, headers: map[string][]string{"Idempotency-Key": {strings.Repeat("a", 129)}}},
		{name: "media", body: `{}`, headers: map[string][]string{"Content-Type": {"text/plain"}}, status: 415, code: "unsupported_media_type", message: "Content-Type must be application/json."},
		{name: "oversize", body: strings.Repeat(" ", 65537), status: 413, code: "payload_too_large", message: "Request body is too large."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, message := tc.status, tc.code, tc.message
			if status == 0 {
				status = 400
				code = "invalid_input"
				message = "Invalid request."
			}
			response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, tc.body, f.token, tc.headers), status, code, message)
			unchanged()
		})
	}
	for _, path := range []string{itemPath + "?", itemPath + "?extra=1", "/v1/staff/catalog/items/10000000-0000-1000-8000-000000000001/availability", "/v1/staff/catalog/items/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA/availability"} {
		response(t, request(t, f.handler, t.Context(), "PATCH", path, `{"expected_version":"0","available":false}`, f.token, nil), 400, "invalid_input", "Invalid request.")
		unchanged()
	}
	for _, token := range []string{"", "forged"} {
		response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"0","available":false}`, token, nil), 401, "unauthenticated", "Authentication is required.")
		unchanged()
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"0","available":false}`, "", map[string][]string{"Origin": {"http://127.0.0.1:3000"}, "X-Auth-User-Id": {"alex"}, "X-Restaurant-Id": {restaurant}, "X-Role": {"staff"}}), 401, "unauthenticated", "Authentication is required.")
	unchanged()
	if f.fetches.Load() != 0 {
		t.Fatal("invalid requests fetched JWKS")
	}
	w := request(t, f.handler, t.Context(), "GET", settingsPath, "", f.token, nil)
	response(t, w, 405, "method_not_allowed", "The method is not allowed.")
	if w.Header().Get("Allow") != "PATCH" {
		t.Fatal("Allow missing")
	}
	unchanged()
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"9223372036854775807","available":false}`, f.token, nil), 409, "version_conflict", "Refresh availability before trying again.")
	unchanged()
	missing := strings.Replace(itemPath, itemID, "10000000-0000-4000-8000-000000000099", 1)
	response(t, request(t, f.handler, t.Context(), "PATCH", missing, `{"expected_version":"0","available":false}`, f.token, nil), 404, "not_found", "Catalog item was not found.")
	unchanged()
	// Real loopback transport on the public handler.
	srv := httptest.NewServer(f.handler)
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), "PATCH", srv.URL+itemPath, strings.NewReader(`{"available":false,"expected_version":"0"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "intent")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("success: %d %s %v", res.StatusCode, first, err)
	}
	var wire map[string]any
	if err = json.Unmarshal(first, &wire); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": itemID, "name": "料理 <&>", "price": map[string]any{"amount_minor": "9223372036854775807", "currency": "KWD", "exponent": float64(3)}, "available": false, "version": "1"}
	if !reflect.DeepEqual(wire["data"], want) {
		t.Fatalf("DTO: %s", first)
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"1","available":true}`, f.token, map[string][]string{"Idempotency-Key": {"next"}}), 200, "", "")
	replay := request(t, f.handler, t.Context(), "PATCH", itemPath, " { \"expected_version\" : \"0\", \"available\" : false } ", f.token, nil)
	response(t, replay, 200, "", "")
	if replay.Body.String() != string(first) {
		t.Fatal("replay bytes changed")
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"0","available":true}`, f.token, nil), 409, "idempotency_conflict", "Idempotency key was already used for a different request.")
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"0","available":true}`, f.token, map[string][]string{"Idempotency-Key": {"rollback"}}), 409, "version_conflict", "Refresh availability before trying again.")
	response(t, request(t, f.handler, t.Context(), "PATCH", itemPath, `{"expected_version":"2","available":true}`, f.token, map[string][]string{"Idempotency-Key": {"rollback"}}), 200, "", "")
	w = request(t, f.handler, t.Context(), "PATCH", settingsPath, `{"expected_version":"0","open":false}`, f.token, nil)
	obj := response(t, w, 200, "", "")
	if !reflect.DeepEqual(obj["data"], map[string]any{"open": false, "version": "1"}) {
		t.Fatal(obj)
	}
	response(t, request(t, f.handler, t.Context(), "PATCH", settingsPath, `{"expected_version":"0","available":false}`, f.token, nil), 400, "invalid_input", "Invalid request.")
	if err := f.members.RevokeMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{itemPath, settingsPath} {
		body := `{"expected_version":"0","available":false}`
		if path == settingsPath {
			body = `{"expected_version":"0","open":false}`
		}
		response(t, request(t, f.handler, t.Context(), "PATCH", path, body, f.token, nil), 403, "forbidden", "Staff access is not available.")
	}
}

func TestConcurrentHTTP(t *testing.T) {
	f := setup(t)
	for _, path := range []string{itemPath, settingsPath} {
		field := "available"
		if path == settingsPath {
			field = "open"
		}
		run := func(version string, same bool) {
			start := make(chan struct{})
			out := make([]*httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					key := "dup"
					if !same {
						key = []string{"a", "b"}[i]
					}
					out[i] = request(t, f.handler, t.Context(), "PATCH", path, `{"expected_version":"`+version+`","`+field+`":false}`, f.token, map[string][]string{"Idempotency-Key": {key}})
				}(i)
			}
			close(start)
			wg.Wait()
			if same {
				response(t, out[0], 200, "", "")
				response(t, out[1], 200, "", "")
				if out[0].Body.String() != out[1].Body.String() {
					t.Fatal("duplicate bodies differ")
				}
			} else {
				wins := 0
				for _, w := range out {
					if w.Code == 200 {
						response(t, w, 200, "", "")
						wins++
					} else {
						response(t, w, 409, "version_conflict", "Refresh availability before trying again.")
					}
				}
				if wins != 1 {
					t.Fatal("not exactly one winner")
				}
			}
		}
		run("0", true)
		run("1", false)
	}
	settings, err := f.store.Settings(t.Context())
	if err != nil || settings.Version != 2 {
		t.Fatal(settings, err)
	}
	menu, err := f.store.Menu(t.Context())
	if err != nil || menu[0].Version != 2 {
		t.Fatal(menu, err)
	}
}

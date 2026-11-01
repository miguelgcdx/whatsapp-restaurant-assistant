package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffavailability"
	"restaurant-assistant/backend/internal/staffcatalog"
	"restaurant-assistant/backend/internal/stafforders"
	"restaurant-assistant/backend/internal/testpg"
	"strings"
	"testing"
)

// These requests exercise the actual orders handler without a database or JWKS
// listener: valid request shapes must be rejected before either lazy dependency
// is contacted. A plain legacy fallback cannot satisfy the orders envelope.
func TestOrdersDispatcherUnauthenticated(t *testing.T) {
	const restaurant = "11111111-1111-4111-8111-111111111111"
	const orderID = "22222222-2222-4222-8222-222222222222"
	const summaryID = "33333333-3333-4333-8333-333333333333"
	store, err := cart.OpenStore(t.Context(), "postgres://fictional@127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pickup, err := cart.NewPickupStore(store, restaurant, cart.PickupCompletionAssessmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := staff.NewVerifier("http://127.0.0.1:3000", "http://127.0.0.1:1/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	orders, err := stafforders.NewHandler(pickup, verifier)
	if err != nil {
		t.Fatal(err)
	}
	fallback := http.HandlerFunc(http.NotFound)
	handler := dispatch(fallback, fallback, fallback, orders)
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"list", "GET", "/v1/staff/orders", ""},
		{"detail", "GET", "/v1/staff/orders/" + orderID, ""},
		{"units", "GET", "/v1/staff/orders/" + orderID + "/summaries/" + summaryID + "/units", ""},
		{"action", "POST", "/v1/staff/orders/" + orderID + "/actions", `{"action":"accept","expected_version":"0"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "host-unauthenticated-action")
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusUnauthorized || res.Header().Get("Content-Type") != "application/json" || res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Location") != "" || res.Header().Get("Allow") != "" {
				t.Fatalf("orders route: status=%d headers=%v body=%q", res.Code, res.Header(), res.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			id, err := hex.DecodeString(envelope.RequestID)
			if err != nil || len(id) != 16 || hex.EncodeToString(id) != envelope.RequestID {
				t.Fatalf("invalid request ID: %q", envelope.RequestID)
			}
			want := `{"error":{"code":"unauthenticated","message":"Authentication is required."},"request_id":"` + envelope.RequestID + "\"}\n"
			if res.Body.String() != want {
				t.Fatalf("orders response=%q want=%q", res.Body.String(), want)
			}
		})
	}
}

func TestDispatcherPreservesOriginalPaths(t *testing.T) {
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, "%s %s %s?%s", name, r.Method, r.URL.Path, r.URL.RawQuery)
		})
	}
	handler := dispatch(marker("access"), marker("catalog"), marker("availability"), marker("orders"))
	for _, tc := range []struct{ method, path, target string }{
		{"GET", "/v1/staff/access", "access"},
		{"GET", "/v1/staff/catalog?limit=2", "catalog"},
		{"PATCH", "/v1/staff/catalog/items/22222222-2222-4222-8222-222222222222/availability", "availability"},
		{"PATCH", "/v1/staff/restaurant/availability", "availability"},
		{"GET", "/v1/staff/orders?limit=2&cursor=opaque", "orders"},
		{"GET", "/v1/staff/orders/22222222-2222-4222-8222-222222222222", "orders"},
		{"GET", "/v1/staff/orders/22222222-2222-4222-8222-222222222222/summaries/33333333-3333-4333-8333-333333333333/units?limit=2&cursor=opaque", "orders"},
		{"POST", "/v1/staff/orders/22222222-2222-4222-8222-222222222222/actions", "orders"},
		{"GET", "/unknown", "access"},
		{"GET", "/v1/staff//orders", "access"},
		{"GET", "/v1/staff/../staff/orders", "access"},
		{"GET", "//v1/staff/orders", "access"},
		{"GET", "/v1/staff/orders/id/actions/", "access"},
		{"GET", "/v1/staff/orders/id/summaries/id/units/", "access"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			want := fmt.Sprintf("%s %s %s?%s", tc.target, tc.method, req.URL.Path, req.URL.RawQuery)
			if res.Code != http.StatusAccepted || res.Body.String() != want || res.Header().Get("Location") != "" {
				t.Fatalf("dispatch: status=%d headers=%v body=%q want=%q", res.Code, res.Header(), res.Body.String(), want)
			}
		})
	}
}

func TestOrdersHelp(t *testing.T) {
	var out, errout bytes.Buffer
	code := execute(t.Context(), []string{"-help"}, func(string) string {
		t.Fatal("help read environment")
		return ""
	}, &out, &errout)
	if code != 0 || out.String() != apiHelp || errout.Len() != 0 {
		t.Fatalf("help: exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
	}
	for _, text := range []string{
		"GET /v1/staff/orders",
		"GET /v1/staff/orders/{id}",
		"GET /v1/staff/orders/{id}/summaries/{summary_id}/units",
		"POST /v1/staff/orders/{id}/actions",
		"process-local",
	} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help missing %q", text)
		}
	}
}

func TestProductionDispatcher(t *testing.T) {
	const restaurant = "11111111-1111-4111-8111-111111111111"
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	if _, err := members.EnsureMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings := catalog.Settings{Currency: "JPY", CurrencyExponent: 0, Open: true}
	items := []catalog.Item{{ID: "10000000-0000-4000-8000-000000000001", Name: "料理", PriceMinor: 500, Currency: "JPY", Available: true}}
	if err := store.Initialize(t.Context(), settings, items); err != nil {
		t.Fatal(err)
	}
	foundationStore, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer foundationStore.Close()
	event := foundation.Event{ID: "dispatcher-preservation", Sender: "fictional", Text: "preserved"}
	if _, err := foundationStore.Accept(t.Context(), event, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := foundationStore.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":%q}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	defer keys.Close()
	verifier, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "sub": "alex", "iat": time.Now().Unix(), "exp": time.Now().Add(50 * time.Second).Unix()})
	token.Header["kid"] = "test"
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	for _, key64 := range []string{"", hex.EncodeToString(key)} {
		t.Run(fmt.Sprintf("key configured %t", key64 != ""), func(t *testing.T) {
			access, err := staff.NewHandler(members, restaurant, verifier)
			if err != nil {
				t.Fatal(err)
			}
			catalogHandler, err := staffcatalog.NewHandler(store, members, restaurant, verifier, key64)
			if err != nil {
				t.Fatal(err)
			}
			// This exact production helper is also called by run(), not a test router.
			availabilityHandler, err := staffavailability.NewHandler(store, members, verifier)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(dispatch(access, catalogHandler, availabilityHandler, http.NotFoundHandler()))
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			request := func(method, path, auth string, status int, body, cache, allow string) {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if auth != "" {
					req.Header.Set("Authorization", "Bearer "+auth)
				}
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != status || res.Header.Get("Cache-Control") != cache || res.Header.Get("Allow") != allow || res.Header.Get("Location") != "" {
					t.Fatalf("%s %s: %d %v %s", method, path, res.StatusCode, res.Header, got)
				}
				if body != "" {
					if string(got) != body {
						t.Fatalf("%s body: %q", path, got)
					}
				} else {
					var envelope map[string]any
					if err := json.Unmarshal(got, &envelope); err != nil {
						t.Fatal(err)
					}
					id, ok := envelope["request_id"].(string)
					if !ok || len(id) != 32 || len(envelope) != 2 {
						t.Fatal("catalog envelope", envelope)
					}
					if res.Header.Get("Content-Type") != "application/json" {
						t.Fatal("catalog content type")
					}
					if status == 200 {
						want := map[string]any{"items": []any{map[string]any{"id": items[0].ID, "name": "料理", "price": map[string]any{"amount_minor": "500", "currency": "JPY", "exponent": float64(0)}, "available": true, "version": "0"}}, "next_cursor": nil, "restaurant": map[string]any{"open": true, "version": "0"}}
						if !reflect.DeepEqual(envelope["data"], want) {
							t.Fatal("catalog data", envelope)
						}
					} else {
						want := map[string]any{"code": "temporarily_unavailable", "message": "Catalog is temporarily unavailable."}
						if !reflect.DeepEqual(envelope["error"], want) {
							t.Fatal("catalog error", envelope)
						}
					}
				}
			}
			request("GET", "/v1/staff/access", raw, 200, `{"restaurant_id":"`+restaurant+`"}`+"\n", "no-store", "")
			request("GET", "/v1/staff/access", "", 401, "{\"error\":{\"code\":\"unauthenticated\",\"message\":\"Authentication is required.\"}}\n", "no-store", "")
			request("GET", "/v1/staff/access?extra=1", raw, 400, "{\"error\":{\"code\":\"invalid_input\",\"message\":\"Invalid request.\"}}\n", "no-store", "")
			request("POST", "/v1/staff/access", raw, 405, "{\"error\":{\"code\":\"method_not_allowed\",\"message\":\"The method is not allowed.\"}}\n", "no-store", "GET")
			catalogStatus := 200
			if key64 == "" {
				catalogStatus = 503
			}
			request("GET", "/v1/staff/catalog", raw, catalogStatus, "", "no-store", "")
			for _, path := range []string{"/unknown", "/v1/staff//catalog", "/v1/staff/catalog/", "/v1/staff/../staff/catalog", "//v1/staff/catalog", "/v1/staff//access", "/v1/staff/access/"} {
				request("GET", path, raw, 404, "404 page not found\n", "", "")
			}
		})
	}
	after, err := foundationStore.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("dispatcher changed foundation/outbound state", err)
	}
	got, err := store.Menu(t.Context())
	if err != nil || !reflect.DeepEqual(got, items) {
		t.Fatal("dispatcher changed menu", err)
	}
	gotSettings, err := store.Settings(t.Context())
	if err != nil || gotSettings != settings {
		t.Fatal("dispatcher changed settings", err)
	}
	// The real production dispatcher permits writes with and without the read-only cursor key.
	for index, key64 := range []string{"", hex.EncodeToString(key)} {
		access, err := staff.NewHandler(members, restaurant, verifier)
		if err != nil {
			t.Fatal(err)
		}
		read, err := staffcatalog.NewHandler(store, members, restaurant, verifier, key64)
		if err != nil {
			t.Fatal(err)
		}
		write, err := staffavailability.NewHandler(store, members, verifier)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(dispatch(access, read, write, http.NotFoundHandler()))
		for _, target := range []struct{ path, field string }{{"/v1/staff/catalog/items/" + items[0].ID + "/availability", "available"}, {"/v1/staff/restaurant/availability", "open"}} {
			body := fmt.Sprintf(`{"expected_version":"%d","%s":false}`, index, target.field)
			req, err := http.NewRequestWithContext(t.Context(), "PATCH", server.URL+target.path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+raw)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", fmt.Sprintf("dispatch-%d", index))
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" || !bytes.Contains(payload, []byte(fmt.Sprintf(`"version":"%d"`, index+1))) {
				t.Fatalf("PATCH %s: %d %s %v", target.path, res.StatusCode, payload, err)
			}
		}
		server.Close()
	}
	after, err = foundationStore.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("writes changed foundation/outbound state", err)
	}
}

func TestCatalogConfiguration(t *testing.T) {
	valid := map[string]string{"LOCAL_DATABASE_URL": "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", "LOCAL_RESTAURANT_ID": "12345678-1234-4234-8234-123456789abc", "STAFF_JWT_ISSUER": "http://127.0.0.1:3000", "STAFF_JWKS_URL": "http://127.0.0.1:3000/api/auth/jwks"}
	for _, key := range []string{"secret-sentinel", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		var out, errout bytes.Buffer
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		code := execute(ctx, []string{"-listen", "127.0.0.1:0"}, func(k string) string {
			if k == "STAFF_CATALOG_CURSOR_KEY" {
				return key
			}
			return valid[k]
		}, &out, &errout)
		if code != 1 || out.Len() != 0 || errout.String() != "Staff API could not be completed.\n" {
			t.Fatalf("invalid key: %d %q %q", code, out.String(), errout.String())
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errout bytes.Buffer
	code := execute(ctx, []string{"-listen", "127.0.0.1:0"}, func(k string) string {
		if k == "STAFF_CATALOG_CURSOR_KEY" {
			return strings.Repeat("a", 64)
		}
		return valid[k]
	}, &out, &errout)
	if code != 0 || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("valid key startup: %d %q %q", code, out.String(), errout.String())
	}
	if !strings.Contains(apiHelp, "PATCH /v1/staff/catalog/items/{id}/availability") || !strings.Contains(apiHelp, "PATCH /v1/staff/restaurant/availability") || !strings.Contains(apiHelp, "Idempotency-Key") || !strings.Contains(apiHelp, "STAFF_CATALOG_CURSOR_KEY") || !strings.Contains(apiHelp, "GET /v1/staff/catalog") {
		t.Fatal("catalog help missing")
	}
}

func TestAPIBoundariesAndHelp(t *testing.T) {
	valid := map[string]string{"LOCAL_DATABASE_URL": "postgres://fictional@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", "LOCAL_RESTAURANT_ID": "12345678-1234-4234-8234-123456789abc", "STAFF_JWT_ISSUER": "http://127.0.0.1:3000", "STAFF_JWKS_URL": "http://127.0.0.1:3000/api/auth/jwks"}
	for _, args := range [][]string{{"-listen", "0.0.0.0:0"}, {"-listen", "localhost:8081"}, {"-listen", "127.0.0.1:65536"}, {"-listen", "127.0.0.1:-1"}, {"-unknown=secret-sentinel"}, {"secret-sentinel"}} {
		var out, errout bytes.Buffer
		code := execute(t.Context(), args, func(k string) string { return valid[k] }, &out, &errout)
		if code != 1 || out.Len() != 0 || errout.String() != "Staff API could not be completed.\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
		}
	}
	for _, key := range []string{"LOCAL_DATABASE_URL", "LOCAL_RESTAURANT_ID", "STAFF_JWT_ISSUER", "STAFF_JWKS_URL"} {
		t.Run(key, func(t *testing.T) {
			var out, errout bytes.Buffer
			code := execute(t.Context(), nil, func(k string) string {
				if k == key {
					return "secret-sentinel"
				}
				return valid[k]
			}, &out, &errout)
			if code != 1 || out.Len() != 0 || errout.String() != "Staff API could not be completed.\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
			}
		})
	}
	var out, errout bytes.Buffer
	code := execute(t.Context(), []string{"-help"}, func(string) string { t.Fatal("help read environment"); return "" }, &out, &errout)
	if code != 0 || out.String() != apiHelp || errout.Len() != 0 {
		t.Fatalf("help %d %q %q", code, out.String(), errout.String())
	}
	// No DB/JWKS is listening: canceled runtime must bind/shut down without probing,
	// provisioning or migrating dependencies.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out.Reset()
	errout.Reset()
	code = execute(ctx, []string{"-listen", "127.0.0.1:0"}, func(k string) string { return valid[k] }, &out, &errout)
	if code != 0 || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("lazy/shutdown %d %q %q", code, out.String(), errout.String())
	}
}

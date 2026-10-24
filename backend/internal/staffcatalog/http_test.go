package staffcatalog_test

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffcatalog"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "11111111-1111-4111-8111-111111111111"
const other = "22222222-2222-4222-8222-222222222222"

func TestProtectedCatalog(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := catalog.Settings{Currency: "JPY", CurrencyExponent: 0, Open: true}
	items := make([]catalog.Item, 105)
	for i := range items {
		items[i] = catalog.Item{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), Name: strings.Repeat("<&料理", 50), Currency: "JPY", PriceMinor: int64(i), Available: i%2 == 0}
	}
	if err := store.Initialize(t.Context(), cfg, items); err != nil {
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
	base, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	event := foundation.Event{ID: "catalog-read", Sender: "fictional", Text: "preserved"}
	if _, err := base.Accept(t.Context(), event, time.Now()); err != nil {
		t.Fatal(err)
	}
	before, err := base.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"test","x":%q}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	defer keys.Close()
	verifier, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{"iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "sub": "alex", "iat": time.Now().Unix(), "exp": time.Now().Add(50 * time.Second).Unix()}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = "test"
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	h, err := staffcatalog.NewHandler(store, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	request := func(handler http.Handler, method, path, body, auth string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" || len(w.Body.Bytes()) > 512*1024 {
			t.Errorf("response %d %v %s; want status %d", w.Code, w.Header(), w.Body.String(), status)
		}
		var obj map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		id, ok := obj["request_id"].(string)
		if !ok || len(id) < 16 || len(obj) != 2 {
			t.Fatalf("envelope %v", obj)
		}
		if status != 200 {
			messages := map[int]string{400: "Invalid request.", 401: "Authentication is required.", 403: "Staff access is not available.", 405: "The method is not allowed.", 503: "Catalog is temporarily unavailable.", 500: "Catalog could not be completed."}
			codes := map[int]string{400: "invalid_input", 401: "unauthenticated", 403: "forbidden", 405: "method_not_allowed", 503: "temporarily_unavailable", 500: "internal_error"}
			e, ok := obj["error"].(map[string]any)
			if !ok || len(e) != 2 || e["code"] != codes[status] || e["message"] != messages[status] {
				t.Errorf("error %v; want status %d", obj, status)
			}
		}
		if status == 405 && w.Header().Get("Allow") != "GET" {
			t.Fatal("Allow missing")
		}
		return obj
	}
	for _, path := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=1&limit=2", "?extra=1", "?cursor=", "?cursor=%ff", "?cursor=" + strings.Repeat("a", 2049), "?limit=%zz", "?limit=1;cursor=x"} {
		request(h, "GET", "/v1/staff/catalog"+path, "", raw, 400)
	}
	request(h, "GET", "/v1/staff/catalog", "x", raw, 400)
	request(h, "POST", "/v1/staff/catalog", "", raw, 405)
	request(h, "GET", "/v1/staff/catalog", "", "", 401)
	request(h, "GET", "/v1/staff/catalog", "", "forged", 401)
	for _, framing := range []string{"Content-Length", "Transfer-Encoding", "Trailer"} {
		req := httptest.NewRequest("GET", "/v1/staff/catalog", nil)
		req.Header.Set(framing, "0")
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var obj map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || w.Header().Get("Cache-Control") != "no-store" || obj["request_id"] == nil || obj["error"].(map[string]any)["code"] != "invalid_input" {
			t.Fatalf("framing %s: %d %s", framing, w.Code, w.Body.String())
		}
	}
	legacy, err := staff.NewHandler(members, restaurant, verifier)
	if err != nil {
		t.Fatal(err)
	}
	legacyRequest := httptest.NewRequest("GET", "/v1/staff/access", nil)
	legacyRequest.Header.Set("Authorization", "Bearer "+raw)
	legacyResponse := httptest.NewRecorder()
	legacy.ServeHTTP(legacyResponse, legacyRequest)
	if legacyResponse.Code != 200 || legacyResponse.Body.String() != `{"restaurant_id":"`+restaurant+`"}`+"\n" || legacyResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("legacy access changed")
	}
	for _, path := range []string{"/unknown", "/v1/staff//catalog", "/v1/staff/catalog/"} {
		w := httptest.NewRecorder()
		legacy.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || w.Header().Get("Location") != "" || w.Body.String() != "404 page not found\n" {
			t.Fatal("unknown route changed")
		}
	}
	if fetches.Load() != 1 {
		t.Fatal("rejected shapes performed outbound requests beyond the legacy authenticated read")
	}
	// Real loopback HTTP, signed JWT, and database rows, including default page.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/staff/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Data struct {
			Items []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Price struct {
					Amount   string `json:"amount_minor"`
					Currency string `json:"currency"`
					Exponent int    `json:"exponent"`
				} `json:"price"`
				Available bool   `json:"available"`
				Version   string `json:"version"`
			} `json:"items"`
			Next       *string `json:"next_cursor"`
			Restaurant struct {
				Open    bool   `json:"open"`
				Version string `json:"version"`
			} `json:"restaurant"`
		} `json:"data"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(bytes, &wire); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || len(wire.Data.Items) != 50 || wire.Data.Next == nil || wire.RequestID == "" || wire.Data.Restaurant.Version != "0" || !wire.Data.Restaurant.Open {
		t.Fatalf("default %s", bytes)
	}
	for i, item := range wire.Data.Items {
		if item.ID != items[i].ID || item.Name != items[i].Name || item.Price.Amount != fmt.Sprint(i) || item.Price.Currency != "JPY" || item.Price.Exponent != 0 || item.Available != items[i].Available || item.Version != "0" {
			t.Fatalf("item %+v", item)
		}
	}
	for _, limit := range []int{1, 100} {
		cursor := ""
		seen := 0
		for {
			path := fmt.Sprintf("/v1/staff/catalog?limit=%d", limit)
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			obj := request(h, "GET", path, "", raw, 200)
			data := obj["data"].(map[string]any)
			if len(data) != 3 {
				t.Fatal(data)
			}
			for _, value := range data["items"].([]any) {
				item := value.(map[string]any)
				if len(item) != 5 || item["id"] != items[seen].ID {
					t.Fatal("dropped/duplicate item")
				}
				seen++
			}
			if data["next_cursor"] == nil {
				break
			}
			cursor = data["next_cursor"].(string)
		}
		if seen != 105 {
			t.Fatal(seen)
		}
	}
	cursor := *wire.Data.Next
	// A known ephemeral key lets the public HTTP test distinguish shape/scope
	// validation from MAC rejection, without accessing handler internals.
	sign := func(body []byte, purpose string) string {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(purpose))
		mac.Write(body)
		return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	parts := strings.Split(cursor, ".")
	cursorBody, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	const cursorPurpose = "restaurant-assistant/staff-catalog/cursor/v1\x00"
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(cursorBody, &fields); err != nil {
		t.Fatal(err)
	}
	// Preserve the production field order when changing individual signed fields.
	changed := func(field, value string) []byte {
		return []byte(strings.Replace(string(cursorBody), `"`+field+`":`+string(fields[field]), `"`+field+`":`+value, 1))
	}
	cases := []struct {
		name    string
		body    []byte
		purpose string
	}{
		{"wrong purpose", cursorBody, "another-purpose"},
		{"wrong kind", changed("kind", `"orders"`), cursorPurpose},
		{"wrong version", changed("v", `2`), cursorPurpose},
		{"wrong restaurant", changed("restaurant", `"`+other+`"`), cursorPurpose},
		{"unknown field", []byte(strings.TrimSuffix(string(cursorBody), "}") + `,"extra":true}`), cursorPurpose},
		{"duplicate field", []byte(strings.TrimSuffix(string(cursorBody), "}") + `,"v":1}`), cursorPurpose},
		{"noncanonical whitespace", append([]byte(" "), cursorBody...), cursorPurpose},
		{"noncanonical escaped JSON", changed("kind", `"\u0063atalog"`), cursorPurpose},
		{"noncanonical field order", []byte(`{"id":` + string(fields["id"]) + `,` + strings.TrimPrefix(strings.TrimSuffix(string(cursorBody), `,"id":`+string(fields["id"])+`}`), "{") + `}`), cursorPurpose},
		{"invalid uuid", changed("id", `"bad"`), cursorPurpose},
		{"uuid wrong version", changed("id", `"10000000-0000-1000-8000-000000000001"`), cursorPurpose},
		{"uuid wrong variant", changed("id", `"10000000-0000-4000-0000-000000000001"`), cursorPurpose},
		{"uuid uppercase", changed("id", `"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"`), cursorPurpose},
		{"zero timestamp", changed("time", `"0001-01-01T00:00:00Z"`), cursorPurpose},
		{"year zero", changed("time", `"0000-01-01T00:00:00Z"`), cursorPurpose},
		{"year 10000", changed("time", `"10000-01-01T00:00:00Z"`), cursorPurpose},
		{"nonUTC timestamp", changed("time", `"2026-01-01T01:00:00+01:00"`), cursorPurpose},
		{"noncanonical fraction", changed("time", `"2026-01-01T00:00:00.000000Z"`), cursorPurpose},
		{"submicrosecond", changed("time", `"2026-01-01T00:00:00.000000001Z"`), cursorPurpose},
		{"malformed timestamp", changed("time", `"infinity"`), cursorPurpose},
		{"malformed JSON", []byte(`{"v":`), cursorPurpose},
	}
	for _, field := range []string{"v", "kind", "restaurant", "time", "id"} {
		body := string(cursorBody)
		fragment := `"` + field + `":` + string(fields[field])
		if field == "id" {
			body = strings.Replace(body, ","+fragment, "", 1)
		} else {
			body = strings.Replace(body, fragment+",", "", 1)
		}
		cases = append(cases, struct {
			name    string
			body    []byte
			purpose string
		}{"missing " + field, []byte(body), cursorPurpose})
	}
	unchanged := func(t *testing.T) {
		t.Helper()
		menu, err := store.Menu(t.Context())
		if err != nil || !reflect.DeepEqual(menu, items) {
			t.Fatal("cursor rejection changed menu", err)
		}
		settings, err := store.Settings(t.Context())
		if err != nil || settings != cfg {
			t.Fatal("cursor rejection changed settings", err)
		}
		state, err := base.Inspect(t.Context(), event.ID)
		if err != nil || !reflect.DeepEqual(state, before) {
			t.Fatal("cursor rejection changed foundation/outbound state", err)
		}
		if fetches.Load() != 1 {
			t.Fatal("cursor rejection performed additional JWKS fetch")
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request(h, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(sign(tc.body, tc.purpose)), "", raw, 400)
			unchanged(t)
		})
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	signature := parts[1]
	last := strings.IndexByte(alphabet, signature[len(signature)-1])
	noncanonicalSignature := signature[:len(signature)-1] + string(alphabet[last|1])
	for _, tc := range []struct{ name, token string }{
		{"padded body", parts[0] + "=." + parts[1]},
		{"padded signature", cursor + "="},
		{"nonzero signature padding bits", parts[0] + "." + noncanonicalSignature},
		{"base64 newline", parts[0] + "\n." + parts[1]},
		{"malformed body base64", "%." + parts[1]},
		{"malformed signature base64", parts[0] + ".%"},
		{"missing signature", parts[0] + "."},
		{"extra segment", cursor + ".extra"},
		{"changed signature", parts[0] + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request(h, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(tc.token), "", raw, 400)
			unchanged(t)
		})
	}
	for _, auth := range []string{"", "forged"} {
		for _, c := range []string{cursor, sign(changed("kind", `"orders"`), cursorPurpose)} {
			request(h, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(c), "", auth, 401)
			unchanged(t)
		}
	}
	// An exactly-full final page must not invent a continuation.
	emptyStore, err := catalog.OpenStore(t.Context(), dsn, "33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatal(err)
	}
	defer emptyStore.Close()
	if err := emptyStore.Initialize(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	empty, err := staffcatalog.NewHandler(emptyStore, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	obj := request(empty, "GET", "/v1/staff/catalog", "", raw, 200)
	data := obj["data"].(map[string]any)
	if len(data["items"].([]any)) != 0 || data["next_cursor"] != nil {
		t.Fatal("empty page", data)
	}
	exactStore, err := catalog.OpenStore(t.Context(), dsn, "44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatal(err)
	}
	defer exactStore.Close()
	one := items[0]
	one.ID = "50000000-0000-4000-8000-000000000001"
	if err := exactStore.Initialize(t.Context(), cfg, []catalog.Item{one}); err != nil {
		t.Fatal(err)
	}
	exact, err := staffcatalog.NewHandler(exactStore, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	obj = request(exact, "GET", "/v1/staff/catalog?limit=1", "", raw, 200)
	data = obj["data"].(map[string]any)
	if len(data["items"].([]any)) != 1 || data["next_cursor"] != nil {
		t.Fatal("exact page", data)
	}
	request(h, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(cursor+"x"), "", raw, 400)
	wrong, err := staffcatalog.NewHandler(store, members, other, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(wrong, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(cursor), "", raw, 400)
	noKey, err := staffcatalog.NewHandler(store, members, restaurant, verifier, "")
	if err != nil {
		t.Fatal(err)
	}
	request(noKey, "GET", "/v1/staff/catalog", "", raw, 503)
	missing, err := catalog.OpenStore(t.Context(), dsn, other)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	unconfigured, err := staffcatalog.NewHandler(missing, members, other, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(unconfigured, "GET", "/v1/staff/catalog", "", raw, 503)
	unavailable, err := catalog.OpenStore(t.Context(), "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer unavailable.Close()
	down, err := staffcatalog.NewHandler(unavailable, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(down, "GET", "/v1/staff/catalog", "", raw, 503)
	downMembers, err := staff.OpenStore(t.Context(), "postgres://127.0.0.1:1/restaurant_foundation_absent?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer downMembers.Close()
	down, err = staffcatalog.NewHandler(store, downMembers, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(down, "GET", "/v1/staff/catalog", "", raw, 503)
	invalidStore, err := catalog.OpenStore(t.Context(), dsn, "66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatal(err)
	}
	defer invalidStore.Close()
	invalidItem := items[0]
	invalidItem.ID = "70000000-0000-1000-8000-000000000001"
	if err := invalidStore.Initialize(t.Context(), cfg, []catalog.Item{invalidItem}); err != nil {
		t.Fatal(err)
	}
	invalid, err := staffcatalog.NewHandler(invalidStore, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(invalid, "GET", "/v1/staff/catalog", "", raw, 500)
	badJWKS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer badJWKS.Close()
	badV, _ := staff.NewVerifier("http://127.0.0.1:3000", badJWKS.URL+"/api/auth/jwks")
	down, err = staffcatalog.NewHandler(store, members, restaurant, badV, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	request(down, "GET", "/v1/staff/catalog", "", raw, 503)
	// Fixture-only SQL targets exclusively the cluster returned by testpg.Start.
	// PostgreSQL permits these timestamps without changing any constraints.
	stampStore, err := catalog.OpenStore(t.Context(), dsn, "88888888-8888-4888-8888-888888888888")
	if err != nil {
		t.Fatal(err)
	}
	defer stampStore.Close()
	stampItems := append([]catalog.Item(nil), items[:3]...)
	for i := range stampItems {
		stampItems[i].ID = fmt.Sprintf("80000000-0000-4000-8000-%012d", i+1)
	}
	if err := stampStore.Initialize(t.Context(), cfg, stampItems); err != nil {
		t.Fatal(err)
	}
	stampHandler, err := staffcatalog.NewHandler(stampStore, members, restaurant, verifier, hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	poolConfig, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, stamp := range []string{"infinity", "-infinity", "10000-01-01 00:00:00+00"} {
		t.Run("persisted "+stamp, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), `UPDATE app.menu_items SET created_at=$1::timestamptz WHERE restaurant_id=$2`, stamp, "88888888-8888-4888-8888-888888888888"); err != nil {
				t.Fatal(err)
			}
			// limit=1 would otherwise emit a malformed timestamp as the continuation.
			p, err := stampStore.Page(t.Context(), 1, nil)
			if err == nil || err.Error() != "invalid stored catalog data" || !reflect.DeepEqual(p, catalog.Page{}) {
				t.Errorf("Page returned %+v, error %v; want safe stored-data failure", p, err)
			}
			request(stampHandler, "GET", "/v1/staff/catalog?limit=1", "", raw, 500)
			// The terminal page cannot silently accept malformed emitted positions either.
			request(stampHandler, "GET", "/v1/staff/catalog?limit=100", "", raw, 500)
			unchanged(t)
			got, err := stampStore.Menu(t.Context())
			if err != nil || !reflect.DeepEqual(got, stampItems) {
				t.Fatal("timestamp rejection changed rows", err)
			}
			if stamp != "-infinity" {
				if _, err := pool.Exec(t.Context(), `UPDATE app.menu_items SET created_at='2026-01-01T00:00:00Z'::timestamptz WHERE id=$1`, stampItems[0].ID); err != nil {
					t.Fatal(err)
				}
				// A malformed prefetched row also fails the entire page, not a clipped
				// success with a cursor that conceals the next invalid position.
				p, err := stampStore.Page(t.Context(), 1, nil)
				if err == nil || err.Error() != "invalid stored catalog data" || !reflect.DeepEqual(p, catalog.Page{}) {
					t.Errorf("malformed lookahead Page: %+v %v", p, err)
				}
				request(stampHandler, "GET", "/v1/staff/catalog?limit=1", "", raw, 500)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Page(ctx, 1, nil); err != catalog.ErrUnavailable {
		t.Fatalf("canceled Page: %v", err)
	}
	canceledRequest := httptest.NewRequest("GET", "/v1/staff/catalog", nil).WithContext(ctx)
	canceledRequest.Header.Set("Authorization", "Bearer "+raw)
	canceledResponse := httptest.NewRecorder()
	h.ServeHTTP(canceledResponse, canceledRequest)
	var canceledEnvelope map[string]any
	if err := json.Unmarshal(canceledResponse.Body.Bytes(), &canceledEnvelope); err != nil {
		t.Fatal(err)
	}
	if canceledResponse.Code != 503 || canceledResponse.Header().Get("Cache-Control") != "no-store" || canceledEnvelope["request_id"] == nil || canceledEnvelope["error"].(map[string]any)["code"] != "temporarily_unavailable" {
		t.Fatal("cancellation did not fail unavailable")
	}
	if err := members.RevokeMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	request(h, "GET", "/v1/staff/catalog?cursor="+url.QueryEscape(cursor), "", raw, 403)
	after, err := base.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("foundation changed", err)
	}
	got, err := store.Menu(t.Context())
	if err != nil || !reflect.DeepEqual(got, items) {
		t.Fatal("menu changed", err)
	}
	settings, err := store.Settings(t.Context())
	if err != nil || settings != cfg {
		t.Fatal("settings changed", err)
	}
}

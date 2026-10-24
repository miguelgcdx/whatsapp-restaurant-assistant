package staff_test

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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "12345678-1234-4234-8234-123456789abc"
const otherRestaurant = "22345678-1234-4234-8234-123456789abc"

func TestMembershipLifecycleAndNoRuntimeMigration(t *testing.T) {
	dsn := testpg.Start(t)
	s, err := staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Access(t.Context(), "alex"); err == nil {
		t.Fatal("runtime created schema")
	}
	if _, err := s.EnsureMember(t.Context(), "alex"); err == nil {
		t.Fatal("operator implicitly migrated")
	}
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	f, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	now := time.Now()
	event := foundation.Event{ID: "preserved", Sender: "fictional", Text: "local"}
	if _, err := f.Accept(t.Context(), event, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := f.CompleteJob(t.Context(), job, "local reply", now); err != nil {
		t.Fatal(err)
	}
	before, err := f.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := staff.OpenStore(t.Context(), dsn, otherRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cross.Close()
	for range 2 {
		if active, err := s.EnsureMember(t.Context(), "alex"); err != nil || !active {
			t.Fatalf("ensure %v %v", active, err)
		}
	}
	if active, err := cross.Access(t.Context(), "alex"); err != nil || active {
		t.Fatalf("cross %v %v", active, err)
	}
	if active, err := s.Access(t.Context(), "missing"); err != nil || active {
		t.Fatalf("missing %v %v", active, err)
	}
	for range 2 {
		if err := s.RevokeMember(t.Context(), "alex"); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeMember(t.Context(), "missing"); err != nil {
			t.Fatal(err)
		}
	}
	if active, err := s.EnsureMember(t.Context(), "alex"); err != nil || active {
		t.Fatalf("reactivation %v %v", active, err)
	}
	for _, subject := range []string{"", " ", strings.Repeat("x", 257)} {
		if _, err := s.EnsureMember(t.Context(), subject); err == nil {
			t.Fatal("invalid subject accepted")
		}
	}
	after, err := f.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("foundation changed: %+v %v", after, err)
	}
}
func TestStoreBoundary(t *testing.T) {
	for _, id := range []string{"bad", strings.ToUpper(restaurant), "12345678123442348234123456789abc"} {
		if s, err := staff.OpenStore(t.Context(), "postgres://test@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", id); err == nil {
			s.Close()
			t.Fatal("invalid UUID accepted")
		}
	}
	if _, err := staff.OpenStore(t.Context(), "postgres://secret-sentinel@192.0.2.1/postgres", restaurant); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("unsafe/redaction %v", err)
	}
	s, err := staff.OpenStore(t.Context(), "postgres://test@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal("not lazy", err)
	}
	s.Close()
}

type membership struct {
	active atomic.Bool
	calls  atomic.Int32
	fail   atomic.Bool
}

func (m *membership) Access(ctx context.Context, subject string) (bool, error) {
	m.calls.Add(1)
	if m.fail.Load() {
		return false, errors.New("private-sentinel")
	}
	return m.active.Load() && subject == "alex", nil
}
func signingKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	p, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return p, k
}
func token(t *testing.T, k any, alg jwt.SigningMethod, kid string, c jwt.MapClaims) string {
	t.Helper()
	v := jwt.NewWithClaims(alg, c)
	v.Header["kid"] = kid
	s, e := v.SignedString(k)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func claims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{"sub": "alex", "iss": "http://127.0.0.1:3000", "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
}
func jwks(p ed25519.PublicKey) string {
	return fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","use":"sig","kid":"known","x":"%s"}]}`, base64.RawURLEncoding.EncodeToString(p))
}
func request(t *testing.T, h http.Handler, method, path, bearer, body string, want int, expected string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	r.Header.Set("X-Auth-User-Id", "alex")
	r.Header.Set("X-Restaurant-Id", restaurant)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want || w.Body.String() != expected {
		t.Fatalf("response = %d %q; want %d %q", w.Code, w.Body.String(), want, expected)
	}
	if want == 405 && w.Header().Get("Allow") != "GET" {
		t.Fatal("missing Allow")
	}
}
func wireError(code, msg string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	return string(b) + "\n"
}
func TestHTTPJWTGuardsAndCurrentMembership(t *testing.T) {
	p, k := signingKey(t)
	_, forged := signingKey(t)
	var fetches atomic.Int32
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetches.Add(1); fmt.Fprint(w, jwks(p)) }))
	defer keys.Close()
	v, err := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	m := &membership{}
	m.active.Store(true)
	h, err := staff.NewHandler(m, restaurant, v)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	valid := token(t, k, jwt.SigningMethodEdDSA, "known", claims(now))
	unauthorized := wireError("unauthenticated", "Authentication is required.")
	cases := []struct {
		name string
		edit func(jwt.MapClaims)
		key  any
		alg  jwt.SigningMethod
		raw  string
	}{
		{name: "valid"}, {name: "anonymous", raw: "absent"}, {name: "malformed", raw: "bad.jwt"}, {name: "forged", key: forged},
		{name: "expired", edit: func(c jwt.MapClaims) {
			c["iat"] = now.Add(-time.Minute).Unix()
			c["exp"] = now.Add(-time.Second).Unix()
		}},
		{name: "issuer", edit: func(c jwt.MapClaims) { c["iss"] = "http://127.0.0.1:3001" }}, {name: "audience", edit: func(c jwt.MapClaims) { c["aud"] = "other" }},
		{name: "missing issuer", edit: func(c jwt.MapClaims) { delete(c, "iss") }}, {name: "missing audience", edit: func(c jwt.MapClaims) { delete(c, "aud") }},
		{name: "extra audience", edit: func(c jwt.MapClaims) { c["aud"] = []string{"restaurant-assistant-api", "unrelated"} }},
		{name: "future iat", edit: func(c jwt.MapClaims) {
			c["iat"] = now.Add(time.Minute).Unix()
			c["exp"] = now.Add(2 * time.Minute).Unix()
		}},
		{name: "missing iat", edit: func(c jwt.MapClaims) { delete(c, "iat") }}, {name: "missing exp", edit: func(c jwt.MapClaims) { delete(c, "exp") }},
		{name: "missing subject", edit: func(c jwt.MapClaims) { delete(c, "sub") }}, {name: "blank subject", edit: func(c jwt.MapClaims) { c["sub"] = " " }},
		{name: "long subject", edit: func(c jwt.MapClaims) { c["sub"] = strings.Repeat("x", 257) }},
		{name: "overlong", edit: func(c jwt.MapClaims) { c["exp"] = now.Add(61 * time.Second).Unix() }},
		{name: "zero life", edit: func(c jwt.MapClaims) { c["exp"] = c["iat"] }},
		{name: "wrong alg", key: []byte("fictional test key"), alg: jwt.SigningMethodHS256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := claims(now)
			if tc.edit != nil {
				tc.edit(c)
			}
			key := tc.key
			if key == nil {
				key = k
			}
			alg := tc.alg
			if alg == nil {
				alg = jwt.SigningMethodEdDSA
			}
			raw := tc.raw
			if raw == "" {
				raw = token(t, key, alg, "known", c)
			}
			if raw == "absent" {
				raw = ""
			}
			before := m.calls.Load()
			if tc.name == "valid" {
				request(t, h, "GET", "/v1/staff/access", raw, "", 200, `{"restaurant_id":"`+restaurant+`"}`+"\n")
			} else {
				request(t, h, "GET", "/v1/staff/access", raw, "", 401, unauthorized)
				if m.calls.Load() != before {
					t.Fatal("invalid token reached membership")
				}
			}
		})
	}
	request(t, h, "GET", "/v1/staff/access?restaurant_id="+otherRestaurant, valid, "", 400, wireError("invalid_input", "Invalid request."))
	request(t, h, "GET", "/v1/staff/access", valid, "{}", 400, wireError("invalid_input", "Invalid request."))
	request(t, h, "POST", "/v1/staff/access", valid, "", 405, wireError("method_not_allowed", "The method is not allowed."))
	m.active.Store(false)
	request(t, h, "GET", "/v1/staff/access", valid, "", 403, wireError("forbidden", "Staff access is not available."))
	m.active.Store(true)
	m.fail.Store(true)
	request(t, h, "GET", "/v1/staff/access", valid, "", 503, wireError("temporarily_unavailable", "Staff access is temporarily unavailable."))
	if fetches.Load() != 1 {
		t.Fatalf("cache fetches %d", fetches.Load())
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, e := v.Verify(t.Context(), token(t, k, jwt.SigningMethodEdDSA, "unknown", claims(now)))
			if !errors.Is(e, staff.ErrUnauthenticated) {
				t.Errorf("unknown kid: %v", e)
			}
		})
	}
	wg.Wait()
	if fetches.Load() > 2 {
		t.Fatalf("unknown kid amplification %d", fetches.Load())
	}
}
func TestJWKSFailureAndConfiguration(t *testing.T) {
	for _, origin := range []string{"https://example.com", "http://localhost:3000", "http://user@127.0.0.1:3000", "http://127.0.0.1:3000?x=1"} {
		if _, e := staff.NewVerifier(origin, "http://127.0.0.1:3000/api/auth/jwks"); e == nil {
			t.Fatal("unsafe issuer", origin)
		}
	}
	for _, url := range []string{"http://example.com/api/auth/jwks", "http://127.0.0.1:3000/wrong", "http://127.0.0.1:3000/api/auth/jwks?x=1"} {
		if _, e := staff.NewVerifier("http://127.0.0.1:3000", url); e == nil {
			t.Fatal("unsafe JWKS", url)
		}
	}
	p, k := signingKey(t)
	keyJSON := strings.TrimSuffix(strings.TrimPrefix(jwks(p), `{"keys":[`), `]}`)
	for _, body := range []string{`{}`, `not json`, strings.Repeat("x", 65537), strings.Replace(jwks(p), "Ed25519", "X25519", 1), strings.Replace(jwks(p), `"OKP"`, `"RSA"`, 1), `{"keys":[` + keyJSON + `,` + keyJSON + `]}`, `{"keys":[` + strings.Repeat(keyJSON+`,`, 32) + keyJSON + `]}`, strings.Replace(jwks(p), `"sig"`, `"enc"`, 1), strings.Replace(jwks(p), `"EdDSA"`, `"HS256"`, 1), strings.Replace(jwks(p), `"kty":`, `"d":"private-sentinel","kty":`, 1)} {
		t.Run(fmt.Sprint(len(body), body[:min(len(body), 12)]), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			v, e := staff.NewVerifier("http://127.0.0.1:3000", srv.URL+"/api/auth/jwks")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = v.Verify(t.Context(), token(t, k, jwt.SigningMethodEdDSA, "known", claims(time.Now()))); !errors.Is(e, staff.ErrUnavailable) {
				t.Fatalf("dependency failure %v", e)
			}
		})
	}
	var target atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { target.Add(1) }))
	defer dest.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer redirect.Close()
	v, e := staff.NewVerifier("http://127.0.0.1:3000", redirect.URL+"/api/auth/jwks")
	if e != nil {
		t.Fatal(e)
	}
	_, e = v.Verify(t.Context(), token(t, k, jwt.SigningMethodEdDSA, "known", claims(time.Now())))
	if !errors.Is(e, staff.ErrUnavailable) || target.Load() != 0 {
		t.Fatal("redirect followed", e)
	}
}

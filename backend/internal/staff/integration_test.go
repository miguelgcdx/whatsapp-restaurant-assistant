package staff_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestConcurrentColdCacheAndCancellation(t *testing.T) {
	p, k := signingKey(t)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetches.Add(1); fmt.Fprint(w, jwks(p)) }))
	defer srv.Close()
	v, e := staff.NewVerifier("http://127.0.0.1:3000", srv.URL+"/api/auth/jwks")
	if e != nil {
		t.Fatal(e)
	}
	raw := token(t, k, jwt.SigningMethodEdDSA, "known", claims(time.Now()))
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			subject, e := v.Verify(t.Context(), raw)
			if e != nil || subject != "alex" {
				t.Errorf("verify %q %v", subject, e)
			}
		})
	}
	wg.Wait()
	if fetches.Load() != 1 {
		t.Fatalf("cold-cache fetches %d", fetches.Load())
	}
	fresh, e := staff.NewVerifier("http://127.0.0.1:3000", srv.URL+"/api/auth/jwks")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := fresh.Verify(ctx, raw); !errors.Is(e, staff.ErrUnavailable) {
		t.Fatalf("canceled fetch = %v", e)
	}
	if fetches.Load() != 1 {
		t.Fatal("canceled request performed network I/O")
	}
}

func TestHTTPStoreMembershipAndFoundationPreservation(t *testing.T) {
	dsn := testpg.Start(t)
	if e := postgres.Migrate(t.Context(), dsn); e != nil {
		t.Fatal(e)
	}
	f, e := postgres.Open(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	now := time.Now()
	event := foundation.Event{ID: "protected-denials", Sender: "fictional", Text: "preserve"}
	if _, e := f.Accept(t.Context(), event, now); e != nil {
		t.Fatal(e)
	}
	job, ok, e := f.ClaimJob(t.Context(), now, time.Minute)
	if e != nil || !ok {
		t.Fatal(e)
	}
	if e := f.CompleteJob(t.Context(), job, "preserved outbox", now); e != nil {
		t.Fatal(e)
	}
	before, e := f.Inspect(t.Context(), event.ID)
	if e != nil {
		t.Fatal(e)
	}
	s, e := staff.OpenStore(t.Context(), dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	cross, e := staff.OpenStore(t.Context(), dsn, otherRestaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer cross.Close()
	if _, e := cross.EnsureMember(t.Context(), "alex"); e != nil {
		t.Fatal(e)
	}
	p, k := signingKey(t)
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jwks(p)) }))
	defer keys.Close()
	v, e := staff.NewVerifier("http://127.0.0.1:3000", keys.URL+"/api/auth/jwks")
	if e != nil {
		t.Fatal(e)
	}
	h, e := staff.NewHandler(s, restaurant, v)
	if e != nil {
		t.Fatal(e)
	}
	raw := token(t, k, jwt.SigningMethodEdDSA, "known", claims(now))
	forbidden := wireError("forbidden", "Staff access is not available.")
	request(t, h, "GET", "/v1/staff/access", "", "", 401, wireError("unauthenticated", "Authentication is required."))
	// The valid identity is a member of another restaurant only. Spoofed caller
	// identity/restaurant headers in request() confer no authority.
	request(t, h, "GET", "/v1/staff/access", raw, "", 403, forbidden)
	if _, e := s.EnsureMember(t.Context(), "alex"); e != nil {
		t.Fatal(e)
	}
	request(t, h, "GET", "/v1/staff/access", raw, "", 200, `{"restaurant_id":"`+restaurant+`"}`+"\n")
	if e := s.RevokeMember(t.Context(), "alex"); e != nil {
		t.Fatal(e)
	}
	request(t, h, "GET", "/v1/staff/access", raw, "", 403, forbidden)
	if _, e := s.EnsureMember(t.Context(), "alex"); e != nil {
		t.Fatal(e)
	}
	request(t, h, "GET", "/v1/staff/access", raw, "", 403, forbidden)
	after, e := f.Inspect(t.Context(), event.ID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("denials modified foundation: %+v %v", after, e)
	}
}

package staff_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/staff"
)

// S1: enforce the signed lifetime, not its whole-second truncation.
func TestIndependentFractionalLifetime(t *testing.T) {
	p, k := signingKey(t)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, jwks(p))
		},
	))
	defer srv.Close()

	v, err := staff.NewVerifier(
		"http://127.0.0.1:3000",
		srv.URL+"/api/auth/jwks",
	)
	if err != nil {
		t.Fatal(err)
	}
	m := &membership{}
	m.active.Store(true)
	h, err := staff.NewHandler(m, restaurant, v)
	if err != nil {
		t.Fatal(err)
	}

	c := claims(time.Now())
	base := time.Now().Add(-2 * time.Second).Unix()
	c["iat"] = float64(base) + 0.01
	c["exp"] = float64(base) + 60.99
	raw := token(t, k, jwt.SigningMethodEdDSA, "known", c)

	request(t, h, "GET", "/v1/staff/access", raw, "", 401,
		wireError("unauthenticated", "Authentication is required."))
	if m.calls.Load() != 0 {
		t.Fatal("overlong signed lifetime reached membership")
	}

	// Valid fractional dates must remain accepted; tightening is not integer-only.
	precision := jwt.TimePrecision
	c["iat"] = float64(base) + 0.125
	c["exp"] = float64(base) + 60.125
	raw = token(t, k, jwt.SigningMethodEdDSA, "known", c)
	request(t, h, "GET", "/v1/staff/access", raw, "", 200,
		fmt.Sprintf("{\"restaurant_id\":%q}\n", restaurant))
	if m.calls.Load() != 1 || jwt.TimePrecision != precision {
		t.Fatal("valid fractional dates or shared parser precision changed")
	}
}

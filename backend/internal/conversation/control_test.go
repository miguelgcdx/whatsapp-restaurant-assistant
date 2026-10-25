package conversation_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "12345678-1234-4234-8234-123456789abc"
const otherRestaurant = "22345678-1234-4234-8234-123456789abc"
const issuer = "http://127.0.0.1:3000"

type auth struct {
	key      ed25519.PrivateKey
	verifier *staff.Verifier
}

func newAuth(t *testing.T) auth {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"owned","x":"%s"}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(server.Close)
	v, err := staff.NewVerifier(issuer, server.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	return auth{key, v}
}
func (a auth) token(t *testing.T, subject string, edit func(jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	c := jwt.MapClaims{"sub": subject, "iss": issuer, "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
	if edit != nil {
		edit(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["kid"] = "owned"
	raw, err := tok.SignedString(a.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fixture struct {
	dsn     string
	auth    auth
	store   *conversation.Store
	members *staff.Store
	cart    *cart.Store
	conn    *pgx.Conn
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	a := newAuth(t)
	f := &fixture{dsn: dsn, auth: a}
	var err error
	f.members, err = staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.members.Close)
	for _, subject := range []string{"alex", "blair"} {
		if ok, err := f.members.EnsureMember(t.Context(), subject); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	cat, err := catalog.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if err := cat.Initialize(t.Context(), catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, nil); err != nil {
		t.Fatal(err)
	}
	f.cart, err = cart.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.cart.Close)
	f.store, err = conversation.OpenStore(t.Context(), dsn+"&application_name=control-test", restaurant, a.verifier)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.store.Close)
	f.conn, err = pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.conn.Close(context.Background()) })
	return f
}
func (f *fixture) id(t *testing.T) cart.ConversationID {
	t.Helper()
	local, err := f.cart.ProvisionLocal(t.Context(), "owned", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	return local.ConversationID()
}
func (f *fixture) counts(t *testing.T) [2]int64 {
	t.Helper()
	var n [2]int64
	if err := f.conn.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM app.conversation_controls),(SELECT count(*) FROM app.conversation_control_operations)`).Scan(&n[0], &n[1]); err != nil {
		t.Fatal(err)
	}
	return n
}
func want(t *testing.T, got conversation.State, err error, id cart.ConversationID, mode conversation.Mode, version int64) conversation.State {
	t.Helper()
	expected := conversation.State{ConversationID: id, Mode: mode, Version: version}
	if err != nil || got != expected {
		t.Fatalf("state %+v error %v; want %+v", got, err, expected)
	}
	return got
}
func rejected(t *testing.T, state conversation.State, err, expected error) {
	t.Helper()
	if state != (conversation.State{}) || !errors.Is(err, expected) {
		t.Fatalf("rejection %+v %v; want zero/%v", state, err, expected)
	}
}
func TestControl(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	token := f.auth.token(t, "alex", nil)
	t.Run("explicit lifecycle and immutable history", func(t *testing.T) {
		id := f.id(t)
		before := f.counts(t)
		s, e := f.store.Read(ctx, token, id)
		rejected(t, s, e, conversation.ErrNotFound)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeAI, 0, "missing")
		rejected(t, s, e, conversation.ErrNotFound)
		if f.counts(t) != before {
			t.Fatal("implicit control or reserved key")
		}
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeStaff, "missing")
		initial := want(t, s, e, id, conversation.ModeStaff, 0)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "same")
		first := want(t, s, e, id, conversation.ModeStaff, 1)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeAI, 1, "next")
		want(t, s, e, id, conversation.ModeAI, 2)
		counts := f.counts(t)
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeStaff, "missing")
		if e != nil || s != initial {
			t.Fatal(s, e)
		}
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "same")
		if e != nil || s != first {
			t.Fatal(s, e)
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 2)
		if f.counts(t) != counts {
			t.Fatal("replay wrote ledger")
		}
	})
	t.Run("whole intent namespace and rejected key reuse", func(t *testing.T) {
		id, second := f.id(t), f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "namespace")
		want(t, s, e, id, conversation.ModeAI, 0)
		s, e = f.store.Initialize(ctx, token, second, conversation.ModeAI, "second-init")
		want(t, s, e, second, conversation.ModeAI, 0)
		before := f.counts(t)
		calls := []func() (conversation.State, error){
			func() (conversation.State, error) {
				return f.store.Initialize(ctx, token, id, conversation.ModeStaff, "namespace")
			},
			func() (conversation.State, error) {
				return f.store.Initialize(ctx, token, second, conversation.ModeAI, "namespace")
			},
			func() (conversation.State, error) {
				return f.store.SetMode(ctx, token, id, conversation.ModeAI, 0, "namespace")
			},
			func() (conversation.State, error) {
				return f.store.Initialize(ctx, token, id, conversation.ModeAI, "fresh-init")
			},
		}
		for _, call := range calls {
			s, e := call()
			rejected(t, s, e, conversation.ErrConflict)
		}
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 9, "reuse")
		rejected(t, s, e, conversation.ErrStale)
		if f.counts(t) != before {
			t.Fatal("rejection recorded")
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 0)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "reuse")
		want(t, s, e, id, conversation.ModeStaff, 1)
		before = f.counts(t)
		for _, call := range []func() (conversation.State, error){
			func() (conversation.State, error) {
				return f.store.SetMode(ctx, token, id, conversation.ModeAI, 0, "reuse")
			},
			func() (conversation.State, error) {
				return f.store.SetMode(ctx, token, id, conversation.ModeStaff, 1, "reuse")
			},
			func() (conversation.State, error) {
				return f.store.SetMode(ctx, token, second, conversation.ModeStaff, 0, "reuse")
			},
		} {
			s, e := call()
			rejected(t, s, e, conversation.ErrConflict)
		}
		if f.counts(t) != before {
			t.Fatal("intent conflict wrote")
		}
		blair := f.auth.token(t, "blair", nil)
		s, e = f.store.SetMode(ctx, blair, id, conversation.ModeAI, 1, "reuse")
		want(t, s, e, id, conversation.ModeAI, 2)
		s, e = f.store.Initialize(ctx, token, second, conversation.ModeAI, "fresh-init")
		rejected(t, s, e, conversation.ErrConflict)
		// A key rejected on one initialized conversation remains usable on a fresh one.
		third := f.id(t)
		s, e = f.store.Initialize(ctx, token, third, conversation.ModeAI, "fresh-init")
		want(t, s, e, third, conversation.ModeAI, 0)
	})
	t.Run("input before authentication and no writes", func(t *testing.T) {
		id := f.id(t)
		before := f.counts(t)
		for _, mode := range []conversation.Mode{"", "AI", "other"} {
			s, e := f.store.Initialize(ctx, "bad", id, mode, "key")
			rejected(t, s, e, conversation.ErrInput)
			s, e = f.store.SetMode(ctx, "bad", id, mode, 0, "key")
			rejected(t, s, e, conversation.ErrInput)
		}
		for _, key := range []string{"", " ", "a/b", "é", "a\n", strings.Repeat("a", 129)} {
			s, e := f.store.Initialize(ctx, "bad", id, conversation.ModeAI, key)
			rejected(t, s, e, conversation.ErrInput)
			s, e = f.store.SetMode(ctx, "bad", id, conversation.ModeAI, 0, key)
			rejected(t, s, e, conversation.ErrInput)
		}
		s, e := f.store.SetMode(ctx, "bad", id, conversation.ModeAI, -1, "key")
		rejected(t, s, e, conversation.ErrInput)
		s, e = f.store.Read(ctx, token, cart.ConversationID{})
		rejected(t, s, e, conversation.ErrInput)
		s, e = f.store.Read(nil, token, id)
		rejected(t, s, e, conversation.ErrInput)
		var absent *conversation.Store
		s, e = absent.Read(ctx, token, id)
		rejected(t, s, e, conversation.ErrInput)
		if f.counts(t) != before {
			t.Fatal("input wrote")
		}
		key := strings.Repeat("Z", 122) + "._:-09"
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeAI, key)
		want(t, s, e, id, conversation.ModeAI, 0)
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeAI, key)
		want(t, s, e, id, conversation.ModeAI, 0)
	})
	t.Run("concrete token guards and current membership on replay", func(t *testing.T) {
		id := f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "auth-init")
		want(t, s, e, id, conversation.ModeAI, 0)
		bad := []string{"", "bad.jwt", "Bearer " + token}
		for _, edit := range []func(jwt.MapClaims){
			func(c jwt.MapClaims) {
				c["iat"] = time.Now().Add(-time.Minute).Unix()
				c["exp"] = time.Now().Add(-time.Second).Unix()
			},
			func(c jwt.MapClaims) { c["iss"] = "http://127.0.0.1:3001" },
			func(c jwt.MapClaims) { c["aud"] = "elsewhere" },
		} {
			bad = append(bad, f.auth.token(t, "alex", edit))
		}
		_, forged, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		foreignAuth := f.auth
		foreignAuth.key = forged
		bad = append(bad, foreignAuth.token(t, "alex", nil))
		hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "alex"})
		hs.Header["kid"] = "owned"
		raw, err := hs.SignedString([]byte("owned fictional secret"))
		if err != nil {
			t.Fatal(err)
		}
		bad = append(bad, raw)
		before := f.counts(t)
		for _, raw := range bad {
			s, e := f.store.SetMode(ctx, raw, id, conversation.ModeStaff, 0, "auth-reuse")
			rejected(t, s, e, staff.ErrUnauthenticated)
			s, e = f.store.Read(ctx, raw, id)
			rejected(t, s, e, staff.ErrUnauthenticated)
			s, e = f.store.Initialize(ctx, raw, id, conversation.ModeAI, "auth-init")
			rejected(t, s, e, staff.ErrUnauthenticated)
		}
		missing := f.auth.token(t, "missing", nil)
		s, e = f.store.Read(ctx, missing, id)
		rejected(t, s, e, conversation.ErrForbidden)
		cross, err := staff.OpenStore(ctx, f.dsn, otherRestaurant)
		if err != nil {
			t.Fatal(err)
		}
		defer cross.Close()
		if ok, err := cross.EnsureMember(ctx, "only-other"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		s, e = f.store.Read(ctx, f.auth.token(t, "only-other", nil), id)
		rejected(t, s, e, conversation.ErrForbidden)
		if err := f.members.RevokeMember(ctx, "alex"); err != nil {
			t.Fatal(err)
		}
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeAI, "auth-init")
		rejected(t, s, e, conversation.ErrForbidden)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "auth-reuse")
		rejected(t, s, e, conversation.ErrForbidden)
		s, e = f.store.Read(ctx, token, id)
		rejected(t, s, e, conversation.ErrForbidden)
		if f.counts(t) != before {
			t.Fatal("unauthorized wrote")
		}
		// Restore only fixture membership, never via the control service.
		if _, err := f.conn.Exec(ctx, `UPDATE app.staff_memberships SET active=true WHERE restaurant_id=$1 AND auth_user_id='alex'`, restaurant); err != nil {
			t.Fatal(err)
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 0)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "auth-reuse")
		want(t, s, e, id, conversation.ModeStaff, 1)
	})
	t.Run("foreign and absent scoped context", func(t *testing.T) {
		id := f.id(t)
		other, err := conversation.OpenStore(ctx, f.dsn, otherRestaurant, f.auth.verifier)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		before := f.counts(t)
		for _, target := range []cart.ConversationID{id, mustID(t, "90000000-0000-4000-8000-000000000001")} {
			s, e := other.Initialize(ctx, token, target, conversation.ModeAI, "scope")
			rejected(t, s, e, conversation.ErrForbidden)
			s, e = f.store.Read(ctx, token, mustID(t, "90000000-0000-4000-8000-000000000001"))
			rejected(t, s, e, conversation.ErrNotFound)
		}
		cross, err := staff.OpenStore(ctx, f.dsn, otherRestaurant)
		if err != nil {
			t.Fatal(err)
		}
		defer cross.Close()
		if ok, err := cross.EnsureMember(ctx, "alex"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		s, e := other.Initialize(ctx, token, id, conversation.ModeAI, "scope")
		rejected(t, s, e, conversation.ErrNotFound)
		s, e = other.Read(ctx, token, id)
		rejected(t, s, e, conversation.ErrNotFound)
		s, e = f.store.SetMode(ctx, token, mustID(t, "90000000-0000-4000-8000-000000000001"), conversation.ModeAI, 0, "scope")
		rejected(t, s, e, conversation.ErrNotFound)
		if f.counts(t) != before {
			t.Fatal("scope wrote")
		}
		s, e = f.store.Initialize(ctx, token, id, conversation.ModeAI, "scope")
		want(t, s, e, id, conversation.ModeAI, 0)
	})
	t.Run("max version and zero errors", func(t *testing.T) {
		id := f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "max-init")
		want(t, s, e, id, conversation.ModeAI, 0)
		if _, err := f.conn.Exec(ctx, `UPDATE app.conversation_controls SET version=$1 WHERE restaurant_id=$2 AND conversation_id=$3`, int64(math.MaxInt64), restaurant, id.String()); err != nil {
			t.Fatal(err)
		}
		before := f.counts(t)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, math.MaxInt64, "max")
		rejected(t, s, e, conversation.ErrStale)
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, math.MaxInt64)
		if f.counts(t) != before {
			t.Fatal("overflow wrote")
		}
	})
	t.Run("concurrent replay and competing CAS", func(t *testing.T) {
		id := f.id(t)
		start := make(chan struct{})
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "concurrent-init")
				results <- outcome{s, e}
			}()
		}
		close(start)
		for range 2 {
			o := join(t, results)
			want(t, o.state, o.err, id, conversation.ModeAI, 0)
		}
		start = make(chan struct{})
		before := f.counts(t)
		for range 2 {
			go func() {
				<-start
				s, e := f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "concurrent-same")
				results <- outcome{s, e}
			}()
		}
		close(start)
		for range 2 {
			o := join(t, results)
			want(t, o.state, o.err, id, conversation.ModeStaff, 1)
		}
		after := f.counts(t)
		if after != ([2]int64{before[0], before[1] + 1}) {
			t.Fatal(before, after)
		}
		start = make(chan struct{})
		before = after
		for _, key := range []string{"race-a", "race-b"} {
			go func() {
				<-start
				s, e := f.store.SetMode(ctx, token, id, conversation.ModeAI, 1, key)
				results <- outcome{s, e}
			}()
		}
		close(start)
		winners := 0
		for range 2 {
			o := join(t, results)
			if o.err == nil {
				want(t, o.state, o.err, id, conversation.ModeAI, 2)
				winners++
			} else {
				rejected(t, o.state, o.err, conversation.ErrStale)
			}
		}
		if winners != 1 || f.counts(t) != ([2]int64{before[0], before[1] + 1}) {
			t.Fatal("CAS did not select one commit")
		}
		s, e := f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 2)
	})
	t.Run("revocation after blocked restaurant applies to replay", func(t *testing.T) {
		id := f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "blocked-init")
		want(t, s, e, id, conversation.ModeAI, 0)
		before := f.counts(t)
		tx, err := f.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err = tx.Exec(ctx, `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant); err != nil {
			t.Fatal(err)
		}
		results := make(chan outcome, 1)
		go func() {
			s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "blocked-init")
			results <- outcome{s, e}
		}()
		waitBlocked(t, f.dsn, "control-test")
		if err := f.members.RevokeMember(ctx, "alex"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		o := join(t, results)
		rejected(t, o.state, o.err, conversation.ErrForbidden)
		if f.counts(t) != before {
			t.Fatal("revoked replay wrote")
		}
		if _, err := f.conn.Exec(ctx, `UPDATE app.staff_memberships SET active=true WHERE restaurant_id=$1 AND auth_user_id='alex'`, restaurant); err != nil {
			t.Fatal(err)
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 0)
	})
	t.Run("membership lock holds until commit", func(t *testing.T) {
		id := f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "hold-init")
		want(t, s, e, id, conversation.ModeAI, 0)
		tx, err := f.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err = tx.Exec(ctx, `SELECT id FROM app.cart_conversations WHERE id=$1 FOR UPDATE`, id.String()); err != nil {
			t.Fatal(err)
		}
		results := make(chan outcome, 1)
		go func() {
			s, e := f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "held")
			results <- outcome{s, e}
		}()
		waitBlocked(t, f.dsn, "control-test")
		revokeCtx, revokeCancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer revokeCancel()
		revoker, err := pgx.Connect(revokeCtx, f.dsn+"&application_name=control-revoke")
		if err != nil {
			t.Fatal(err)
		}
		revoked := make(chan error, 1)
		revokeDone := make(chan struct{})
		go func() {
			defer close(revokeDone)
			_, e := revoker.Exec(revokeCtx, `UPDATE app.staff_memberships SET active=false WHERE restaurant_id=$1 AND auth_user_id='alex'`, restaurant)
			revoked <- e
		}()
		defer func() {
			// Unblock and join even when a positive-wait assertion fails. Never
			// close a pgx connection concurrently with its pending statement.
			revokeCancel()
			rollback(tx)
			select {
			case <-revokeDone:
				closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = revoker.Close(closeCtx)
			case <-time.After(time.Second):
				t.Error("revocation cleanup failed to join")
			}
		}()
		waitBlocked(t, f.dsn, "control-revoke")
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		o := join(t, results)
		want(t, o.state, o.err, id, conversation.ModeStaff, 1)
		select {
		case err := <-revoked:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("revocation did not join")
		}
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "held")
		rejected(t, s, e, conversation.ErrForbidden)
		if _, err := f.conn.Exec(ctx, `UPDATE app.staff_memberships SET active=true WHERE restaurant_id=$1 AND auth_user_id='alex'`, restaurant); err != nil {
			t.Fatal(err)
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeStaff, 1)
	})
	t.Run("cancellation earlier and local caps preserve state", func(t *testing.T) {
		id := f.id(t)
		s, e := f.store.Initialize(ctx, token, id, conversation.ModeAI, "deadline-init")
		want(t, s, e, id, conversation.ModeAI, 0)
		before := f.counts(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		s, e = f.store.Read(canceled, token, id)
		rejected(t, s, e, context.Canceled)
		tx, err := f.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err = tx.Exec(ctx, `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant); err != nil {
			t.Fatal(err)
		}
		for _, duration := range []time.Duration{100 * time.Millisecond, 8 * time.Second} {
			deadline, cancel := context.WithTimeout(ctx, duration)
			start := time.Now()
			s, e = f.store.SetMode(deadline, token, id, conversation.ModeStaff, 0, "deadline-reuse")
			cancel()
			rejected(t, s, e, context.DeadlineExceeded)
			elapsed := time.Since(start)
			if duration == 8*time.Second && (elapsed < 4*time.Second || elapsed > 6500*time.Millisecond) {
				t.Fatalf("local cap %v", elapsed)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if f.counts(t) != before {
			t.Fatal("canceled request wrote")
		}
		s, e = f.store.Read(ctx, token, id)
		want(t, s, e, id, conversation.ModeAI, 0)
		s, e = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "deadline-reuse")
		want(t, s, e, id, conversation.ModeStaff, 1)
	})
	t.Run("closed pool is sanitized", func(t *testing.T) {
		id := f.id(t)
		s, err := conversation.OpenStore(ctx, f.dsn, restaurant, f.auth.verifier)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		before := f.counts(t)
		state, err := s.Read(ctx, token, id)
		rejected(t, state, err, conversation.ErrUnavailable)
		if f.counts(t) != before {
			t.Fatal("closed pool wrote")
		}
	})
}
func mustID(t *testing.T, raw string) cart.ConversationID {
	t.Helper()
	id, err := cart.ParseConversationID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type outcome struct {
	state conversation.State
	err   error
}

func join(t *testing.T, ch <-chan outcome) outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(7 * time.Second):
		t.Fatal("operation failed to join")
		return outcome{}
	}
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func waitBlocked(t *testing.T, dsn, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for {
		var blocked bool
		err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0)`, name).Scan(&blocked)
		if err != nil {
			t.Fatal("positive lock observation", err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("no positive lock observation")
		case <-time.After(time.Millisecond):
		}
	}
}

// unpublishedDeadline models the scheduling window between a known deadline
// elapsing and its cancellation becoming observable through Done/Err. Deadline
// stays constant; the embedded cancelable context publishes only at cleanup.
// No production hook or alternate verifier/DB implementation is involved.
type unpublishedDeadline struct {
	context.Context
	deadline time.Time
}

func (c unpublishedDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestDeadlineBeforeCancellationPublication(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	token := f.auth.token(t, "alex", nil)
	id := f.id(t)
	s, err := f.store.Initialize(ctx, token, id, conversation.ModeAI, "publication-init")
	initial := want(t, s, err, id, conversation.ModeAI, 0)
	before := f.counts(t)

	tx, err := f.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant); err != nil {
		t.Fatal(err)
	}
	parent, publish := context.WithCancel(ctx)
	defer publish()
	delayed := unpublishedDeadline{Context: parent, deadline: time.Now().Add(2 * time.Second)}
	results := make(chan outcome, 1)
	go func() {
		s, err := f.store.SetMode(delayed, token, id, conversation.ModeStaff, 0, "publication-reuse")
		results <- outcome{s, err}
	}()
	waitBlocked(t, f.dsn, "control-test")
	// A deadline barrier, not a sleep-based race assertion: the owned query is
	// positively blocked, and Err/Done publication remains under fixture control.
	timer := time.NewTimer(time.Until(delayed.deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if delayed.Err() != nil {
		t.Fatal("fixture unexpectedly published cancellation")
	}
	observer, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	var canceled bool
	if err := observer.QueryRow(ctx, `SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name='control-test' AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0`).Scan(&canceled); err != nil || !canceled {
		t.Fatalf("cancel owned blocked statement: acknowledged=%v error=%v", canceled, err)
	}
	o := join(t, results)
	// Keep post-rejection assertions observable even on the original RED.
	if o.state != (conversation.State{}) || !errors.Is(o.err, context.DeadlineExceeded) {
		t.Errorf("elapsed deadline before publication: got %+v %v; want zero/context deadline exceeded", o.state, o.err)
	}
	if delayed.Err() != nil || time.Now().Before(delayed.deadline) {
		t.Fatal("deadline/publication ordering was not exercised")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = f.store.Read(ctx, token, id)
	want(t, s, err, id, conversation.ModeAI, 0)
	s, err = f.store.Initialize(ctx, token, id, conversation.ModeAI, "publication-init")
	if err != nil || s != initial || f.counts(t) != before {
		t.Fatal("deadline rejection changed state, historical replay, or ledger", s, err)
	}
	s, err = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "publication-reuse")
	want(t, s, err, id, conversation.ModeStaff, 1)
	s, err = f.store.SetMode(ctx, token, id, conversation.ModeStaff, 0, "publication-reuse")
	want(t, s, err, id, conversation.ModeStaff, 1)
	if f.counts(t) != ([2]int64{before[0], before[1] + 1}) {
		t.Fatal("reused key did not commit exactly one operation")
	}

	// Expiry must not override an already observable explicit cancellation.
	canceledParent, cancel := context.WithCancel(ctx)
	cancel()
	s, err = f.store.Read(unpublishedDeadline{Context: canceledParent, deadline: delayed.deadline}, token, id)
	rejected(t, s, err, context.Canceled)
	// A live deadline does not turn a genuine dependency failure into expiry.
	closed, err := conversation.OpenStore(ctx, f.dsn, restaurant, f.auth.verifier)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	s, err = closed.Read(unpublishedDeadline{Context: ctx, deadline: time.Now().Add(time.Minute)}, token, id)
	rejected(t, s, err, conversation.ErrUnavailable)
	s, err = f.store.Read(ctx, token, id)
	want(t, s, err, id, conversation.ModeStaff, 1)
	if f.counts(t) != ([2]int64{before[0], before[1] + 1}) {
		t.Fatal("cancellation/unavailability changed ledger")
	}

	// All public entry points also reject an already elapsed deadline while
	// publication is pending; initialization cannot bootstrap or reserve a key.
	fresh := f.id(t)
	expired := unpublishedDeadline{Context: ctx, deadline: delayed.deadline}
	before = f.counts(t)
	s, err = f.store.Initialize(expired, token, fresh, conversation.ModeAI, "entry-reuse")
	rejected(t, s, err, context.DeadlineExceeded)
	s, err = f.store.SetMode(expired, token, id, conversation.ModeAI, 1, "entry-set-reuse")
	rejected(t, s, err, context.DeadlineExceeded)
	s, err = f.store.Read(expired, token, id)
	rejected(t, s, err, context.DeadlineExceeded)
	s, err = f.store.Read(ctx, token, id)
	want(t, s, err, id, conversation.ModeStaff, 1)
	s, err = f.store.Read(ctx, token, fresh)
	rejected(t, s, err, conversation.ErrNotFound)
	if f.counts(t) != before {
		t.Fatal("expired entry points wrote state or ledger")
	}
	s, err = f.store.Initialize(ctx, token, fresh, conversation.ModeAI, "entry-reuse")
	want(t, s, err, fresh, conversation.ModeAI, 0)
	s, err = f.store.SetMode(ctx, token, id, conversation.ModeAI, 1, "entry-set-reuse")
	want(t, s, err, id, conversation.ModeAI, 2)
}

func TestConstructor(t *testing.T) {
	a := newAuth(t)
	dsn := testpg.Start(t)
	for _, id := range []string{"", "bad", strings.ToUpper(restaurant), strings.ReplaceAll(restaurant, "-", "")} {
		s, e := conversation.OpenStore(t.Context(), dsn, id, a.verifier)
		if s != nil || !errors.Is(e, conversation.ErrInput) {
			t.Fatal(s, e)
		}
	}
	for _, tc := range []struct {
		ctx context.Context
		dsn string
		v   *staff.Verifier
	}{
		{nil, dsn, a.verifier}, {t.Context(), dsn, nil}, {t.Context(), "postgres://secret-sentinel@192.0.2.1/postgres", a.verifier},
	} {
		s, e := conversation.OpenStore(tc.ctx, tc.dsn, restaurant, tc.v)
		if s != nil || !errors.Is(e, conversation.ErrInput) {
			t.Fatal(s, e)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, e := conversation.OpenStore(ctx, dsn, restaurant, a.verifier)
	if s != nil || !errors.Is(e, context.Canceled) {
		t.Fatal(s, e)
	}
	s, e = conversation.OpenStore(t.Context(), dsn+"&application_name=control-constructor", restaurant, a.verifier)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	observer, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(t.Context())
	var connections int64
	if err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE application_name='control-constructor'`).Scan(&connections); err != nil || connections != 0 {
		t.Fatalf("constructor opened database connections: count=%d error=%v", connections, err)
	}
}
func TestAuthenticationDependencyAndContext(t *testing.T) {
	a := newAuth(t)
	dsn := testpg.Start(t)
	raw := a.token(t, "alex", nil)
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			entered := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				if canceled {
					<-r.Context().Done()
				} else {
					w.WriteHeader(503)
				}
			}))
			defer srv.Close()
			v, e := staff.NewVerifier(issuer, srv.URL+"/api/auth/jwks")
			if e != nil {
				t.Fatal(e)
			}
			store, e := conversation.OpenStore(t.Context(), dsn, restaurant, v)
			if e != nil {
				t.Fatal(e)
			}
			// A closed owned pool makes transaction-before-auth regressions fail
			// without allowing a connection attempt to an unowned endpoint.
			store.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			results := make(chan outcome, 1)
			go func() {
				s, e := store.Read(ctx, raw, mustID(t, "90000000-0000-4000-8000-000000000001"))
				results <- outcome{s, e}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("auth did not precede database")
			}
			expected := staff.ErrUnavailable
			if canceled {
				cancel()
				expected = context.Canceled
			}
			o := join(t, results)
			rejected(t, o.state, o.err, expected)
		})
	}
}

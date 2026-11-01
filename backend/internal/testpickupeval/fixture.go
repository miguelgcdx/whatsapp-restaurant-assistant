package testpickupeval

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const (
	restaurantID                         = "a0000000-0000-4000-8000-000000000001"
	menuID                               = "10000000-0000-4000-8000-000000000001"
	staffSubject cart.PickupStaffSubject = "pickup-eval-operator"
)

type fixture struct {
	ctx            context.Context
	t              testing.TB
	store          *cart.Store
	pickup         *cart.PickupStore
	jobs           *postgres.Store
	local          cart.LocalContext
	snapshot       cart.Snapshot
	controlVersion cart.Version
	gate           *menugate.Gate
	reply          string
	// Fixture-only auth, never part of Scenario/Observation or model wire.
	control *conversation.Store
	token   string
}

func (f *fixture) check(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("pickup evaluation fixture: %v", err)
	}
}

func newOperation(t testing.TB) cart.OperationID {
	t.Helper()
	id, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newFixture(ctx context.Context, t testing.TB) *fixture {
	t.Helper()
	f := &fixture{ctx: ctx, t: t}
	dsn := testpg.Start(t)
	f.check(postgres.Migrate(ctx, dsn))
	catalogStore, err := catalog.OpenStore(ctx, dsn, restaurantID)
	f.check(err)
	t.Cleanup(catalogStore.Close)
	f.check(catalogStore.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true},
		[]catalog.Item{{ID: menuID, Name: "Fictional sandwich", PriceMinor: 650, Currency: "USD", Available: true}}))
	f.store, err = cart.OpenStore(ctx, dsn, restaurantID)
	f.check(err)
	t.Cleanup(f.store.Close)
	f.local, err = f.store.ProvisionLocal(ctx, "fictional-business", "fictional-sender")
	f.check(err)
	f.jobs, err = postgres.Open(ctx, dsn)
	f.check(err)
	t.Cleanup(f.jobs.Close)

	created, err := f.store.Create(ctx, f.local, newOperation(t))
	f.check(err)
	menu, err := cart.ParseMenuID(menuID)
	f.check(err)
	add, err := cart.Add(menu, 2)
	f.check(err)
	added, err := f.store.Apply(ctx, f.local, newOperation(t), created.Cart.ID, created.Cart.Version, add)
	f.check(err)
	note, err := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, "  no salt 🥪\n")
	f.check(err)
	noted, err := f.store.Apply(ctx, f.local, newOperation(t), added.Cart.ID, added.Cart.Version, note)
	f.check(err)

	// Fulfillment uses the established legacy LOCAL execution path. The setup
	// job is completed before any menu claim; no manufactured execution handle.
	event := foundation.Event{ID: "eval-select-pickup", Sender: "fictional-sender", Text: "Select pickup"}
	_, err = f.jobs.Accept(ctx, event, time.Now().UTC())
	f.check(err)
	job, ok, err := f.jobs.ClaimJob(ctx, time.Now().UTC(), time.Minute)
	f.check(err)
	if !ok {
		t.Fatal("fixture fulfillment job was not claimed")
	}
	call, err := cart.SetFulfillmentCall(noted.Cart.ID, noted.Cart.Version, cart.PickupFulfillment())
	f.check(err)
	execution, err := f.store.RegisterExecution(ctx, f.local, event.ID, 1, call)
	f.check(err)
	selected, err := f.store.ExecuteDraft(ctx, f.local, execution, job)
	f.check(err)
	f.snapshot = selected.Cart
	f.check(f.jobs.CompleteJob(ctx, job, "Local setup completed (not presented)", time.Now().UTC()))
	f.controlVersion = f.initializeControl(dsn)
	f.pickup, err = cart.NewPickupStore(f.store, restaurantID, cart.PickupCompletionAssessmentVersion)
	f.check(err)

	// The existing menu gate, not SQL, persists each restaurant reply. Only this
	// auxiliary reply provider uses an in-memory transport; the completion
	// assessor separately uses real loopback HTTP and actual wire capture.
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-eval-reply", Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		text, err := json.Marshal(f.reply)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` + string(text) + `}}]}`))}, nil
	})})
	f.check(err)
	f.gate, err = menugate.Open(ctx, dsn, restaurantID, client, 100)
	f.check(err)
	t.Cleanup(f.gate.Close)
	return f
}

func (f *fixture) finish(x cart.PickupExecution, reply string) menugate.Receipt {
	f.t.Helper()
	f.reply = reply
	receipt, err := f.gate.RunAndQueue(f.ctx, x.Source, x.Claim)
	f.check(err)
	return receipt
}

func (f *fixture) initializeControl(dsn string) cart.Version {
	f.t.Helper()
	members, err := staff.OpenStore(f.ctx, dsn, restaurantID)
	f.check(err)
	f.t.Cleanup(members.Close)
	_, err = members.EnsureMember(f.ctx, string(staffSubject))
	f.check(err)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	f.check(err)
	jwks := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "kid": "eval", "x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	boundServer(jwks)
	jwks.Start()
	f.t.Cleanup(jwks.Close)
	verifier, err := staff.NewVerifier(jwks.URL, jwks.URL+"/api/auth/jwks")
	f.check(err)
	control, err := conversation.OpenStore(f.ctx, dsn, restaurantID, verifier)
	f.check(err)
	f.t.Cleanup(control.Close)
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"sub": string(staffSubject), "iss": jwks.URL, "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	token.Header["kid"] = "eval"
	raw, err := token.SignedString(private)
	f.check(err)
	f.control, f.token = control, raw
	state, err := control.Initialize(f.ctx, raw, f.local.ConversationID(), conversation.ModeAI, "eval-initialize")
	f.check(err)
	version, err := cart.ParseVersion(state.Version)
	f.check(err)
	return version
}

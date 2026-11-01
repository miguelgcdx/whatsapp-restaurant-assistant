package main

import (
	"bytes"
	"crypto/ed25519"
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
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffavailability"
	"restaurant-assistant/backend/internal/staffcatalog"
	"restaurant-assistant/backend/internal/stafforders"
	"restaurant-assistant/backend/internal/testpg"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

const connectedRestaurant = "a0000000-0000-4000-8000-000000000001"
const connectedMenu = "10000000-0000-4000-8000-000000000001"

func connectedCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type connectedTransport func(*http.Request) (*http.Response, error)

func (f connectedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type connectedFixture struct {
	store   *cart.Store
	pickup  *cart.PickupStore
	local   cart.LocalContext
	members *staff.Store
	handler http.Handler
	token   string
	order   cart.PickupOrder
	draft   cart.Snapshot
}

// This is a self-connected fixture, not independent-review evidence. Only the
// model response and LOCAL receiver are synthetic; authority and persistence use
// production public operations against testpg's newly created cluster.
func connectedSetup(t *testing.T) *connectedFixture {
	t.Helper()
	dsn := testpg.Start(t)
	connectedCheck(t, postgres.Migrate(t.Context(), dsn))
	cat, err := catalog.OpenStore(t.Context(), dsn, connectedRestaurant)
	connectedCheck(t, err)
	t.Cleanup(cat.Close)
	connectedCheck(t, cat.Initialize(t.Context(), catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: true}, []catalog.Item{{ID: connectedMenu, Name: "Fictional sandwich", PriceMinor: 650, Currency: "USD", Available: true}}))
	s, err := cart.OpenStore(t.Context(), dsn, connectedRestaurant)
	connectedCheck(t, err)
	t.Cleanup(s.Close)
	local, err := s.ProvisionLocal(t.Context(), "connected-business", "connected-sender")
	connectedCheck(t, err)
	members, err := staff.OpenStore(t.Context(), dsn, connectedRestaurant)
	connectedCheck(t, err)
	t.Cleanup(members.Close)
	for _, subject := range []string{"connected-operator", "connected-observer"} {
		created, err := members.EnsureMember(t.Context(), subject)
		connectedCheck(t, err)
		if !created {
			t.Fatal("new cluster already had fixture member")
		}
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	connectedCheck(t, err)
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"connected","x":%q}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(keys.Close)
	verifier, err := staff.NewVerifier(keys.URL, keys.URL+"/api/auth/jwks")
	connectedCheck(t, err)
	now := time.Now()
	claims := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": "connected-operator", "iss": keys.URL, "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(59 * time.Second).Unix()})
	claims.Header["kid"] = "connected"
	token, err := claims.SignedString(private)
	connectedCheck(t, err)
	control, err := conversation.OpenStore(t.Context(), dsn, connectedRestaurant, verifier)
	connectedCheck(t, err)
	t.Cleanup(control.Close)

	// Minimal existing draft/fulfillment recipe; no direct SQL or preview route.
	op, err := cart.NewOperationID()
	connectedCheck(t, err)
	created, err := s.Create(t.Context(), local, op)
	connectedCheck(t, err)
	menu, err := cart.ParseMenuID(connectedMenu)
	connectedCheck(t, err)
	add, err := cart.Add(menu, 2)
	connectedCheck(t, err)
	op, err = cart.NewOperationID()
	connectedCheck(t, err)
	added, err := s.Apply(t.Context(), local, op, created.Cart.ID, created.Cart.Version, add)
	connectedCheck(t, err)
	note, err := cart.SetNote([]cart.UnitID{added.Cart.Units[0].ID}, "  no salt 🥪\n")
	connectedCheck(t, err)
	op, err = cart.NewOperationID()
	connectedCheck(t, err)
	noted, err := s.Apply(t.Context(), local, op, added.Cart.ID, added.Cart.Version, note)
	connectedCheck(t, err)
	jobs, err := postgres.Open(t.Context(), dsn)
	connectedCheck(t, err)
	t.Cleanup(jobs.Close)
	event := foundation.Event{ID: "connected-fulfillment", Sender: "connected-sender", Text: "select pickup"}
	_, err = jobs.Accept(t.Context(), event, now)
	connectedCheck(t, err)
	job, ok, err := jobs.ClaimJob(t.Context(), now, time.Minute)
	connectedCheck(t, err)
	if !ok || job.Event.ID != event.ID {
		t.Fatal("missing owned fulfillment claim")
	}
	call, err := cart.SetFulfillmentCall(noted.Cart.ID, noted.Cart.Version, cart.PickupFulfillment())
	connectedCheck(t, err)
	plan, err := s.RegisterExecution(t.Context(), local, event.ID, 1, call)
	connectedCheck(t, err)
	selected, err := s.ExecuteDraft(t.Context(), local, plan, job)
	connectedCheck(t, err)
	connectedCheck(t, jobs.CompleteJob(t.Context(), job, "fixture fulfillment selected", time.Now()))
	_, err = control.Initialize(t.Context(), token, local.ConversationID(), conversation.ModeAI, "connected-init")
	connectedCheck(t, err)
	pickup, err := cart.NewPickupStore(s, connectedRestaurant, cart.PickupCompletionAssessmentVersion)
	connectedCheck(t, err)

	// The actual commandcode HTTP client serves both the gate and assessor.
	var assessments atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var wire struct {
			Tools []struct {
				Function commandcode.ToolDefinition `json:"function"`
			} `json:"tools"`
		}
		if r.Method != "POST" || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&wire) != nil {
			t.Error("invalid fixture model request")
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(wire.Tools) == 1 && wire.Tools[0].Function.Name == "get_menu" {
			io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Local placeholder, not presented"},"finish_reason":"stop"}]}`)
			return
		}
		if len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "assess_pickup_completion" {
			t.Error("unexpected model tool")
			http.Error(w, "unexpected tool", 400)
			return
		}
		assessments.Add(1)
		io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"connected-assessment","type":"function","function":{"name":"assess_pickup_completion","arguments":"{\"decision\":\"complete\"}"}}]}}]}`)
	}))
	t.Cleanup(model.Close)
	destination, err := url.Parse(model.URL)
	connectedCheck(t, err)
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-connected-key", Transport: connectedTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != commandcode.Endpoint {
			return nil, fmt.Errorf("unexpected model endpoint")
		}
		localRequest := r.Clone(r.Context())
		localRequest.URL = destination
		localRequest.Host = destination.Host
		return model.Client().Transport.RoundTrip(localRequest)
	})})
	connectedCheck(t, err)
	gate, err := menugate.Open(t.Context(), dsn, connectedRestaurant, client, 100)
	connectedCheck(t, err)
	t.Cleanup(gate.Close)
	inbound := func(id, text string) cart.PickupExecution {
		t.Helper()
		_, fresh, err := s.AcceptMenu(t.Context(), local, foundation.Event{ID: id, Sender: event.Sender, Text: text}, time.Now())
		connectedCheck(t, err)
		if !fresh {
			t.Fatal("expected fresh menu event")
		}
		claim, ok, err := s.ClaimMenu(t.Context(), local, time.Minute)
		connectedCheck(t, err)
		if !ok {
			t.Fatal("missing owned menu claim")
		}
		zero, err := cart.ParseVersion(0)
		connectedCheck(t, err)
		return cart.PickupExecution{Local: local, Source: claim.Source(), Claim: claim.Job(), ControlVersion: zero}
	}
	x := inbound("connected-summary", "Show the final pickup summary")
	call, err = cart.PreparePickupCall(selected.Cart.ID, selected.Cart.Version)
	connectedCheck(t, err)
	x.Execution, err = s.RegisterExecution(t.Context(), local, x.Source.EventProviderID(), 1, call)
	connectedCheck(t, err)
	summary, err := pickup.Prepare(t.Context(), x, selected.Cart.ID, selected.Cart.Version)
	connectedCheck(t, err)
	queued, err := pickup.ReadLocalIntents(t.Context(), local, cart.PickupIntentID{}, 100)
	connectedCheck(t, err)
	if len(queued) != 1 || queued[0].Kind != "summary" || queued[0].ID != summary.Intent.ID || queued[0].CandidateID != summary.CandidateID || queued[0].Text != summary.Intent.Text {
		t.Fatal("missing authoritative queued summary", queued)
	}
	receiver := testpickupreceiver.New(t)
	body, err := json.Marshal(testpickupreceiver.Message{CorrelationID: queued[0].ID.String(), Text: queued[0].Text})
	connectedCheck(t, err)
	req, err := http.NewRequestWithContext(t.Context(), "POST", receiver.URL(), bytes.NewReader(body))
	connectedCheck(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := receiver.Client().Do(req)
	connectedCheck(t, err)
	var ack testpickupreceiver.Acknowledgement
	decodeErr := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&ack)
	res.Body.Close()
	connectedCheck(t, decodeErr)
	digest, err := hex.DecodeString(ack.ContentSHA256)
	connectedCheck(t, err)
	if res.StatusCode != 200 || ack.CorrelationID != queued[0].ID.String() || ack.ReceiverReference == "" || len(digest) != sha256.Size || !bytes.Equal(digest, summary.Intent.TextDigest[:]) {
		t.Fatal("invalid actual HTTP ACK", ack)
	}
	var receivedDigest [32]byte
	copy(receivedDigest[:], digest)
	_, err = pickup.RecordLocalPresentation(t.Context(), local, cart.PickupLocalPresentation{IntentID: queued[0].ID, CandidateID: queued[0].CandidateID, ReceiverReference: ack.ReceiverReference, ReceivedTextDigest: receivedDigest})
	connectedCheck(t, err)
	_, err = gate.RunAndQueue(t.Context(), x.Source, x.Claim)
	connectedCheck(t, err)
	x = inbound("connected-confirmation", "No, that's all. Place the order.")
	assessor, err := pickupintent.New(pickupintent.Config{Client: client, MaxOutputTokens: 80})
	connectedCheck(t, err)
	submitted, err := pickup.AssessAndSubmit(t.Context(), x, 1, assessor)
	connectedCheck(t, err)
	if assessments.Load() != 1 || submitted.Order.Status != cart.PickupSubmitted || submitted.Order.Version.Int64() != 0 || submitted.Order.Confirmed.ID != summary.CandidateID || submitted.Order.Confirmed.UnitCount != 2 || submitted.Order.Total.AmountMinor != 1300 || submitted.Order.Payment.State != cart.PickupUnpaid {
		t.Fatal("actual assessor did not submit expected pickup", submitted)
	}
	access, err := staff.NewHandler(members, connectedRestaurant, verifier)
	connectedCheck(t, err)
	read, err := staffcatalog.NewHandler(cat, members, connectedRestaurant, verifier, strings.Repeat("a", 64))
	connectedCheck(t, err)
	availability, err := staffavailability.NewHandler(cat, members, verifier)
	connectedCheck(t, err)
	orders, err := stafforders.NewHandler(pickup, verifier)
	connectedCheck(t, err)
	return &connectedFixture{s, pickup, local, members, dispatch(access, read, availability, orders), token, submitted.Order, selected.Cart}
}

// Compare DATA separately from per-attempt request IDs, including on replay.
func connectedRequest(t *testing.T, f *connectedFixture, method, path, token, key, body string, status int) (json.RawMessage, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(t.Context())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
	}
	res := httptest.NewRecorder()
	f.handler.ServeHTTP(res, req)
	if res.Code != status || res.Header().Get("Content-Type") != "application/json" || res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Location") != "" || res.Header().Get("Allow") != "" {
		t.Fatalf("%s %s: status=%d headers=%v body=%s", method, path, res.Code, res.Header(), res.Body.String())
	}
	var envelope map[string]json.RawMessage
	connectedCheck(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	var id string
	connectedCheck(t, json.Unmarshal(envelope["request_id"], &id))
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != id || len(envelope) != 2 {
		t.Fatal("invalid response envelope/request ID", res.Body.String())
	}
	field := "data"
	if status != 200 {
		field = "error"
	}
	if len(envelope[field]) == 0 {
		t.Fatal("missing envelope field", field)
	}
	return envelope[field], id
}

type connectedState struct {
	Order   cart.PickupOrder
	Units   cart.PickupOrderUnitPage
	Intents []cart.PickupLocalIntent
	Draft   cart.Snapshot
	Orders  cart.PickupOrderPage
}

// A separate real member remains authorized after the action actor is revoked.
// Public persisted reads, not SQL counters or a fake port, prove no second effect.
func connectedRead(t *testing.T, f *connectedFixture) connectedState {
	t.Helper()
	var state connectedState
	var err error
	state.Order, err = f.pickup.ReadOrder(t.Context(), "connected-observer", f.order.ID)
	connectedCheck(t, err)
	state.Units, err = f.pickup.ReadUnits(t.Context(), "connected-observer", f.order.ID, f.order.Confirmed.ID, cart.PickupPageRequest{})
	connectedCheck(t, err)
	state.Intents, err = f.pickup.ReadLocalIntents(t.Context(), f.local, cart.PickupIntentID{}, 100)
	connectedCheck(t, err)
	state.Draft, err = f.store.Read(t.Context(), f.local, f.draft.ID)
	connectedCheck(t, err)
	state.Orders, err = f.pickup.ListOrders(t.Context(), "connected-observer", cart.PickupPageRequest{})
	connectedCheck(t, err)
	return state
}

func connectedObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var data map[string]any
	connectedCheck(t, json.Unmarshal(raw, &data))
	return data
}

func connectedCapabilities(t *testing.T, data map[string]any, order cart.PickupOrder) {
	t.Helper()
	actions, ok := data["permitted_actions"].([]any)
	if !ok || len(actions) != len(order.PermittedActions) {
		t.Fatal("missing server capabilities", data)
	}
	seen := map[string]bool{}
	for i, action := range actions {
		name, ok := action.(string)
		if !ok || seen[name] || name != string(order.PermittedActions[i]) {
			t.Fatal("incorrect/duplicate capabilities", actions)
		}
		seen[name] = true
	}
}

func TestOrdersConnectedPersistenceReplayAndCurrentAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL binaries")
	}
	f := connectedSetup(t)
	path := "/v1/staff/orders/" + f.order.ID.String()
	unitsPath := path + "/summaries/" + f.order.Confirmed.ID.String() + "/units"
	before := connectedRead(t, f)
	if len(before.Units.Items) != 2 || len(before.Orders.Items) != 1 || len(before.Intents) != 2 || before.Intents[1].Kind != "receipt" || !reflect.DeepEqual(before.Order, f.order) || !reflect.DeepEqual(before.Draft, f.draft) {
		t.Fatal("invalid persisted submission fixture", before)
	}
	for _, tc := range []struct{ name, token string }{{"missing-JWT", ""}, {"malformed-JWT", "not.a.jwt"}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range []struct{ method, path, body string }{{"GET", "/v1/staff/orders", ""}, {"GET", path, ""}, {"GET", unitsPath, ""}, {"POST", path + "/actions", `{"action":"accept","expected_version":"0"}`}} {
				raw, _ := connectedRequest(t, f, route.method, route.path, tc.token, "unauthenticated", route.body, 401)
				if string(raw) != `{"code":"unauthenticated","message":"Authentication is required."}` {
					t.Fatal("unsafe authentication error", string(raw))
				}
				if !reflect.DeepEqual(connectedRead(t, f), before) {
					t.Fatal("authentication rejection changed persisted effects")
				}
			}
		})
	}
	listRaw, _ := connectedRequest(t, f, "GET", "/v1/staff/orders", f.token, "", "", 200)
	list := connectedObject(t, listRaw)
	items, ok := list["items"].([]any)
	if !ok || len(items) != 1 || len(list) != 2 || list["next_cursor"] != nil {
		t.Fatal("list page", list)
	}
	money := map[string]any{"amount_minor": "1300", "currency": "USD", "exponent": float64(2)}
	wantSummary := map[string]any{"id": f.order.ID.String(), "version": "0", "created_at": f.order.CreatedAt.UTC().Format(time.RFC3339Nano), "fulfillment_status": "submitted", "payment_status": "unpaid", "fulfillment_kind": "pickup", "total": money}
	if !reflect.DeepEqual(items[0], wantSummary) {
		t.Fatal("minimal list fields (no detail/capabilities)", list)
	}
	detailRaw, _ := connectedRequest(t, f, "GET", path, f.token, "", "", 200)
	detail := connectedObject(t, detailRaw)
	connectedCapabilities(t, detail, before.Order)
	confirmed, ok := detail["confirmed"].(map[string]any)
	if !ok || confirmed["id"] != f.order.Confirmed.ID.String() || confirmed["unit_count"] != float64(2) || confirmed["units_href"] != strings.Replace(unitsPath, "/v1/", "/api/", 1) || !reflect.DeepEqual(confirmed["total"], money) {
		t.Fatal("confirmed snapshot/link", detail)
	}
	// Follow the frontend link's equivalent mounted API path and both pages.
	firstRaw, _ := connectedRequest(t, f, "GET", unitsPath+"?limit=1", f.token, "", "", 200)
	first := connectedObject(t, firstRaw)
	cursor, ok := first["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("missing units cursor", first)
	}
	secondPath := unitsPath + "?limit=1&cursor=" + url.QueryEscape(cursor)
	secondRaw, _ := connectedRequest(t, f, "GET", secondPath, f.token, "", "", 200)
	second := connectedObject(t, secondRaw)
	for i, page := range []map[string]any{first, second} {
		units, ok := page["items"].([]any)
		if !ok || len(units) != 1 || len(page) != 4 || page["summary_id"] != f.order.Confirmed.ID.String() || page["unit_count"] != float64(2) {
			t.Fatal("units page linkage", page)
		}
		u := before.Units.Items[i]
		want := map[string]any{"id": u.ID.String(), "menu_item_id": connectedMenu, "name": "Fictional sandwich", "note": []string{"  no salt 🥪\n", ""}[i], "unit_price": map[string]any{"amount_minor": "650", "currency": "USD", "exponent": float64(2)}}
		if !reflect.DeepEqual(units[0], want) {
			t.Fatal("confirmed unit content", units[0], want)
		}
	}
	if second["next_cursor"] != nil || before.Units.Items[0].ID == before.Units.Items[1].ID {
		t.Fatal("units termination/identity")
	}
	acceptBody := `{"action":"accept","expected_version":"0"}`
	acceptedRaw, acceptedID := connectedRequest(t, f, "POST", path+"/actions", f.token, "connected-key-A", acceptBody, 200)
	accepted := connectedRead(t, f)
	if accepted.Order.Status != cart.PickupAccepted || accepted.Order.Version.Int64() != 1 {
		t.Fatal("accept not persisted", accepted.Order)
	}
	acceptedData := connectedObject(t, acceptedRaw)
	connectedCapabilities(t, acceptedData, accepted.Order)
	if acceptedData["id"] != f.order.ID.String() || acceptedData["version"] != "1" || acceptedData["fulfillment_status"] != "accepted" || !reflect.DeepEqual(acceptedData["confirmed"], confirmed) {
		t.Fatal("incorrect committed accept DATA", acceptedData)
	}
	preparingBody := fmt.Sprintf(`{"action":"start_preparing","expected_version":"%d"}`, accepted.Order.Version.Int64())
	preparingRaw, _ := connectedRequest(t, f, "POST", path+"/actions", f.token, "connected-key-B", preparingBody, 200)
	advanced := connectedRead(t, f)
	if advanced.Order.Status != cart.PickupPreparing || advanced.Order.Version.Int64() != 2 || advanced.Order.Payment.State != cart.PickupUnpaid || advanced.Order.Payment.CollectedAt != nil || advanced.Order.PreparationStartedAt == nil || advanced.Order.Confirmed != before.Order.Confirmed || !reflect.DeepEqual(advanced.Units, before.Units) || !reflect.DeepEqual(advanced.Draft, before.Draft) {
		t.Fatal("unpaid preparation/immutable confirmed terms", advanced)
	}
	preparingData := connectedObject(t, preparingRaw)
	connectedCapabilities(t, preparingData, advanced.Order)
	if preparingData["id"] != f.order.ID.String() || preparingData["version"] != "2" || preparingData["fulfillment_status"] != "preparing" || preparingData["payment_status"] != "unpaid" || !reflect.DeepEqual(preparingData["confirmed"], confirmed) {
		t.Fatal("incorrect committed preparation DATA", preparingData)
	}
	for i, state := range []connectedState{accepted, advanced} {
		if len(state.Intents) != len(before.Intents)+i+1 || !reflect.DeepEqual(state.Intents[:2], before.Intents) {
			t.Fatal("incorrect durable notification count/prefix", state.Intents)
		}
		intent := state.Intents[len(state.Intents)-1]
		want := "Pickup order " + f.order.ID.String() + ": " + string(state.Order.Status) + "; payment: unpaid."
		if intent.Kind != "status" || intent.OrderID == nil || *intent.OrderID != f.order.ID || intent.CandidateID != f.order.Confirmed.ID || intent.Text != want {
			t.Fatal("wrong notification intent", intent)
		}
	}
	if !reflect.DeepEqual(advanced.Intents[:len(accepted.Intents)], accepted.Intents) || advanced.Intents[2].ID == advanced.Intents[3].ID {
		t.Fatal("notification identity/preservation")
	}
	replayRaw, replayID := connectedRequest(t, f, "POST", path+"/actions", f.token, "connected-key-A", acceptBody, 200)
	if !bytes.Equal(replayRaw, acceptedRaw) || replayID == acceptedID {
		t.Fatal("replay did not return original DATA with fresh request ID")
	}
	liveRaw, _ := connectedRequest(t, f, "GET", path, f.token, "", "", 200)
	if !bytes.Equal(liveRaw, preparingRaw) || !reflect.DeepEqual(connectedRead(t, f), advanced) {
		t.Fatal("replay changed live state/effects")
	}
	for _, page := range []struct {
		path string
		raw  json.RawMessage
	}{{unitsPath + "?limit=1", firstRaw}, {secondPath, secondRaw}} {
		raw, _ := connectedRequest(t, f, "GET", page.path, f.token, "", "", 200)
		if !bytes.Equal(raw, page.raw) {
			t.Fatal("action changed immutable HTTP unit page")
		}
	}
	conflict, _ := connectedRequest(t, f, "POST", path+"/actions", f.token, "connected-key-A", `{"action":"reject","expected_version":"0"}`, 409)
	if string(conflict) != `{"code":"idempotency_conflict","message":"Idempotency key was already used for a different request."}` || !reflect.DeepEqual(connectedRead(t, f), advanced) {
		t.Fatal("changed intent must conflict without effects", string(conflict))
	}
	connectedCheck(t, f.members.RevokeMember(t.Context(), "connected-operator"))
	forbidden, _ := connectedRequest(t, f, "POST", path+"/actions", f.token, "connected-key-A", acceptBody, 403)
	if string(forbidden) != `{"code":"forbidden","message":"Staff access is not available."}` || !reflect.DeepEqual(connectedRead(t, f), advanced) {
		t.Fatal("current membership must precede replay without effects", string(forbidden))
	}
}

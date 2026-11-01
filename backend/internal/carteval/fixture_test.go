package carteval_test

import (
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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "12345678-1234-4234-8234-123456789abc"
const burger = "10000000-0000-4000-8000-000000000001"
const water = "10000000-0000-4000-8000-000000000002"
const soup = "10000000-0000-4000-8000-000000000003"
const addTwo = `{"action":"add","expected_version":"0","menu_id":"10000000-0000-4000-8000-000000000001","count":2}`

// Counts are invocations at this test's public boundary, NOT durable writes or
// hidden AddCaller invocations. Setup is excluded and explicitly reported below.
type counts struct {
	Transport     int `json:"model_transport_attempts"`
	Caller        int `json:"AdmitAdd_invocations"`
	Native        int `json:"direct_AdmitNativeEdit_invocations"`
	Decode        int `json:"direct_Decode_invocations"`
	Read          int `json:"direct_cart_Read_invocations"`
	Browse        int `json:"direct_catalog_Snapshot_invocations"`
	Discover      int `json:"ClaimNextNativeEdit_invocations"`
	Recover       int `json:"RecoverNativeEdit_invocations"`
	Execute       int `json:"ExecuteNativeEdit_invocations"`
	Inspect       int `json:"direct_job_Inspect_invocations"`
	ControlRead   int `json:"direct_control_Read_invocations"`
	AdmissionRead int `json:"direct_ReadAdmission_invocations"`
}

type delta struct {
	Version  int64 `json:"version"`
	Quantity int   `json:"quantity"`
	Subtotal int64 `json:"subtotal_minor"`
}

// These are returned boundary outcomes, not durable mutation counters.
type outcomes struct {
	PendingReferences        int `json:"pending_references"`
	Clarifications           int `json:"clarifications"`
	NativeRejections         int `json:"native_rejections"`
	DecodeRejections         int `json:"decode_rejections"`
	DecodeAcceptedCompanions int `json:"decode_accepted_companions_not_admitted"`
	AcquiredWork             int `json:"acquired_work"`
	EmptyDiscoveries         int `json:"empty_discoveries"`
	ExecutedResults          int `json:"executed_results"`
}

type record struct {
	SchemaVersion     int                   `json:"schema_version"`
	Candidate         string                `json:"candidate"`
	Case              string                `json:"case"`
	Variant           string                `json:"variant"`
	Level             string                `json:"level"`
	Script            string                `json:"actual_script_not_original_NL"`
	Expected          string                `json:"expected"`
	Actual            string                `json:"actual"`
	Error             string                `json:"error_identity"`
	ZeroCall          *bool                 `json:"zero_call,omitempty"`
	Component         string                `json:"component_outcome"`
	Outcome           string                `json:"outcome"`
	Gates             []string              `json:"gates"`
	Counts            counts                `json:"invocations_excluding_setup"`
	Returns           outcomes              `json:"returned_outcomes_not_mutation_counts"`
	Unavailable       map[string]string     `json:"unavailable_observations"`
	Scope             map[string]string     `json:"trusted_setup_scope,omitempty"`
	Before            *cart.Snapshot        `json:"before,omitempty"`
	Pending           *cart.Snapshot        `json:"pending_or_rejected,omitempty"`
	After             *cart.Snapshot        `json:"after,omitempty"`
	PendingDelta      *delta                `json:"pending_delta,omitempty"`
	FinalDelta        *delta                `json:"final_delta,omitempty"`
	PendingOperation  string                `json:"pending_operation_id,omitempty"`
	PendingState      string                `json:"pending_reference_state,omitempty"`
	ProposalArguments string                `json:"scripted_proposal_arguments,omitempty"`
	ExecuteResult     *cart.Result          `json:"execute_result,omitempty"`
	Menu              *catalog.MenuSnapshot `json:"public_browse,omitempty"`
	JobBefore         *foundation.Snapshot  `json:"job_before,omitempty"`
	JobAfter          *foundation.Snapshot  `json:"job_after,omitempty"`
}

func begin(t *testing.T, id, variant, level, script, expected string) *record {
	t.Helper()
	r := &record{SchemaVersion: 1, Candidate: "carteval-fixture-v1", Case: id, Variant: variant, Level: level,
		Script: script, Expected: expected, Component: "NOT_RUN", Outcome: "NOT_RUN",
		Gates:       []string{"L: original utterance interpretation not authorized or measured"},
		Unavailable: map[string]string{"durable_mutation_counters": "UNAVAILABLE: no public durable cart-mutation counter; invocation counts are not mutations", "AddCaller_ReadMenu_count": "UNAVAILABLE: private binding; not instrumented", "AddCaller_internal_admission_and_reads": "UNAVAILABLE: only outer invocation and returned outcome observed"}}
	t.Cleanup(func() {
		if t.Failed() {
			r.Component = "FAIL"
			r.Outcome = "FAIL"
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Errorf("encode synthetic record: %v", err)
			return
		}
		t.Logf("CART_EVAL_RECORD %s", b)
	})
	return r
}

func observed(r *record, actual string) {
	r.Actual = actual
	r.Component = "PASS"
	// PASS is ONLY this named component; hidden counts are accepted limitations,
	// never invented observations or a full original-case/language disposition.
	r.Outcome = "PASS"
}

func require(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Fatal(message)
	}
}

func difference(before, after cart.Snapshot) *delta {
	return &delta{Version: after.Version.Int64() - before.Version.Int64(), Quantity: len(after.Units) - len(before.Units), Subtotal: after.Subtotal - before.Subtotal}
}

type fixture struct {
	dsn      string
	cart     *cart.Store
	catalog  *catalog.Store
	controls *conversation.Store
	jobs     *postgres.Store
	key      ed25519.PrivateKey
	issuer   string
}

// One owned TCP cluster; each component gets a NEW public local conversation and
// sole draft. No private SQL observations or direct control-row insertion.
func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	f := &fixture{dsn: testpg.Start(t)}
	if err := postgres.Migrate(ctx, f.dsn); err != nil {
		t.Fatal("isolated public migration failed (driver details withheld)")
	}
	var err error
	f.catalog, err = catalog.OpenStore(ctx, f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.catalog.Close)
	err = f.catalog.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, []catalog.Item{
		{ID: burger, Name: "Burger", PriceMinor: 700, Currency: "USD", Available: true},
		{ID: water, Name: "Water", PriceMinor: 200, Currency: "USD", Available: true},
		{ID: soup, Name: "Soup", PriceMinor: 500, Currency: "USD", Available: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.key = key
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/auth/jwks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"owned","x":"%s"}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(server.Close)
	f.issuer = server.URL
	verifier, err := staff.NewVerifier(server.URL, server.URL+"/api/auth/jwks")
	if err != nil {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(ctx, f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(members.Close)
	created, err := members.EnsureMember(ctx, "synthetic-evaluator")
	require(t, err == nil && created, "fabricated staff membership initialization failed")
	f.controls, err = conversation.OpenStore(ctx, f.dsn, restaurant, verifier)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.controls.Close)
	f.cart, err = cart.OpenStore(ctx, f.dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.cart.Close)
	f.jobs, err = postgres.Open(ctx, f.dsn)
	if err != nil {
		t.Fatal("isolated public job store open failed (driver details withheld)")
	}
	t.Cleanup(f.jobs.Close)
	return f
}

type scope struct {
	record    *record
	local     cart.LocalContext
	claim     cart.MenuClaim
	initial   cart.Snapshot
	event     foundation.Event
	admission cart.Admission
	control   conversation.State
	token     string
}

func (f *fixture) fresh(t *testing.T, r *record, text string) scope {
	t.Helper()
	ctx := t.Context()
	sender := "synthetic-" + r.Case + "-" + r.Variant
	local, err := f.cart.ProvisionLocal(ctx, "synthetic-business", sender)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": "synthetic-evaluator", "iss": f.issuer, "aud": "restaurant-assistant-api", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()})
	token.Header["kid"] = "owned"
	raw, err := token.SignedString(f.key)
	if err != nil {
		t.Fatal("synthetic token signing failed")
	}
	control, err := f.controls.Initialize(ctx, raw, local.ConversationID(), conversation.ModeAI, "initialize-"+sender)
	require(t, err == nil && control.Mode == conversation.ModeAI && control.Version == 0, "public AI control initialization failed")
	op, err := cart.NewOperationID()
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.cart.Create(ctx, local, op)
	if err != nil {
		t.Fatal(err)
	}
	require(t, created.OperationID == op && created.Cart.ConversationID == local.ConversationID(), "create identity mismatch")
	require(t, created.Cart.Version.Int64() == 0 && len(created.Cart.Units) == 0 && created.Cart.Subtotal == 0 && created.Cart.Currency == "USD" && created.Cart.Exponent == 2, "independent draft not empty USD exponent 2")
	event := foundation.Event{ID: sender, Sender: sender, Text: text}
	admission, isNew, err := f.cart.AcceptMenu(ctx, local, event, time.Now().UTC())
	require(t, err == nil && isNew && admission.Mode == "ai" && admission.Version == 0, "public event admission failed")
	claim, ok, err := f.cart.ClaimMenu(ctx, local, 5*time.Minute)
	require(t, err == nil && ok && claim.Source() == admission.Source && claim.Job().Event == event, "public menu claim mismatch")
	r.Scope = map[string]string{"restaurant_id": restaurant, "conversation_id": local.ConversationID().String(), "sender": sender, "event_id": event.ID, "event_uuid": claim.Source().EventUUID(), "menu_job_id": claim.Job().ID, "cart_id": created.Cart.ID.String(), "create_operation_id": op.String(), "staff_subject": "synthetic-evaluator", "setup": "Migrate/Initialize/EnsureMember/ProvisionLocal/conversation.Initialize/Create/AcceptMenu/ClaimMenu excluded from invocation counters"}
	return scope{record: r, local: local, claim: claim, initial: created.Cart, event: event, admission: admission, control: control, token: raw}
}

func (f *fixture) read(t *testing.T, r *record, s scope) cart.Snapshot {
	t.Helper()
	r.Counts.Read++
	got, err := f.cart.Read(t.Context(), s.local, s.initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *fixture) job(t *testing.T, s scope) foundation.Snapshot {
	t.Helper()
	s.record.Counts.Inspect++
	got, err := f.jobs.Inspect(t.Context(), s.event.ID)
	if err != nil {
		t.Fatal("public job inspection failed (driver details withheld)")
	}
	return got
}

func (f *fixture) unchangedAuthority(t *testing.T, s scope) {
	t.Helper()
	s.record.Counts.ControlRead++
	got, err := f.controls.Read(t.Context(), s.token, s.local.ConversationID())
	require(t, err == nil && got == s.control, "public control changed")
	s.record.Counts.AdmissionRead++
	admission, err := f.cart.ReadAdmission(t.Context(), s.local, s.event.ID)
	// ClaimMenu changes queued to leased; compare current immutable provenance.
	require(t, err == nil && admission.Source == s.admission.Source && admission.JobID == s.admission.JobID && admission.Mode == s.admission.Mode && admission.Version == s.admission.Version && admission.State == "leased" && admission.OutboxID == "", "public admission provenance or reply changed")
}

type transport func(*http.Request) (*http.Response, error)

func (fn transport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func (f *fixture) caller(t *testing.T, r *record) *menugate.AddCaller {
	t.Helper()
	client, err := cc.New(cc.Config{APIKey: "fabricated-evaluation-key", Transport: transport(func(req *http.Request) (*http.Response, error) {
		r.Counts.Transport++
		r.ProposalArguments = addTwo
		var wire struct {
			Messages []cc.Message `json:"messages"`
			Tools    []struct {
				Function cc.ToolDefinition `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
			t.Error("synthetic request decode failed")
		}
		if len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "propose_add" || len(wire.Messages) != 2 || wire.Messages[1].Content == nil || *wire.Messages[1].Content != r.Script {
			t.Error("unexpected actual Add script/tool boundary")
		}
		body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{{ID: "synthetic-add", Type: "function", Function: cc.FunctionCall{Name: "propose_add", Arguments: addTwo}}}}}}})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := menugate.OpenAddCaller(t.Context(), f.dsn, restaurant, client, 321)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(caller.Close)
	return caller
}

func assertBurgers(t *testing.T, s cart.Snapshot) {
	t.Helper()
	require(t, s.Version.Int64() == 1 && len(s.Units) == 2 && s.Subtotal == 1400 && s.Currency == "USD" && s.Exponent == 2, "executed quantity/version/subtotal/currency mismatch")
	seen := map[cart.UnitID]bool{}
	for _, u := range s.Units {
		require(t, u.ID != (cart.UnitID{}) && !seen[u.ID] && u.MenuID.String() == burger && u.PriceMinor == 700 && u.Currency == "USD" && u.Exponent == 2 && u.Note == "", "executed actual unit identity/menu/price/note mismatch")
		seen[u.ID] = true
	}
}

// Deliberately compare semantic unit contents, not generated cart/conversation or
// unit IDs. Both sides are validated independently by assertBurgers first.
func sameContents(a, b cart.Snapshot) bool {
	if a.Currency != b.Currency || a.Exponent != b.Exponent || a.Subtotal != b.Subtotal || len(a.Units) != len(b.Units) {
		return false
	}
	remaining := append([]cart.Unit(nil), b.Units...)
	for _, u := range a.Units {
		found := -1
		for i, v := range remaining {
			u.ID = cart.UnitID{}
			v.ID = cart.UnitID{}
			if u == v {
				found = i
				break
			}
		}
		if found < 0 {
			return false
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	return len(remaining) == 0
}

func TestCartEvaluationFixture(t *testing.T) {
	f := setup(t)
	var baseline cart.Snapshot
	for _, id := range []string{"C01", "C02", "C03"} {
		t.Run(id, func(t *testing.T) {
			r := begin(t, id, "scripted-add", "I/component", "add two burgers", "fresh empty draft; optional public catalog browse read-only; PENDING unchanged; existing native consumer adds two unnoted B at 700, subtotal 1400, version +1")
			s := f.fresh(t, r, r.Script)
			before := f.read(t, r, s)
			r.Before = &before
			if id == "C01" {
				r.Counts.Browse++
				menu, err := f.catalog.Snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				r.Menu = &menu
				want := catalog.MenuSnapshot{Settings: catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: true}, Items: []catalog.Item{{ID: burger, Name: "Burger", PriceMinor: 700, Currency: "USD", Available: true}, {ID: water, Name: "Water", PriceMinor: 200, Currency: "USD", Available: true}, {ID: soup, Name: "Soup", PriceMinor: 500, Currency: "USD", Available: false}}}
				require(t, reflect.DeepEqual(menu, want), "public browse differs from stored synthetic fixture")
				require(t, reflect.DeepEqual(f.read(t, r, s), before), "browse changed draft")
			}
			jobBefore := f.job(t, s)
			r.JobBefore = &jobBefore
			caller := f.caller(t, r)
			r.Counts.Caller++
			ref, err := caller.AdmitAdd(t.Context(), s.claim)
			require(t, err == nil && ref != (cart.NativeActionRef{}) && ref.OperationID() != (cart.OperationID{}), "Add did not return populated PENDING reference")
			r.Returns.PendingReferences++
			r.PendingOperation = ref.OperationID().String()
			r.PendingState = "PENDING, not execution"
			pending := f.read(t, r, s)
			r.Pending = &pending
			r.PendingDelta = difference(before, pending)
			require(t, reflect.DeepEqual(pending, before), "PENDING changed cart/version")
			require(t, r.Counts.Transport == 1, "expected exactly one scripted model transport attempt")
			f.unchangedAuthority(t, s)
			require(t, f.job(t, s) == jobBefore, "pending admission changed public job/effect counters")
			var work cart.NativeActionClaim
			var ok bool
			if id == "C02" {
				r.Counts.Discover++
				work, ok, err = f.cart.ClaimNextNativeEdit(t.Context(), time.Minute)
			} else {
				r.Counts.Recover++
				work, ok, err = f.cart.RecoverNativeEdit(t.Context(), ref, time.Minute)
			}
			require(t, err == nil && ok && work.Ref() == ref, "existing consumer lease lost pending identity")
			r.Returns.AcquiredWork++
			r.Counts.Execute++
			result, err := f.cart.ExecuteNativeEdit(t.Context(), work)
			require(t, err == nil && result.OperationID == ref.OperationID(), "execution result identity mismatch")
			r.Returns.ExecutedResults++
			r.ExecuteResult = &result
			after := f.read(t, r, s)
			r.After = &after
			r.FinalDelta = difference(before, after)
			require(t, reflect.DeepEqual(after, result.Cart) && after.ID == before.ID && after.ConversationID == before.ConversationID, "public Read and ExecuteResult differ")
			assertBurgers(t, after)
			if id == "C01" {
				baseline = after
			} else {
				require(t, baseline.ID != after.ID && baseline.ConversationID != after.ConversationID && sameContents(baseline, after), "fresh/reset semantic equality failed")
			}
			r.Counts.Discover++
			empty, ok, err := f.cart.ClaimNextNativeEdit(t.Context(), time.Minute)
			require(t, err == nil && !ok && empty == (cart.NativeActionClaim{}), "completed work remained discoverable")
			r.Returns.EmptyDiscoveries++
			jobAfter := f.job(t, s)
			r.JobAfter = &jobAfter
			require(t, jobAfter == jobBefore, "native edit unexpectedly changed public reply job/effect counters")
			f.unchangedAuthority(t, s)
			observed(r, "PENDING unchanged; separate actual ExecuteResult matches public Read; independent reset and semantic contents checked")
		})
	}
	t.Run("C09", func(t *testing.T) {
		r := begin(t, "C09", "unknown-name", "I/component", "add one pizza", "supported grammar with unknown menu NAME requires clarification before model I/O; no native work and unchanged public draft")
		s := f.fresh(t, r, r.Script)
		before := f.read(t, r, s)
		r.Before = &before
		jobBefore := f.job(t, s)
		r.JobBefore = &jobBefore
		r.Counts.Caller++
		ref, err := f.caller(t, r).AdmitAdd(t.Context(), s.claim)
		require(t, err == menugate.ErrClarificationRequired && ref == (cart.NativeActionRef{}) && r.Counts.Transport == 0, "unknown NAME did not clarify with zero reference/transport")
		r.Error = "menugate.ErrClarificationRequired"
		r.Returns.Clarifications++
		after := f.read(t, r, s)
		r.Pending = &after
		r.After = &after
		r.PendingDelta = difference(before, after)
		r.FinalDelta = difference(before, after)
		require(t, reflect.DeepEqual(before, after), "unknown NAME changed draft")
		r.Counts.Discover++
		work, ok, err := f.cart.ClaimNextNativeEdit(t.Context(), time.Minute)
		require(t, err == nil && !ok && work == (cart.NativeActionClaim{}), "unknown NAME admitted native work")
		r.Returns.EmptyDiscoveries++
		jobAfter := f.job(t, s)
		r.JobAfter = &jobAfter
		require(t, jobAfter == jobBefore, "unknown NAME changed public job/effect counters")
		f.unchangedAuthority(t, s)
		r.Gates = append(r.Gates, "unknown ID and original 'Add a pizza.' interpretation not established")
		observed(r, "known unknown-name branch only; no admission or execution")
	})
	t.Run("C10", func(t *testing.T) {
		r := begin(t, "C10", "trusted-native-unavailable", "I/lower-level-component", "trusted native Add(S,1)", "exact cart.ErrUnavailableItem; zero native reference; unchanged empty draft and no discoverable work")
		s := f.fresh(t, r, "synthetic trusted unavailable-item admission")
		before := f.read(t, r, s)
		r.Before = &before
		jobBefore := f.job(t, s)
		r.JobBefore = &jobBefore
		menu, err := cart.ParseMenuID(soup)
		if err != nil {
			t.Fatal(err)
		}
		action, err := cart.Add(menu, 1)
		if err != nil {
			t.Fatal(err)
		}
		call, err := cart.EditCall(before.ID, before.Version, action)
		if err != nil {
			t.Fatal(err)
		}
		r.Counts.Native++
		ref, err := f.cart.AdmitNativeEdit(t.Context(), s.local, s.claim, 0, call)
		require(t, err == cart.ErrUnavailableItem && ref == (cart.NativeActionRef{}), "unavailable native Add did not reject exactly")
		r.Error = "cart.ErrUnavailableItem"
		r.Returns.NativeRejections++
		after := f.read(t, r, s)
		r.Pending = &after
		r.After = &after
		r.PendingDelta = difference(before, after)
		r.FinalDelta = difference(before, after)
		require(t, reflect.DeepEqual(before, after), "unavailable Add changed draft")
		r.Counts.Discover++
		work, ok, err := f.cart.ClaimNextNativeEdit(t.Context(), time.Minute)
		require(t, err == nil && !ok && work == (cart.NativeActionClaim{}), "unavailable Add admitted work")
		r.Returns.EmptyDiscoveries++
		jobAfter := f.job(t, s)
		r.JobAfter = &jobAfter
		require(t, jobAfter == jobBefore, "unavailable Add changed public job/effect counters")
		f.unchangedAuthority(t, s)
		r.Gates = append(r.Gates, "AddCaller/original NL and separate existing-order preservation not evaluated")
		observed(r, "lower-level trusted native admission rejected; no caller, model or execution invocation")
	})
	for _, variant := range []struct{ name, field string }{{"price", `"price_minor":1`}, {"authority", `"authority":"staff"`}, {"cart", `"cart_id":"30000000-0000-4000-8000-000000000001"`}} {
		t.Run("C11/"+variant.name, func(t *testing.T) {
			script := strings.TrimSuffix(addTwo, "}") + "," + variant.field + "}"
			r := begin(t, "C11", variant.name, "D/component", script, "extra field rejects exact cartintent.ErrInput and zero cart.Call; trusted cart bound outside JSON; no admission/execution")
			s := f.fresh(t, r, "synthetic decoder companion only")
			before := f.read(t, r, s)
			r.Before = &before
			jobBefore := f.job(t, s)
			r.JobBefore = &jobBefore
			r.Counts.Decode++
			call, err := cartintent.Decode(script, before.ID)
			zero := reflect.DeepEqual(call, cart.Call{})
			r.ZeroCall = &zero
			require(t, err == cartintent.ErrInput && zero, "decoder rejection must be exact ErrInput and zero Call")
			r.Error = "cartintent.ErrInput"
			r.Returns.DecodeRejections++
			// Preserve the accepted shape and external binding through the SAME public
			// decoder, without admitting or executing the valid companion proposal.
			r.Counts.Decode++
			valid, err := cartintent.Decode(addTwo, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			menu, err := cart.ParseMenuID(burger)
			if err != nil {
				t.Fatal(err)
			}
			action, err := cart.Add(menu, 2)
			if err != nil {
				t.Fatal(err)
			}
			want, err := cart.EditCall(before.ID, before.Version, action)
			if err != nil {
				t.Fatal(err)
			}
			require(t, reflect.DeepEqual(valid, want), "valid companion did not preserve external trusted cart binding")
			r.Returns.DecodeAcceptedCompanions++
			after := f.read(t, r, s)
			r.Pending = &after
			r.After = &after
			r.PendingDelta = difference(before, after)
			r.FinalDelta = difference(before, after)
			require(t, reflect.DeepEqual(before, after), "decoder companion changed cart")
			r.Counts.Discover++
			work, ok, err := f.cart.ClaimNextNativeEdit(t.Context(), time.Minute)
			require(t, err == nil && !ok && work == (cart.NativeActionClaim{}), "decoder companion admitted native work")
			r.Returns.EmptyDiscoveries++
			jobAfter := f.job(t, s)
			r.JobAfter = &jobAfter
			require(t, jobAfter == jobBefore, "decoder companion changed public job/effect counters")
			f.unchangedAuthority(t, s)
			observed(r, "exact ErrInput and zero Call; accepted companion equals constructor-built trusted-bound Call; neither proposal admitted or executed")
		})
	}
	for _, id := range []string{"C04", "C05", "C06", "C07", "C08", "C12"} {
		t.Run(id, func(t *testing.T) {
			r := begin(t, id, "outside-first-cut", "deferred", "NOT_RUN", "outside firstcut10778")
			r.Gates = append(r.Gates, "not selected: no Remove/SetNote/context/order/summary/dashboard evaluation")
			r.Actual = "NOT_RUN; no fixture allocated or operation invoked for this case"
		})
	}
}

func TestSemanticContentsComparison(t *testing.T) {
	menu, err := cart.ParseMenuID(burger)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cart.ParseUnitID("20000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cart.ParseUnitID("20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	a := cart.Snapshot{Currency: "USD", Exponent: 2, Subtotal: 1400, Units: []cart.Unit{{ID: first, MenuID: menu, PriceMinor: 700, Currency: "USD", Exponent: 2}, {ID: second, MenuID: menu, PriceMinor: 700, Currency: "USD", Exponent: 2, Note: "synthetic note"}}}
	b := a
	b.Units = []cart.Unit{a.Units[1], a.Units[0]}
	b.Units[0].ID = first
	b.Units[1].ID = second
	require(t, sameContents(a, b), "semantic equality must ignore unit IDs and order")
	for _, field := range []string{"note", "price", "menu", "currency", "exponent", "quantity", "subtotal"} {
		t.Run(field, func(t *testing.T) {
			changed := b
			changed.Units = append([]cart.Unit(nil), b.Units...)
			switch field {
			case "note":
				changed.Units[0].Note = "different"
			case "price":
				changed.Units[0].PriceMinor++
			case "menu":
				changed.Units[0].MenuID = cart.MenuID{}
			case "currency":
				changed.Currency = "EUR"
			case "exponent":
				changed.Exponent = 3
			case "quantity":
				changed.Units = changed.Units[:1]
			case "subtotal":
				changed.Subtotal++
			}
			require(t, !sameContents(a, changed), "semantic comparison missed changed "+field)
		})
	}
}

func TestSnapshotDelta(t *testing.T) {
	before := cart.Snapshot{Subtotal: 700, Units: make([]cart.Unit, 1)}
	version, err := cart.ParseVersion(1)
	if err != nil {
		t.Fatal(err)
	}
	after := cart.Snapshot{Version: version, Subtotal: 1400, Units: make([]cart.Unit, 2)}
	require(t, *difference(before, after) == (delta{Version: 1, Quantity: 1, Subtotal: 700}), "snapshot delta mismatch")
	require(t, *difference(before, before) == (delta{}), "unchanged delta mismatch")
}

package menugate_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/conversation"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/menuengine"
	"restaurant-assistant/backend/internal/menugate"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "12345678-1234-4234-8234-123456789abc"
const foreignRestaurant = "22345678-1234-4234-8234-123456789abc"
const item = "10000000-0000-4000-8000-000000000001"
const key = "fabricated-gate-key"
const issuer = "http://127.0.0.1:3000"

type fixture struct {
	dsn, token string
	cart       *cart.Store
	controls   *conversation.Store
	jobs       *postgres.Store
	conn       *pgx.Conn
	next       int
}
type turn struct {
	local  cart.LocalContext
	source cart.Source
	job    foundation.Job
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	f := &fixture{dsn: testpg.Start(t)}
	if e := postgres.Migrate(ctx, f.dsn); e != nil {
		t.Fatal(e)
	}
	cat, e := catalog.OpenStore(ctx, f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer cat.Close()
	if e = cat.Initialize(ctx, catalog.Settings{Currency: "USD", CurrencyExponent: 2, Open: false}, []catalog.Item{{ID: item, Name: "Stored closed menu", PriceMinor: 650, Currency: "USD", Available: false}}); e != nil {
		t.Fatal(e)
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"owned","x":"%s"}]}`, base64.RawURLEncoding.EncodeToString(pub))
	}))
	t.Cleanup(server.Close)
	verifier, e := staff.NewVerifier(issuer, server.URL+"/api/auth/jwks")
	if e != nil {
		t.Fatal(e)
	}
	members, e := staff.OpenStore(ctx, f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	defer members.Close()
	if ok, e := members.EnsureMember(ctx, "fixture-staff"); e != nil || !ok {
		t.Fatal(ok, e)
	}
	issued := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": "fixture-staff", "iss": issuer, "aud": "restaurant-assistant-api", "iat": issued.Unix(), "exp": issued.Add(time.Minute).Unix()})
	tok.Header["kid"] = "owned"
	f.token, e = tok.SignedString(priv)
	if e != nil {
		t.Fatal(e)
	}
	f.controls, e = conversation.OpenStore(ctx, f.dsn, restaurant, verifier)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(f.controls.Close)
	f.cart, e = cart.OpenStore(ctx, f.dsn, restaurant)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(f.cart.Close)
	f.jobs, e = postgres.Open(ctx, f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(f.jobs.Close)
	f.conn = connect(t, f.dsn)
	return f
}
func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, e := pgx.Connect(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}
func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, e := f.conn.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) fresh(t *testing.T, mode conversation.Mode) turn {
	t.Helper()
	ctx := t.Context()
	f.next++
	name := fmt.Sprintf("turn-%d", f.next)
	// Fixture preparation keeps previously expired, distinct-sender test jobs out
	// of the global reclaim queue. Every new turn is still publicly Accept/Claim.
	f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '1 hour' WHERE state='leased'`)
	local, e := f.cart.ProvisionLocal(ctx, "fixture-business", name)
	if e != nil {
		t.Fatal(e)
	}
	event := foundation.Event{ID: name, Sender: name, Text: "persisted question " + name}
	now := time.Now()
	if _, e = f.jobs.Accept(ctx, event, now); e != nil {
		t.Fatal(e)
	}
	job, ok, e := f.jobs.ClaimJob(ctx, now, time.Minute)
	if e != nil || !ok || job.Event.ID != event.ID {
		t.Fatalf("current claim %+v %v %v", job, ok, e)
	}
	source, e := f.cart.BindSource(ctx, local, event.ID)
	if e != nil {
		t.Fatal(e)
	}
	if mode != "" {
		state, e := f.controls.Initialize(ctx, f.token, local.ConversationID(), mode, "init-"+name)
		if e != nil || state.Version != 0 {
			t.Fatal(state, e)
		}
	}
	return turn{local, source, job}
}
func (f *fixture) mode(t *testing.T, v turn, mode conversation.Mode) {
	t.Helper()
	ctx := t.Context()
	s, e := f.controls.Read(ctx, f.token, v.local.ConversationID())
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.controls.SetMode(ctx, f.token, v.local.ConversationID(), mode, s.Version, fmt.Sprintf("mode-%d-%d", f.next, s.Version))
	if e != nil {
		t.Fatal(s, e)
	}
}
func (f *fixture) hashes(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, table := range []string{"restaurants", "restaurant_settings", "menu_items", "cart_conversations", "cart_execution_sources", "cart_execution_calls", "cart_operations", "carts", "cart_units", "conversation_controls", "conversation_control_operations", "inbound_events", "jobs", "outbox", "send_attempts"} {
		var h string
		if e := f.conn.QueryRow(t.Context(), `SELECT count(*)::text||':'||md5(COALESCE(string_agg(row_to_json(r)::text,',' ORDER BY row_to_json(r)::text),'')) FROM app.`+table+` r`).Scan(&h); e != nil {
			t.Fatal(e)
		}
		m[table] = h
	}
	return m
}
func (f *fixture) unchanged(t *testing.T, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, f.hashes(t)) {
		t.Fatal("gate changed persisted authority, domain state, calls, or effects")
	}
}
func (f *fixture) inspect(t *testing.T, v turn) foundation.Snapshot {
	t.Helper()
	s, e := f.jobs.Inspect(t.Context(), v.job.Event.ID)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func client(t *testing.T, fn transport) *cc.Client {
	t.Helper()
	c, e := cc.New(cc.Config{APIKey: key, Transport: fn})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func response(message cc.Message, finish string) *http.Response {
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
func answer() *http.Response {
	s := "ephemeral proposal"
	return response(cc.Message{Role: cc.RoleAssistant, Content: &s}, "stop")
}
func menuCall() *http.Response {
	return response(cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{{ID: "owned-menu-call", Type: "function", Function: cc.FunctionCall{Name: "get_menu", Arguments: "{}"}}}}, "tool_calls")
}
func openGate(t *testing.T, f *fixture, c *cc.Client) *menugate.Gate {
	t.Helper()
	g, e := menugate.Open(t.Context(), f.dsn, restaurant, c, 321)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(g.Close)
	return g
}
func rejected(t *testing.T, result menuengine.Result, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(result, menuengine.Result{}) {
		t.Fatalf("result=%+v error=%v want zero/%v", result, err, want)
	}
}
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-time.After(3 * time.Second):
		t.Fatal("bounded join did not complete")
	}
	var zero T
	return zero
}
func waitLock(t *testing.T, c *pgx.Conn, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		var waiting bool
		if e := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1)`, prefix+"%").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
	}
}
func expired(t *testing.T, f *fixture, v turn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		var yes bool
		if e := f.conn.QueryRow(ctx, `SELECT clock_timestamp()>=lease_until FROM app.jobs WHERE id=$1`, v.job.ID).Scan(&yes); e != nil {
			t.Fatal(e)
		}
		if yes {
			return
		}
	}
}

type outcome struct {
	r menuengine.Result
	e error
}

// One owned cluster serves independent turn namespaces. No vendor HTTP transport
// or environment-provided credentials are used anywhere in these public tests.
func TestGateAuthority(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	t.Run("closed complete menu and direct proposal", func(t *testing.T) {
		v := f.fresh(t, conversation.ModeAI)
		var calls int
		var first []cc.Message
		c := client(t, func(r *http.Request) (*http.Response, error) {
			calls++
			var wire struct {
				Model    string       `json:"model"`
				Messages []cc.Message `json:"messages"`
				Tools    []struct {
					Type     string            `json:"type"`
					Function cc.ToolDefinition `json:"function"`
				} `json:"tools"`
				Max int `json:"max_tokens"`
			}
			if e := json.NewDecoder(r.Body).Decode(&wire); e != nil {
				t.Error(e)
			}
			if r.Method != "POST" || r.URL.String() != cc.Endpoint || r.Header.Get("Authorization") != "Bearer "+key || wire.Model != cc.Model || wire.Max != 321 || len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "get_menu" {
				t.Error("unexpected concrete wire boundary")
			}
			if calls == 1 {
				if len(wire.Messages) != 2 || wire.Messages[1].Content == nil || *wire.Messages[1].Content != v.job.Event.Text {
					t.Error("unvalidated text/history")
				}
				first = wire.Messages
				return menuCall(), nil
			}
			if calls != 2 || len(wire.Messages) != 4 || !reflect.DeepEqual(wire.Messages[:2], first) || wire.Messages[3].Role != cc.RoleTool || wire.Messages[3].ToolCallID != "owned-menu-call" {
				t.Error("unexpected tool/history count")
			}
			var payload any
			if wire.Messages[3].Content == nil {
				t.Error("missing complete menu")
			} else if e := json.Unmarshal([]byte(*wire.Messages[3].Content), &payload); e != nil {
				t.Error(e)
			}
			var want any
			_ = json.Unmarshal([]byte(`{"open":false,"items":[{"id":"10000000-0000-4000-8000-000000000001","name":"Stored closed menu","available":false,"price":{"amount_minor":"650","currency":"USD","exponent":2}}]}`), &want)
			if !reflect.DeepEqual(payload, want) {
				t.Errorf("complete stored closed/unavailable menu: %+v", payload)
			}
			return answer(), nil
		})
		g := openGate(t, f, c)
		// Existing public cart state/replay is independent of the menu proposal.
		op, e := cart.NewOperationID()
		if e != nil {
			t.Fatal(e)
		}
		base, e := f.cart.Create(ctx, v.local, op)
		if e != nil {
			t.Fatal(e)
		}
		before := f.hashes(t)
		inspect := f.inspect(t, v)
		source := v.source
		got, e := g.Run(ctx, v.source, v.job)
		if e != nil || got.Reply != "ephemeral proposal" || got.Menu == nil || got.Menu.Open || calls != 2 {
			t.Fatalf("closed %+v %v calls=%d", got, e, calls)
		}
		f.unchanged(t, before)
		read, e := f.cart.Read(ctx, v.local, base.Cart.ID)
		if e != nil || !reflect.DeepEqual(read, base.Cart) {
			t.Fatal(read, e)
		}
		replay, e := f.cart.Create(ctx, v.local, op)
		if e != nil || !reflect.DeepEqual(replay, base) {
			t.Fatal(replay, e)
		}
		retry, e := f.cart.BindSource(ctx, v.local, v.job.Event.ID)
		if e != nil || retry != source || f.inspect(t, v) != inspect {
			t.Fatal("binding/claim changed", e)
		}
		directCalls := 0
		direct := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) { directCalls++; return answer(), nil }))
		got, e = direct.Run(ctx, v.source, v.job)
		if e != nil || got.Reply != "ephemeral proposal" || got.Menu != nil || directCalls != 1 {
			t.Fatalf("direct %+v %v", got, e)
		}
		f.unchanged(t, before)
	})
	t.Run("constructor and input boundaries", func(t *testing.T) {
		c := client(t, func(*http.Request) (*http.Response, error) { t.Error("unexpected HTTP"); return answer(), nil })
		for _, tc := range []struct {
			ctx     context.Context
			dsn, id string
			c       *cc.Client
			n       int
		}{{nil, f.dsn, restaurant, c, 1}, {ctx, f.dsn, strings.ToUpper(restaurant), c, 1}, {ctx, f.dsn, restaurant, nil, 1}, {ctx, f.dsn, restaurant, c, 0}, {ctx, f.dsn, restaurant, c, 8193}, {ctx, "postgres://fixture@remote.invalid/db", restaurant, c, 1}} {
			g, e := menugate.Open(tc.ctx, tc.dsn, tc.id, tc.c, tc.n)
			if g != nil || !errors.Is(e, menugate.ErrInput) {
				t.Fatal(g, e)
			}
		}
		v := f.fresh(t, conversation.ModeAI)
		g := openGate(t, f, c)
		before := f.hashes(t)
		got, e := g.Run(nil, v.source, v.job)
		rejected(t, got, e, menugate.ErrInput)
		got, e = g.Run(ctx, cart.Source{}, v.job)
		rejected(t, got, e, menugate.ErrInput)
		var nilGate *menugate.Gate
		got, e = nilGate.Run(ctx, v.source, v.job)
		rejected(t, got, e, menugate.ErrInput)
		got, e = (&menugate.Gate{}).Run(ctx, v.source, v.job)
		rejected(t, got, e, menugate.ErrInput)
		foreign, e := menugate.Open(ctx, f.dsn, foreignRestaurant, c, 1)
		if e != nil {
			t.Fatal(e)
		}
		defer foreign.Close()
		got, e = foreign.Run(ctx, v.source, v.job)
		rejected(t, got, e, menugate.ErrNotFound)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		got, e = g.Run(cancelled, v.source, v.job)
		rejected(t, got, e, context.Canceled)
		late := lateDeadline{Context: ctx, end: time.Now().Add(-time.Second)}
		got, e = g.Run(late, v.source, v.job)
		rejected(t, got, e, context.DeadlineExceeded)
		g.Close()
		got, e = g.Run(ctx, v.source, v.job)
		rejected(t, got, e, menugate.ErrUnavailable)
		f.unchanged(t, before)
	})
	t.Run("precheck never calls model", func(t *testing.T) {
		for _, name := range []string{"missing-control", "staff", "id", "uppercase", "unhyphenated", "generation", "attempt", "event", "sender", "text", "source-provider", "infinity", "expired", "reclaimed", "completed", "source-missing", "source-owner", "conversation-owner"} {
			t.Run(name, func(t *testing.T) {
				mode := conversation.ModeAI
				if name == "missing-control" {
					mode = ""
				}
				if name == "staff" {
					mode = conversation.ModeStaff
				}
				v := f.fresh(t, mode)
				job := v.job
				source := v.source
				want := foundation.ErrStaleLease
				switch name {
				case "missing-control":
					want = menugate.ErrNotFound
				case "staff":
					want = menugate.ErrControlChanged
				case "id":
					job.ID = "00000000-0000-0000-0000-000000000000"
				case "uppercase":
					job.ID = strings.ToUpper(job.ID)
				case "unhyphenated":
					job.ID = strings.ReplaceAll(job.ID, "-", "")
				case "generation":
					job.Generation++
				case "attempt":
					job.Attempt++
				case "event":
					job.Event.ID = "other"
				case "sender":
					job.Event.Sender = "other"
				case "text":
					job.Event.Text = "other"
				case "source-provider":
					other := f.fresh(t, conversation.ModeAI)
					source = other.source
				case "infinity":
					f.exec(t, `UPDATE app.jobs SET lease_until='infinity' WHERE id=$1`, job.ID)
				case "expired":
					f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID)
				case "reclaimed":
					retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
					if e != nil || !ok || retry.ID != job.ID || retry.Generation == job.Generation {
						t.Fatal(retry, ok, e)
					}
				case "completed":
					if e := f.jobs.CompleteJob(ctx, job, "fixture completion", time.Now()); e != nil {
						t.Fatal(e)
					}
				case "source-missing":
					unbound := foundation.Event{ID: job.Event.ID + "-unbound", Sender: job.Event.Sender + "-unbound", Text: "explicit unbound fixture"}
					if _, e := f.jobs.Accept(ctx, unbound, time.Now().Add(24*time.Hour)); e != nil {
						t.Fatal(e)
					}
					var eid string
					if e := f.conn.QueryRow(ctx, `SELECT id::text FROM app.inbound_events WHERE provider_event_id=$1`, unbound.ID).Scan(&eid); e != nil {
						t.Fatal(e)
					}
					f.exec(t, `UPDATE app.cart_execution_sources SET event_id=$1 WHERE event_id=$2`, eid, source.EventUUID())
					want = menugate.ErrNotFound
				case "source-owner":
					other := f.fresh(t, conversation.ModeAI)
					f.exec(t, `UPDATE app.cart_execution_sources SET conversation_id=$1 WHERE event_id=$2`, other.local.ConversationID().String(), source.EventUUID())
					want = menugate.ErrNotFound
				case "conversation-owner":
					f.exec(t, `UPDATE app.cart_conversations SET business='changed' WHERE id=$1`, v.local.ConversationID().String())
					want = menugate.ErrNotFound
				}
				var calls atomic.Int32
				g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return answer(), nil }))
				before := f.hashes(t)
				inspect := f.inspect(t, v)
				got, e := g.Run(ctx, source, job)
				rejected(t, got, e, want)
				if calls.Load() != 0 {
					t.Fatal("model before authority")
				}
				f.unchanged(t, before)
				if f.inspect(t, v) != inspect {
					t.Fatal("claim/effect changed")
				}
			})
		}
	})
	t.Run("postcheck rejects drift and staff completes across model barrier", func(t *testing.T) {
		for _, name := range []string{"takeover", "takeover-after-menu", "takeover-return", "same-mode", "expiry", "reclaim", "completed", "content", "conversation", "source"} {
			t.Run(name, func(t *testing.T) {
				v := f.fresh(t, conversation.ModeAI)
				entered := make(chan struct{}, 1)
				release := make(chan struct{})
				var calls atomic.Int32
				g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
					n := calls.Add(1)
					if name == "takeover-after-menu" && n == 1 {
						return menuCall(), nil
					}
					entered <- struct{}{}
					select {
					case <-release:
						return answer(), nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}))
				live, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				done := make(chan outcome, 1)
				go func() { r, e := g.Run(live, v.source, v.job); done <- outcome{r, e} }()
				await(t, entered)
				want := menugate.ErrControlChanged
				switch name {
				case "takeover", "takeover-after-menu":
					f.mode(t, v, conversation.ModeStaff)
				case "takeover-return":
					f.mode(t, v, conversation.ModeStaff)
					f.mode(t, v, conversation.ModeAI)
				case "same-mode":
					f.mode(t, v, conversation.ModeAI)
				case "expiry":
					f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, v.job.ID)
					want = foundation.ErrStaleLease
				case "reclaim":
					retry, ok, e := f.jobs.ClaimJob(ctx, time.Now().Add(2*time.Minute), time.Minute)
					if e != nil || !ok || retry.ID != v.job.ID {
						t.Fatal(retry, ok, e)
					}
					want = foundation.ErrStaleLease
				case "completed":
					if e := f.jobs.CompleteJob(ctx, v.job, "fixture completion", time.Now()); e != nil {
						t.Fatal(e)
					}
					want = foundation.ErrStaleLease
				case "content":
					f.exec(t, `UPDATE app.inbound_events SET content='changed' WHERE id=$1`, v.source.EventUUID())
					want = foundation.ErrStaleLease
				case "conversation":
					f.exec(t, `UPDATE app.cart_conversations SET sender='changed' WHERE id=$1`, v.local.ConversationID().String())
					want = menugate.ErrNotFound
				case "source":
					other := f.fresh(t, conversation.ModeAI)
					f.exec(t, `UPDATE app.cart_execution_sources SET conversation_id=$1 WHERE event_id=$2`, other.local.ConversationID().String(), v.source.EventUUID())
					want = menugate.ErrNotFound
				}
				before := f.hashes(t)
				inspect := f.inspect(t, v)
				close(release)
				got := await(t, done)
				rejected(t, got.r, got.e, want)
				wantCalls := int32(1)
				if name == "takeover-after-menu" {
					wantCalls = 2
				}
				if calls.Load() != wantCalls {
					t.Fatal(calls.Load())
				}
				f.unchanged(t, before)
				if f.inspect(t, v) != inspect {
					t.Fatal("postcheck changed job/effects")
				}
			})
		}
	})
	t.Run("fresh database clock after pre and post blocking", func(t *testing.T) {
		for _, phase := range []string{"pre", "post"} {
			for _, resource := range []string{"restaurant", "conversation", "source", "control", "job"} {
				t.Run(phase+"-"+resource, func(t *testing.T) {
					v := f.fresh(t, conversation.ModeAI)
					gateConn := connect(t, f.dsn)
					tx, e := gateConn.Begin(ctx)
					if e != nil {
						t.Fatal(e)
					}
					defer tx.Rollback(ctx)
					var calls atomic.Int32
					entered := make(chan struct{}, 1)
					release := make(chan struct{})
					g := openGate(t, f, client(t, func(r *http.Request) (*http.Response, error) {
						calls.Add(1)
						if phase == "post" {
							entered <- struct{}{}
							select {
							case <-release:
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
						}
						return answer(), nil
					}))
					live, cancel := context.WithTimeout(ctx, 3*time.Second)
					defer cancel()
					done := make(chan outcome, 1)
					start := func() { go func() { r, e := g.Run(live, v.source, v.job); done <- outcome{r, e} }() }
					if phase == "post" {
						start()
						await(t, entered)
					}
					f.exec(t, `UPDATE app.jobs SET lease_until=clock_timestamp()+interval '250 milliseconds' WHERE id=$1`, v.job.ID)
					var sql, prefix string
					var arg any
					switch resource {
					case "restaurant":
						sql = `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`
						arg = restaurant
						prefix = "SELECT id::text FROM app.restaurants"
					case "conversation":
						sql = `SELECT id FROM app.cart_conversations WHERE id=$1 FOR UPDATE`
						arg = v.local.ConversationID().String()
						prefix = "SELECT id::text FROM app.cart_conversations"
					case "source":
						sql = `SELECT event_id FROM app.cart_execution_sources WHERE event_id=$1 FOR UPDATE`
						arg = v.source.EventUUID()
						prefix = "SELECT event_id::text FROM app.cart_execution_sources"
					case "control":
						sql = `SELECT conversation_id FROM app.conversation_controls WHERE conversation_id=$1 FOR UPDATE`
						arg = v.local.ConversationID().String()
						prefix = "SELECT mode,version FROM app.conversation_controls"
					case "job":
						sql = `SELECT id FROM app.jobs WHERE id=$1 FOR UPDATE`
						arg = v.job.ID
						prefix = "SELECT j.id"
					}
					if _, e = tx.Exec(ctx, sql, arg); e != nil {
						t.Fatal(e)
					}
					before := f.hashes(t)
					if phase == "pre" {
						start()
					} else {
						close(release)
					}
					waitLock(t, f.conn, prefix)
					expired(t, f, v)
					if e = tx.Rollback(ctx); e != nil {
						t.Fatal(e)
					}
					got := await(t, done)
					rejected(t, got.r, got.e, foundation.ErrStaleLease)
					wantCalls := int32(0)
					if phase == "post" {
						wantCalls = 1
					}
					if calls.Load() != wantCalls {
						t.Fatal("wrong phase HTTP count", calls.Load())
					}
					f.unchanged(t, before)
				})
			}
		}
	})
	t.Run("contexts errors bounds and pool reuse", func(t *testing.T) {
		v := f.fresh(t, conversation.ModeAI)
		var calls atomic.Int32
		g := openGate(t, f, client(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return answer(), nil }))
		gateConn := connect(t, f.dsn)
		tx, e := gateConn.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE`, restaurant); e != nil {
			t.Fatal(e)
		}
		before := f.hashes(t)
		for _, kind := range []string{"cancel", "earlier", "db-five-second-cap"} {
			live, cancel := context.WithCancel(ctx)
			if kind == "earlier" {
				cancel()
				live, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
			}
			done := make(chan outcome, 1)
			go func() { r, e := g.Run(live, v.source, v.job); done <- outcome{r, e} }()
			waitLock(t, f.conn, "SELECT id::text FROM app.restaurants")
			want := context.DeadlineExceeded
			if kind == "cancel" {
				cancel()
				want = context.Canceled
			}
			var got outcome
			if kind == "db-five-second-cap" {
				select {
				case got = <-done:
				case <-time.After(7 * time.Second):
					t.Fatal("five-second phase cap missing")
				}
			} else {
				got = await(t, done)
			}
			cancel()
			rejected(t, got.r, got.e, want)
		}
		if calls.Load() != 0 {
			t.Fatal("blocked precheck invoked model")
		}
		if e = tx.Rollback(ctx); e != nil {
			t.Fatal(e)
		}
		f.unchanged(t, before)
		if got, e := g.Run(ctx, v.source, v.job); e != nil || got.Reply != "ephemeral proposal" {
			t.Fatal("pool not reusable", got, e)
		}
		f.unchanged(t, before)
		for _, name := range []string{"http-safe-error", "client-protocol", "engine-protocol", "elapsed-success", "elapsed-error", "cancel-during-model", "earlier-model-deadline"} {
			t.Run(name, func(t *testing.T) {
				v := f.fresh(t, conversation.ModeAI)
				live, cancel := context.WithCancel(ctx)
				defer cancel()
				want := cc.ErrUnavailable
				if strings.HasPrefix(name, "elapsed-") {
					live = lateDeadline{Context: ctx, end: time.Now().Add(100 * time.Millisecond)}
					want = context.DeadlineExceeded
				}
				if name == "earlier-model-deadline" {
					cancel()
					live, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
					defer cancel()
					want = context.DeadlineExceeded
				}
				c := client(t, func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) > 20*time.Second {
						t.Error("overall cooperative bound not visible")
					}
					switch name {
					case "http-safe-error":
						return nil, errors.New("synthetic private SQL DSN key")
					case "client-protocol":
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("private"))}, nil
					case "engine-protocol":
						return response(cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{{ID: "bad-menu-parameters", Type: "function", Function: cc.FunctionCall{Name: "get_menu", Arguments: `{"unexpected":true}`}}}}, "tool_calls"), nil
					case "elapsed-success", "elapsed-error":
						end, _ := live.Deadline()
						dbctx, stop := context.WithTimeout(ctx, time.Second)
						defer stop()
						for {
							var elapsed bool
							if e := f.conn.QueryRow(dbctx, `SELECT clock_timestamp()>=$1`, end).Scan(&elapsed); e != nil {
								return nil, e
							}
							if elapsed {
								break
							}
						}
						if name == "elapsed-error" {
							return nil, errors.New("private")
						}
						return answer(), nil
					case "cancel-during-model":
						cancel()
						return answer(), nil
					case "earlier-model-deadline":
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return answer(), nil
				})
				if name == "client-protocol" {
					want = cc.ErrProtocol
				}
				if name == "engine-protocol" {
					want = menuengine.ErrProtocol
				}
				if name == "cancel-during-model" {
					want = context.Canceled
				}
				g := openGate(t, f, c)
				before := f.hashes(t)
				got, e := g.Run(live, v.source, v.job)
				rejected(t, got, e, want)
				if strings.Contains(e.Error(), "private") {
					t.Fatal("error leaked dependency details")
				}
				f.unchanged(t, before)
			})
		}
	})
}

type lateDeadline struct {
	context.Context
	end time.Time
}

func (c lateDeadline) Deadline() (time.Time, bool) { return c.end, true }

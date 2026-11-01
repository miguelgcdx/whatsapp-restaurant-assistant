package testpickupeval

// Reuse the actual pickup fixture and public stores. This is not a fake host or
// a production mount. These helpers do not add SQL or alter Scenario/Edit.Pending.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/carteditflow"
	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/editintent"
	"restaurant-assistant/backend/internal/menugate"
)

// answer runs at the existing injected HTTP boundary after the host has supplied
// its per-invocation facts; it can perform a public-store mutation before reply.
func ambiguityModel(t testing.TB, answer func(editintent.Input) string) (editintent.Interpreter, func() []editintent.Input) {
	t.Helper()
	var mu sync.Mutex
	var inputs []editintent.Input
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var wire ModelRequest
		if json.NewDecoder(r.Body).Decode(&wire) != nil || len(wire.Messages) != 2 || wire.Messages[1].Content == nil || len(wire.Tools) != 1 || wire.Tools[0].Function.Name != "interpret_cart_edit" {
			t.Error("invalid edit HTTP request")
			w.WriteHeader(400)
			return
		}
		var input editintent.Input
		if json.Unmarshal([]byte(*wire.Messages[1].Content), &input) != nil {
			t.Error("invalid actual edit facts")
			w.WriteHeader(400)
			return
		}
		args := func() string {
			mu.Lock()
			defer mu.Unlock()
			inputs = append(inputs, input)
			return answer(input)
		}()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "not evidence of application", "tool_calls": []commandcode.ToolCall{{ID: "offline-ambiguity", Type: "function", Function: commandcode.FunctionCall{Name: "interpret_cart_edit", Arguments: args}}}}}}})
	}))
	boundServer(server)
	server.Start()
	t.Cleanup(server.Close)
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-ambiguity", Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != commandcode.Endpoint {
			return nil, errors.New("unexpected endpoint")
		}
		local := r.Clone(r.Context())
		local.URL = destination
		local.Host = destination.Host
		local.Header.Del("Authorization")
		response, err := server.Client().Transport.RoundTrip(local)
		// Explicit synchronization for public-store callback observations, independent
		// of incidental HTTP transport synchronization.
		mu.Lock()
		mu.Unlock()
		return response, err
	})})
	if err != nil {
		t.Fatal(err)
	}
	interpreter, err := editintent.New(editintent.Config{Client: client, MaxOutputTokens: 400})
	if err != nil {
		t.Fatal(err)
	}
	return interpreter, func() []editintent.Input {
		mu.Lock()
		defer mu.Unlock()
		return append([]editintent.Input(nil), inputs...)
	}
}
func ambiguityHost(t testing.TB, f *fixture, i editintent.Interpreter, gate carteditflow.ClarificationGate) *carteditflow.Host {
	t.Helper()
	h, err := carteditflow.New(carteditflow.Config{Store: f.store, Interpreter: i, ReplyGate: gate, NativeLease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func (f *fixture) ambiguityTurn(claim cart.MenuClaim, slot int64) carteditflow.Turn {
	return carteditflow.Turn{Local: f.local, Claim: claim, Cart: f.snapshot.ID, Version: f.snapshot.Version, Slot: slot}
}
func (f *fixture) ambiguityView(claim cart.MenuClaim) []cart.PendingEdit {
	f.t.Helper()
	view, err := f.store.ReadPendingEdits(f.ctx, f.local, claim, f.snapshot.ID, f.snapshot.Version)
	f.check(err)
	return view.Entries()
}
func (f *fixture) ambiguityRun(h *carteditflow.Host, out carteditflow.Outcome) cart.Result {
	f.t.Helper()
	result, err := h.Execute(f.ctx, out.Native())
	f.check(err)
	current, err := f.store.Read(f.ctx, f.local, f.snapshot.ID)
	f.check(err)
	if result.OperationID != out.Native().OperationID() || !reflect.DeepEqual(result.Cart, current) || current.Version.Int64() != f.snapshot.Version.Int64()+1 {
		f.t.Fatal("no actual native application/readback")
	}
	f.snapshot = current
	return result
}
func ambiguityRemove(s cart.Snapshot) string {
	return fmt.Sprintf(`{"action":"remove","expected_version":"%d","unit_ids":["%s"]}`, s.Version.Int64(), s.Units[0].ID.String())
}
func ambiguityAdd(s cart.Snapshot) string {
	return fmt.Sprintf(`{"action":"add","expected_version":"%d","menu_id":"%s","count":1}`, s.Version.Int64(), menuID)
}

// The failure wrapper delegates to the actual queue with a canceled context:
// known mark success followed by an actual public queue rejection, no fake SQL.
type canceledClarificationGate struct{ carteditflow.ClarificationGate }

func (g canceledClarificationGate) QueueClarification(ctx context.Context, c cart.MenuClaim, s int64) (menugate.Receipt, error) {
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	return g.ClarificationGate.QueueClarification(canceled, c, s)
}

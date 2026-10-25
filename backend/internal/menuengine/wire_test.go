package menuengine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	cc "restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/menuengine"
)

const fakeKey = "fabricated-menu-engine-key"
const privateText = "synthetic-private-reasoning-SQL-DSN-key"

func ptr(s string) *string { return &s }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type wireRequest struct {
	Model    string       `json:"model"`
	Messages []cc.Message `json:"messages"`
	Tools    []struct {
		Type     string            `json:"type"`
		Function cc.ToolDefinition `json:"function"`
	} `json:"tools"`
	ToolChoice string `json:"tool_choice"`
	Stream     bool   `json:"stream"`
	MaxTokens  int    `json:"max_tokens"`
}

type wireTrace struct {
	mu       sync.Mutex
	requests []wireRequest
}

func (tr *wireTrace) count() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.requests)
}
func (tr *wireTrace) request(i int) wireRequest {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.requests[i]
}

// Every model operation goes through an injected in-memory transport; no socket,
// default transport, ambient key, or provider account is used.
func testClient(t *testing.T, respond func(int, wireRequest) (*http.Response, error)) (*cc.Client, *wireTrace) {
	t.Helper()
	tr := new(wireTrace)
	client, err := cc.New(cc.Config{APIKey: fakeKey, Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "POST" || req.URL.String() != "https://api.commandcode.ai/provider/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer "+fakeKey || req.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected model HTTP boundary")
		}
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var w wireRequest
		if err := json.Unmarshal(data, &w); err != nil {
			return nil, err
		}
		if w.Model != "deepseek/deepseek-v4.1-flash" || w.Stream || w.MaxTokens != 321 || w.ToolChoice != "auto" {
			t.Error("unexpected model/options")
		}
		if len(w.Tools) != 1 || w.Tools[0].Type != "function" || w.Tools[0].Function.Name != "get_menu" {
			t.Error("unexpected tool surface")
		} else {
			var schema any
			if err := json.Unmarshal(w.Tools[0].Function.Parameters, &schema); err != nil {
				t.Error(err)
			}
			want := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
			if !reflect.DeepEqual(schema, want) {
				t.Error("unexpected zero-argument schema")
			}
		}
		tr.mu.Lock()
		tr.requests = append(tr.requests, w)
		n := len(tr.requests)
		tr.mu.Unlock()
		return respond(n, w)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return client, tr
}
func response(message cc.Message, finish string) *http.Response {
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
func answer() *http.Response {
	return response(cc.Message{Role: cc.RoleAssistant, Content: ptr("menu proposal"), ReasoningContent: ptr(privateText)}, "stop")
}
func call() cc.ToolCall {
	return cc.ToolCall{ID: "menu_call_1", Type: "function", Function: cc.FunctionCall{Name: "get_menu", Arguments: " \n{ \t } "}}
}
func toolMessage(calls ...cc.ToolCall) cc.Message {
	return cc.Message{Role: cc.RoleAssistant, Content: ptr("partial proposal"), ReasoningContent: ptr(privateText), ToolCalls: calls}
}
func newEngine(t *testing.T, c *cc.Client, read menuengine.MenuReader) *menuengine.Engine {
	t.Helper()
	e, err := menuengine.New(menuengine.Config{Client: c, ReadMenu: read, MaxOutputTokens: 321})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func menuFixture() catalog.MenuSnapshot {
	return catalog.MenuSnapshot{Settings: catalog.Settings{Currency: "USD", CurrencyExponent: 2, FlatFeeMinor: 300, Open: false, Version: 7}, Items: []catalog.Item{
		{ID: "10000000-0000-4000-8000-000000000001", Name: "料理 <&>", PriceMinor: math.MaxInt64, Currency: "USD", Available: false, Version: 9},
		{ID: "10000000-0000-4000-8000-000000000002", Name: "Water", PriceMinor: 0, Currency: "USD", Available: true, Version: 2},
	}}
}

// This independent public wire schema is deliberately not the engine encoder.
type menuWire struct {
	Open  bool       `json:"open"`
	Items []itemWire `json:"items"`
}
type itemWire struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Available bool      `json:"available"`
	Price     priceWire `json:"price"`
}
type priceWire struct {
	Amount   string `json:"amount_minor"`
	Currency string `json:"currency"`
	Exponent int16  `json:"exponent"`
}

func expectedWire(t *testing.T, s catalog.MenuSnapshot) string {
	t.Helper()
	w := menuWire{Open: s.Settings.Open, Items: make([]itemWire, 0, len(s.Items))}
	for _, it := range s.Items {
		w.Items = append(w.Items, itemWire{it.ID, it.Name, it.Available, priceWire{strconv.FormatInt(it.PriceMinor, 10), it.Currency, s.Settings.CurrencyExponent}})
	}
	data, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMenuTurnReplayAndClosedWire(t *testing.T) {
	for ri, reason := range []*string{nil, ptr(""), ptr(privateText)} {
		for ci, content := range []*string{nil, ptr(""), ptr("partial proposal")} {
			t.Run(fmt.Sprintf("reason%d-content%d", ri, ci), func(t *testing.T) {
				snapshot := menuFixture()
				before, _ := json.Marshal(snapshot)
				reads := 0
				first := toolMessage(call())
				first.Content = content
				first.ReasoningContent = reason
				c, tr := testClient(t, func(n int, w wireRequest) (*http.Response, error) {
					if n == 1 {
						if len(w.Messages) != 2 || w.Messages[0].Role != cc.RoleSystem || w.Messages[0].Content == nil || *w.Messages[0].Content == "" || w.Messages[1].Role != cc.RoleUser || w.Messages[1].Content == nil || *w.Messages[1].Content != "  show menu\n" {
							t.Error("initial history/text changed")
						}
						return response(first, "tool_calls"), nil
					}
					if n != 2 || len(w.Messages) != 4 {
						t.Fatalf("unexpected continuation length/attempt %d", n)
					}
					if !reflect.DeepEqual(w.Messages[2], first) {
						t.Error("assistant content/reasoning/ID/raw arguments changed")
					}
					tool := w.Messages[3]
					if tool.Role != cc.RoleTool || tool.ToolCallID != call().ID || tool.Content == nil || *tool.Content != expectedWire(t, snapshot) {
						t.Error("matching complete tool result missing")
					}
					return answer(), nil
				})
				e := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return snapshot, nil })
				got, err := e.Run(t.Context(), "  show menu\n")
				if err != nil || got.Reply != "menu proposal" || got.Menu == nil || got.Menu.Open || reads != 1 || tr.count() != 2 {
					t.Fatalf("tool result %+v %v reads=%d attempts=%d", got, err, reads, tr.count())
				}
				if !reflect.DeepEqual(tr.request(0).Messages, tr.request(1).Messages[:2]) || !reflect.DeepEqual(tr.request(0).Tools, tr.request(1).Tools) {
					t.Fatal("continuation changed original messages/tools")
				}
				after, _ := json.Marshal(snapshot)
				if string(before) != string(after) {
					t.Fatal("caller snapshot mutated")
				}
			})
		}
	}
}

func TestMenuTurnEmptyWire(t *testing.T) {
	s := menuFixture()
	s.Settings.Open = true
	s.Items = []catalog.Item{}
	c, tr := testClient(t, func(n int, w wireRequest) (*http.Response, error) {
		if n == 1 {
			return response(toolMessage(call()), "tool_calls"), nil
		}
		if len(w.Messages) != 4 || w.Messages[3].Content == nil || *w.Messages[3].Content != `{"open":true,"items":[]}` {
			t.Fatal("empty menu must be an array")
		}
		return answer(), nil
	})
	got, err := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { return s, nil }).Run(t.Context(), "menu")
	if err != nil || got.Menu == nil || !got.Menu.Open || tr.count() != 2 {
		t.Fatalf("empty result %+v %v", got, err)
	}
}

func sizedMenu(t *testing.T, size int) catalog.MenuSnapshot {
	t.Helper()
	s := menuFixture()
	s.Items = make([]catalog.Item, 1000)
	for i := range s.Items {
		s.Items[i] = catalog.Item{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), Name: "x", PriceMinor: 1, Currency: "USD", Available: true}
	}
	remaining := size - len(expectedWire(t, s))
	if remaining < 0 {
		t.Fatal("target below fixture baseline")
	}
	for i := range s.Items {
		n := min(199, remaining)
		s.Items[i].Name += strings.Repeat("x", n)
		remaining -= n
	}
	if remaining != 0 || len(expectedWire(t, s)) != size {
		t.Fatal("cannot independently construct target wire length")
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestMenuTurnWireBudget(t *testing.T) {
	for _, size := range []int{256 << 10, (256 << 10) + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			s := sizedMenu(t, size)
			reads := 0
			c, tr := testClient(t, func(n int, w wireRequest) (*http.Response, error) {
				if n == 1 {
					return response(toolMessage(call()), "tool_calls"), nil
				}
				if w.Messages[3].Content == nil || len(*w.Messages[3].Content) != size || *w.Messages[3].Content != expectedWire(t, s) {
					t.Fatal("encoded tool boundary differs")
				}
				return answer(), nil
			})
			got, err := newEngine(t, c, func(context.Context) (catalog.MenuSnapshot, error) { reads++; return s, nil }).Run(t.Context(), "menu")
			if size == 256<<10 {
				if err != nil || got.Menu == nil || tr.count() != 2 {
					t.Fatalf("inclusive limit %+v %v", got, err)
				}
			} else {
				zeroError(t, got, err, menuengine.ErrResource)
				if tr.count() != 1 {
					t.Fatal("overflow made continuation")
				}
			}
			if reads != 1 {
				t.Fatal("wrong lookup count")
			}
		})
	}
}

package commandcode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	cc "restaurant-assistant/backend/internal/commandcode"
	"strings"
	"testing"
)

type independentRT func(*http.Request) (*http.Response, error)

func (f independentRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type independentBody struct {
	io.Reader
	closed bool
}

func (b *independentBody) Close() error { b.closed = true; return nil }
func TestIndependentResponseBoundary(t *testing.T) {
	good := `{"id":"new","type":"function","function":{"name":"lookup","arguments":"{ \"q\" : 1 }"}}`
	tests := []struct {
		name, calls string
		want        error
	}{
		{"valid raw arguments", good, nil},
		{"late undeclared call", good + `,{"id":"next","type":"function","function":{"name":"other","arguments":"{}"}}`, cc.ErrProtocol},
		{"historical ID reuse", strings.Replace(good, `"new"`, `"old"`, 1), cc.ErrProtocol},
		{"function case alias", strings.Replace(good, `"function":`, `"Function":`, 1), cc.ErrProtocol},
		{"arguments case alias", strings.Replace(good, `"arguments":`, `"Arguments":`, 1), cc.ErrProtocol},
		{"null identity", strings.Replace(good, `"id":"new"`, `"id":null`, 1), cc.ErrProtocol},
		{"null function", `{"id":"new","type":"function","function":null}`, cc.ErrProtocol},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			empty := ""
			request := cc.Request{MaxOutputTokens: 1, Tools: []cc.ToolDefinition{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}, Messages: []cc.Message{
				{Role: cc.RoleUser, Content: &empty},
				{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{{ID: "old", Type: "function", Function: cc.FunctionCall{Name: "lookup", Arguments: `{}`}}}},
				{Role: cc.RoleTool, ToolCallID: "old", Content: &empty},
			}}
			before, _ := json.Marshal(request)
			body := &independentBody{Reader: strings.NewReader(`{"extra":{"ok":true},"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"sensitive fixture reasoning","tool_calls":[` + tc.calls + `]},"finish_reason":"tool_calls"}]}`)}
			attempts := 0
			client, err := cc.New(cc.Config{APIKey: "fabricated-independent-key", Transport: independentRT(func(*http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Complete(context.Background(), request)
			after, _ := json.Marshal(request)
			if attempts != 1 || !body.closed || string(before) != string(after) {
				t.Fatal("attempt/body/input invariant")
			}
			if tc.want != nil {
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, cc.Result{}) {
					t.Fatalf("got %#v, %v", got, err)
				}
				if err.Error() != "commandcode: invalid response" {
					t.Fatalf("unexpected public error: %q", err)
				}
			} else if err != nil || len(got.Message.ToolCalls) != 1 || got.Message.ToolCalls[0].Function.Arguments != `{ "q" : 1 }` || got.Message.ReasoningContent == nil || *got.Message.ReasoningContent != "sensitive fixture reasoning" {
				t.Fatalf("lossy result %#v, %v", got, err)
			}
		})
	}
}
func TestIndependentCanceledResponseBranches(t *testing.T) {
	for _, status := range []int{200, 401, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &independentBody{Reader: strings.NewReader(`{}`)}
			attempts := 0
			client, err := cc.New(cc.Config{APIKey: "fabricated-independent-key", Transport: independentRT(func(r *http.Request) (*http.Response, error) {
				attempts++
				cancel()
				<-r.Context().Done()
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			text := "synthetic"
			got, err := client.Complete(ctx, cc.Request{MaxOutputTokens: 1, Messages: []cc.Message{{Role: cc.RoleUser, Content: &text}}})
			if attempts != 1 || !body.closed || !reflect.DeepEqual(got, cc.Result{}) {
				t.Fatal("attempt/body/zero-result invariant")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("observable cancellation lost: %v", err)
			}
		})
	}
}

type independentGuideTransport struct{}

func (independentGuideTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Synthetic answer."},"finish_reason":"stop"}]}`))}, nil
}
func TestIndependentGuide(t *testing.T) {
	client, err := cc.New(cc.Config{APIKey: "fabricated-example-key", Transport: independentGuideTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	text := "Synthetic fixture question."
	got, err := client.Complete(context.Background(), cc.Request{Messages: []cc.Message{{Role: cc.RoleUser, Content: &text}}, MaxOutputTokens: 64})
	if err != nil || got.Message.Content == nil || *got.Message.Content != "Synthetic answer." || got.FinishReason != "stop" {
		t.Fatalf("guide result %#v, %v", got, err)
	}
}

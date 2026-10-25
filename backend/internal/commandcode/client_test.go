package commandcode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	cc "restaurant-assistant/backend/internal/commandcode"
)

const fakeKey = "fabricated-only-key"
const answer = `{"choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}]}`

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
	n      int
	fail   bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if b.fail {
		return 0, errors.New(fakeKey + " private prompt")
	}
	n, e := b.Reader.Read(p)
	b.n += n
	return n, e
}
func (b *trackedBody) Close() error { b.closed = true; return nil }
func ptr(s string) *string          { return &s }
func request() cc.Request {
	return cc.Request{Messages: []cc.Message{{Role: cc.RoleUser, Content: ptr("private prompt")}}, MaxOutputTokens: 64}
}
func tool() cc.ToolDefinition {
	return cc.ToolDefinition{Name: "fixture_lookup", Description: "synthetic only", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}
}
func call(id string) cc.ToolCall {
	return cc.ToolCall{ID: id, Type: "function", Function: cc.FunctionCall{Name: "fixture_lookup", Arguments: `{ "q" : "sample" }`}}
}
func response(body *trackedBody, status int, ct string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{ct}}, Body: body}
}
func client(t *testing.T, f rtFunc) *cc.Client {
	t.Helper()
	c, e := cc.New(cc.Config{APIKey: fakeKey, Transport: f})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func safeError(t *testing.T, got cc.Result, e error, want error) {
	t.Helper()
	if e == nil || !reflect.DeepEqual(got, cc.Result{}) {
		t.Fatalf("expected zero result/error: %#v %v", got, e)
	}
	if want != nil && !errors.Is(e, want) {
		t.Fatalf("wrong error: %v, want %v", e, want)
	}
	for _, secret := range []string{fakeKey, "private prompt", "attacker.invalid"} {
		if strings.Contains(fmt.Sprintf("%+v", e), secret) {
			t.Fatalf("error leaked private value")
		}
	}
}

func TestConfig(t *testing.T) {
	for _, key := range []string{"", " \t\n", "a\nb", "a b", "a\x00b", "a\x7fb", "a\u0085b", "a\xff", strings.Repeat("a", 4097)} {
		t.Run(fmt.Sprintf("invalid-%q", key), func(t *testing.T) {
			n := 0
			c, e := cc.New(cc.Config{APIKey: key, Transport: rtFunc(func(*http.Request) (*http.Response, error) { n++; return nil, nil })})
			if c != nil || e == nil || !errors.Is(e, cc.ErrInput) || n != 0 {
				t.Fatalf("invalid config accepted: %v %d", e, n)
			}
			if key != "" && strings.Contains(e.Error(), key) {
				t.Fatal("key leaked")
			}
		})
	}
	// A valid key may have surrounding whitespace; no environment fallback is used.
	c, e := cc.New(cc.Config{APIKey: "  " + fakeKey + "  ", Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+fakeKey {
			t.Error("key not trimmed")
		}
		return response(&trackedBody{Reader: strings.NewReader(answer)}, 200, "application/json"), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Complete(context.Background(), request()); e != nil {
		t.Fatal(e)
	}
	// Construction only: never send using the private default transport.
	if c, e := cc.New(cc.Config{APIKey: fakeKey}); c == nil || e != nil {
		t.Fatalf("default constructor: %v", e)
	}
}

func TestInvalidRequestsNeverSend(t *testing.T) {
	cases := map[string]func(*cc.Request){
		"no messages": func(r *cc.Request) { r.Messages = nil },
		"too many messages": func(r *cc.Request) {
			for len(r.Messages) < 129 {
				r.Messages = append(r.Messages, r.Messages[0])
			}
		},
		"zero tokens": func(r *cc.Request) { r.MaxOutputTokens = 0 }, "too many tokens": func(r *cc.Request) { r.MaxOutputTokens = 8193 },
		"unknown role":         func(r *cc.Request) { r.Messages[0].Role = "admin" },
		"user missing content": func(r *cc.Request) { r.Messages[0].Content = nil },
		"user reasoning":       func(r *cc.Request) { r.Messages[0].ReasoningContent = ptr("") },
		"user calls":           func(r *cc.Request) { r.Messages[0].ToolCalls = []cc.ToolCall{call("a")} },
		"user tool id":         func(r *cc.Request) { r.Messages[0].ToolCallID = "a" },
		"invalid UTF8":         func(r *cc.Request) { r.Messages[0].Content = ptr("\xff") },
		"NUL":                  func(r *cc.Request) { r.Messages[0].Content = ptr("a\x00b") },
		"oversize request":     func(r *cc.Request) { r.Messages[0].Content = ptr(strings.Repeat("x", 1<<20)) },
		"duplicate names":      func(r *cc.Request) { r.Tools = []cc.ToolDefinition{tool(), tool()} },
		"too many tools": func(r *cc.Request) {
			for i := 0; i < 33; i++ {
				v := tool()
				v.Name = fmt.Sprintf("t%d", i)
				r.Tools = append(r.Tools, v)
			}
		},
		"bad function name":  func(r *cc.Request) { v := tool(); v.Name = "bad name"; r.Tools = []cc.ToolDefinition{v} },
		"long function name": func(r *cc.Request) { v := tool(); v.Name = strings.Repeat("x", 65); r.Tools = []cc.ToolDefinition{v} },
		"bad description":    func(r *cc.Request) { v := tool(); v.Description = "\x00"; r.Tools = []cc.ToolDefinition{v} },
		"bad schema": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`[]`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"missing schema type": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"schema malformed": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"schema properties array": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{"type":"object","properties":[]}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"schema property scalar": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{"type":"object","properties":{"x":true}}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"schema required scalar": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{"type":"object","required":true}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"schema required duplicate": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{"type":"object","required":["x","x"]}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"encoded escaping over limit": func(r *cc.Request) { r.Messages[0].Content = ptr(strings.Repeat("\t", 600000)) },
		"schema duplicate": func(r *cc.Request) {
			v := tool()
			v.Parameters = json.RawMessage(`{"type":"object","type":"array"}`)
			r.Tools = []cc.ToolDefinition{v}
		},
		"orphan result": func(r *cc.Request) {
			r.Messages = append(r.Messages, cc.Message{Role: cc.RoleTool, Content: ptr("{}"), ToolCallID: "a"})
		},
		"assistant tool id": func(r *cc.Request) {
			r.Messages = append(r.Messages, cc.Message{Role: cc.RoleAssistant, Content: ptr("ok"), ToolCallID: "a"})
		},
		"assistant no content": func(r *cc.Request) { r.Messages = append(r.Messages, cc.Message{Role: cc.RoleAssistant}) },
		"assistant reasoning only": func(r *cc.Request) {
			r.Messages = append(r.Messages, cc.Message{Role: cc.RoleAssistant, ReasoningContent: ptr("opaque")})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := request()
			change(&r)
			n := 0
			c := client(t, func(*http.Request) (*http.Response, error) { n++; return nil, errors.New("must not send") })
			got, e := c.Complete(context.Background(), r)
			safeError(t, got, e, cc.ErrInput)
			if n != 0 {
				t.Fatalf("attempts %d", n)
			}
		})
	}
	n := 0
	c := client(t, func(*http.Request) (*http.Response, error) { n++; return nil, nil })
	got, e := c.Complete(nil, request())
	safeError(t, got, e, cc.ErrInput)
	if n != 0 {
		t.Fatal("nil ctx sent")
	}
}

func TestInvalidToolHistory(t *testing.T) {
	cases := map[string]func(*cc.Request){
		"unknown call name":    func(r *cc.Request) { r.Messages[1].ToolCalls[0].Function.Name = "undeclared" },
		"bad call type":        func(r *cc.Request) { r.Messages[1].ToolCalls[0].Type = "other" },
		"empty call id":        func(r *cc.Request) { r.Messages[1].ToolCalls[0].ID = "" },
		"bad call id":          func(r *cc.Request) { r.Messages[1].ToolCalls[0].ID = "a b" },
		"long call id":         func(r *cc.Request) { r.Messages[1].ToolCalls[0].ID = strings.Repeat("a", 129) },
		"array args":           func(r *cc.Request) { r.Messages[1].ToolCalls[0].Function.Arguments = `[]` },
		"malformed args":       func(r *cc.Request) { r.Messages[1].ToolCalls[0].Function.Arguments = `{` },
		"duplicate args":       func(r *cc.Request) { r.Messages[1].ToolCalls[0].Function.Arguments = `{"q":1,"q":2}` },
		"duplicate calls":      func(r *cc.Request) { r.Messages[1].ToolCalls = append(r.Messages[1].ToolCalls, call("a")) },
		"reused result":        func(r *cc.Request) { r.Messages = append(r.Messages, r.Messages[2]) },
		"tool missing content": func(r *cc.Request) { r.Messages[2].Content = nil },
		"tool reasoning":       func(r *cc.Request) { r.Messages[2].ReasoningContent = ptr("") },
		"tool calls":           func(r *cc.Request) { r.Messages[2].ToolCalls = []cc.ToolCall{call("b")} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := request()
			r.Tools = []cc.ToolDefinition{tool()}
			r.Messages = append(r.Messages, cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{call("a")}}, cc.Message{Role: cc.RoleTool, ToolCallID: "a", Content: ptr("{}")})
			change(&r)
			n := 0
			c := client(t, func(*http.Request) (*http.Response, error) { n++; return nil, nil })
			got, e := c.Complete(context.Background(), r)
			safeError(t, got, e, cc.ErrInput)
			if n != 0 {
				t.Fatal("invalid history sent")
			}
		})
	}
}

func TestResponsesAndSafeFailures(t *testing.T) {
	cases := []struct {
		name, body, ct string
		status         int
		want           error
		readFail       bool
	}{
		{"answer with metadata", `{"id":"untrusted","model":"alias","usage":{"extra":true},"choices":[{"index":0,"message":{"role":"assistant","content":"ok","extra":7},"finish_reason":"stop","extra":true}]}`, "application/json", 200, nil, false},
		{"invalid utf8", "\xff", "application/json", 200, cc.ErrProtocol, false},
		{"invalid json", "{", "application/json", 200, cc.ErrProtocol, false},
		{"trailing json", answer + `{}`, "application/json", 200, cc.ErrProtocol, false},
		{"duplicate critical", `{"choices":[],"choices":[]}`, "application/json", 200, cc.ErrProtocol, false},
		{"duplicate nested", `{"choices":[{"index":0,"message":{"role":"assistant","role":"user","content":"ok"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"no choice", `{"choices":[]}`, "application/json", 200, cc.ErrProtocol, false},
		{"two choices", `{"choices":[{},{}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"wrong index", `{"choices":[{"index":1,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"missing index", `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"wrong role", `{"choices":[{"index":0,"message":{"role":"user","content":"ok"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"empty answer", `{"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"null answer", `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"reasoning only", `{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"opaque"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"length", `{"choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`, "application/json", 200, cc.ErrIncomplete, false},
		{"filter", `{"choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"content_filter"}]}`, "application/json", 200, cc.ErrRefusal, false},
		{"refusal field", `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","refusal":"private prompt"},"finish_reason":"stop"}]}`, "application/json", 200, cc.ErrRefusal, false},
		{"refusal finish", `{"choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"refusal"}]}`, "application/json", 200, cc.ErrRefusal, false},
		{"missing finish", `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer"}}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"unknown finish", `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"other"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"empty tool finish", `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"tool_calls"}]}`, "application/json", 200, cc.ErrProtocol, false},
		{"SSE", answer, "text/event-stream", 200, cc.ErrProtocol, false},
		{"missing content type", answer, "", 200, cc.ErrProtocol, false},
		{"oversize", strings.Repeat("x", 512*1024+1), "application/json", 200, cc.ErrResource, false},
		{"read failure", answer, "application/json", 200, cc.ErrUnavailable, true},
	}
	for _, status := range []int{301, 302, 307, 308, 400, 401, 403, 404, 429, 500, 503} {
		want := cc.ErrHTTP
		switch {
		case status == 401 || status == 403:
			want = cc.ErrAccess
		case status == 429:
			want = cc.ErrRateLimit
		case status >= 500:
			want = cc.ErrUnavailable
		}
		cases = append(cases, struct {
			name, body, ct string
			status         int
			want           error
			readFail       bool
		}{fmt.Sprint(status), fakeKey + " private prompt", "application/json", status, want, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			b := &trackedBody{Reader: strings.NewReader(tc.body), fail: tc.readFail}
			c := client(t, func(*http.Request) (*http.Response, error) {
				n++
				res := response(b, tc.status, tc.ct)
				res.Header.Set("Location", "https://attacker.invalid/stolen")
				return res, nil
			})
			got, e := c.Complete(context.Background(), request())
			if tc.want == nil {
				if e != nil || got.Message.Content == nil || *got.Message.Content != "ok" || got.FinishReason != "stop" {
					t.Fatalf("result: %#v %v", got, e)
				}
			} else {
				safeError(t, got, e, tc.want)
				if tc.status != 200 {
					var he *cc.HTTPError
					if !errors.As(e, &he) || he.StatusCode != tc.status {
						t.Fatalf("missing numeric status: %v", e)
					}
				}
			}
			if n != 1 || !b.closed || b.n > 512*1024+1 {
				t.Fatalf("attempts=%d closed=%v read=%d", n, b.closed, b.n)
			}
		})
	}
}

func TestNoToolsWirePreservesOrdinaryCompletion(t *testing.T) {
	c := client(t, func(req *http.Request) (*http.Response, error) {
		var fields map[string]json.RawMessage
		if e := json.NewDecoder(req.Body).Decode(&fields); e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{"tools", "tool_choice", "response_format", "thinking"} {
			if _, exists := fields[key]; exists {
				t.Errorf("unexpected no-tool field %s", key)
			}
		}
		if string(fields["max_tokens"]) != "64" || string(fields["stream"]) != "false" {
			t.Fatal("ordinary completion changed")
		}
		return response(&trackedBody{Reader: strings.NewReader(answer)}, 200, "application/json"), nil
	})
	got, e := c.Complete(context.Background(), request())
	if e != nil || got.Message.Content == nil || *got.Message.Content != "fixture answer" {
		t.Fatalf("ordinary answer: %#v %v", got, e)
	}
}

func TestCancellationDuringBodyRead(t *testing.T) {
	entered := make(chan struct{})
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	var body *trackedBody
	c := client(t, func(req *http.Request) (*http.Response, error) {
		n++
		body = &trackedBody{Reader: readerFunc(func([]byte) (int, error) {
			close(entered)
			<-req.Context().Done()
			return 0, fmt.Errorf("%s private prompt: %w", fakeKey, req.Context().Err())
		})}
		return response(body, 200, "application/json"), nil
	})
	go func() {
		defer close(done)
		got, e := c.Complete(ctx, request())
		safeError(t, got, e, context.Canceled)
	}()
	<-entered
	cancel()
	<-done
	if n != 1 || !body.closed {
		t.Fatal("cancelled body leaked or retried")
	}
}

func TestResponseRejectsCaseAliasesAndExtraneousIdentity(t *testing.T) {
	bodies := []string{
		strings.Replace(answer, `"choices":`, `"Choices":`, 1),
		strings.Replace(answer, `"index":0`, `"index":0,"Index":0`, 1),
		strings.Replace(answer, `"content":"fixture answer"`, `"content":"fixture answer","Content":"hidden"`, 1),
		strings.Replace(answer, `"content":`, `"tool_call_id":null,"content":`, 1),
	}
	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			n := 0
			b := &trackedBody{Reader: strings.NewReader(body)}
			c := client(t, func(*http.Request) (*http.Response, error) { n++; return response(b, 200, "application/json"), nil })
			got, e := c.Complete(context.Background(), request())
			safeError(t, got, e, cc.ErrProtocol)
			if n != 1 || !b.closed {
				t.Fatal("body leaked or retried")
			}
		})
	}
}

func TestResponseAndTransportErrorClosesBody(t *testing.T) {
	b := &trackedBody{Reader: strings.NewReader(answer)}
	n := 0
	c := client(t, func(*http.Request) (*http.Response, error) {
		n++
		return response(b, 200, "application/json"), errors.New(fakeKey + " private prompt")
	})
	got, e := c.Complete(context.Background(), request())
	safeError(t, got, e, cc.ErrUnavailable)
	if n != 1 || !b.closed {
		t.Fatal("body leaked or retried")
	}
}

func TestTransportCancellationIdentity(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			n := 0
			c := client(t, func(*http.Request) (*http.Response, error) {
				n++
				return nil, fmt.Errorf("%s private prompt: %w", fakeKey, cause)
			})
			got, e := c.Complete(context.Background(), request())
			safeError(t, got, e, cause)
			if n != 1 {
				t.Fatal("retried")
			}
		})
	}
}

func TestCancellationAndTransportErrors(t *testing.T) {
	t.Run("pre-cancel", func(t *testing.T) {
		n := 0
		c := client(t, func(*http.Request) (*http.Response, error) { n++; return nil, nil })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, e := c.Complete(ctx, request())
		safeError(t, got, e, context.Canceled)
		if n != 0 {
			t.Fatal("cancelled request sent")
		}
	})
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("barrier-", deadline), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(5*time.Second))
				defer stop()
			}
			n := 0
			c := client(t, func(r *http.Request) (*http.Response, error) {
				n++
				d, ok := r.Context().Deadline()
				if !ok || time.Until(d) > 10*time.Second || time.Until(d) <= 0 {
					t.Error("missing bounded deadline")
				}
				if deadline {
					parentDeadline, _ := ctx.Deadline()
					if !d.Equal(parentDeadline) {
						t.Error("earlier caller deadline changed")
					}
				}
				close(entered)
				<-release
				<-r.Context().Done()
				return nil, errors.New(fakeKey + " private prompt")
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				got, e := c.Complete(ctx, request())
				safeError(t, got, e, context.Canceled)
			}()
			<-entered
			cancel()
			close(release)
			<-done
			if n != 1 {
				t.Fatal("retried")
			}
		})
	}
	t.Run("expired deadline", func(t *testing.T) {
		n := 0
		c := client(t, func(*http.Request) (*http.Response, error) { n++; return nil, nil })
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		got, e := c.Complete(ctx, request())
		safeError(t, got, e, context.DeadlineExceeded)
		if n != 0 {
			t.Fatal("expired request sent")
		}
	})
	t.Run("redacted transport", func(t *testing.T) {
		n := 0
		c := client(t, func(*http.Request) (*http.Response, error) {
			n++
			return nil, errors.New(fakeKey + " private prompt https://attacker.invalid")
		})
		got, e := c.Complete(context.Background(), request())
		safeError(t, got, e, cc.ErrUnavailable)
		if n != 1 {
			t.Fatal("retried")
		}
	})
}

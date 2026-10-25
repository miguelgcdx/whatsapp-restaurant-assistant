package commandcode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cc "restaurant-assistant/backend/internal/commandcode"
)

func TestTwoExplicitTurnsPreserveWireAndInputs(t *testing.T) {
	for ri, reason := range []*string{nil, ptr(""), ptr("opaque private reasoning")} {
		for ci, content := range []*string{nil, ptr("")} {
			t.Run(fmt.Sprintf("reason-%d-content-%d", ri, ci), func(t *testing.T) {
				r := request()
				r.MaxOutputTokens = 8192
				r.Tools = []cc.ToolDefinition{tool()}
				second := tool()
				second.Name = "fixture_second"
				r.Tools = append(r.Tools, second)
				r.Messages = []cc.Message{{Role: cc.RoleSystem, Content: ptr("synthetic rules")}, {Role: cc.RoleUser, Content: ptr("earlier")}, {Role: cc.RoleAssistant, Content: ptr("earlier answer"), ReasoningContent: ptr("earlier reasoning")}, {Role: cc.RoleUser, Content: ptr("later")}}
				initial, _ := json.Marshal(r)
				n := 0
				c := client(t, func(req *http.Request) (*http.Response, error) {
					n++
					if req.Method != "POST" || req.URL.String() != "https://api.commandcode.ai/provider/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer "+fakeKey || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Accept") != "application/json" {
						t.Error("wrong actual HTTP route/headers")
					}
					data, e := io.ReadAll(req.Body)
					if e != nil {
						t.Fatal(e)
					}
					var wire map[string]json.RawMessage
					if e = json.Unmarshal(data, &wire); e != nil {
						t.Fatal(e)
					}
					if string(wire["model"]) != `"deepseek/deepseek-v4.1-flash"` || string(wire["stream"]) != "false" || string(wire["max_tokens"]) != "8192" || string(wire["tool_choice"]) != `"auto"` {
						t.Fatalf("wrong wire options: %s", data)
					}
					for _, key := range []string{"response_format", "thinking", "reasoning_effort", "strict"} {
						if _, ok := wire[key]; ok {
							t.Errorf("unexpected option %s", key)
						}
					}
					var tools []struct {
						Type     string
						Function cc.ToolDefinition
					}
					if e = json.Unmarshal(wire["tools"], &tools); e != nil || len(tools) != 2 || tools[0].Type != "function" || tools[0].Function.Name != "fixture_lookup" || tools[1].Function.Name != "fixture_second" {
						t.Fatalf("tools order/type: %s", data)
					}
					var schema any
					var original any
					_ = json.Unmarshal(tools[0].Function.Parameters, &schema)
					_ = json.Unmarshal(tool().Parameters, &original)
					if !reflect.DeepEqual(schema, original) {
						t.Fatal("schema changed")
					}
					var messages []map[string]json.RawMessage
					if e = json.Unmarshal(wire["messages"], &messages); e != nil {
						t.Fatal(e)
					}
					expected := 4
					if n == 2 {
						expected = 6
					}
					if len(messages) != expected {
						t.Fatalf("history count: %s", data)
					}
					roles := []string{"system", "user", "assistant", "user", "assistant", "tool"}
					for i, m := range messages {
						if string(m["role"]) != `"`+roles[i]+`"` {
							t.Fatalf("role order: %s", data)
						}
					}
					if string(messages[2]["reasoning_content"]) != `"earlier reasoning"` || string(messages[2]["content"]) != `"earlier answer"` {
						t.Fatal("prior assistant before later user dropped")
					}
					if n == 2 {
						m := messages[4]
						if content == nil {
							if string(m["content"]) != "null" {
								t.Fatal("null content lost")
							}
						} else if string(m["content"]) != `""` {
							t.Fatal("empty content lost")
						}
						if reason == nil {
							if _, ok := m["reasoning_content"]; ok {
								t.Fatal("nil reasoning backfilled")
							}
						} else {
							b, _ := json.Marshal(*reason)
							if string(m["reasoning_content"]) != string(b) {
								t.Fatal("reasoning changed")
							}
						}
						var calls []cc.ToolCall
						_ = json.Unmarshal(m["tool_calls"], &calls)
						if !reflect.DeepEqual(calls, []cc.ToolCall{call("call_1"), call("call_2")}) {
							t.Fatalf("arguments/IDs changed: %#v", calls)
						}
						if string(messages[5]["tool_call_id"]) != `"call_1"` || string(messages[5]["content"]) != `"{ \"synthetic\" : true }"` {
							t.Fatal("tool result changed")
						}
						return response(&trackedBody{Reader: strings.NewReader(answer)}, 200, "application/json; charset=utf-8"), nil
					}
					m := cc.Message{Role: cc.RoleAssistant, Content: content, ReasoningContent: reason, ToolCalls: []cc.ToolCall{call("call_1"), call("call_2")}}
					encoded, _ := json.Marshal(m)
					body := `{"choices":[{"index":0,"message":` + string(encoded) + `,"finish_reason":"tool_calls"}]}`
					return response(&trackedBody{Reader: strings.NewReader(body)}, 200, "application/json"), nil
				})
				first, e := c.Complete(context.Background(), r)
				if e != nil {
					t.Fatal(e)
				}
				after, _ := json.Marshal(r)
				if string(initial) != string(after) || n != 1 {
					t.Fatal("input mutated or automatic tool loop")
				}
				if !reflect.DeepEqual(first.Message.Content, content) || !reflect.DeepEqual(first.Message.ReasoningContent, reason) || !reflect.DeepEqual(first.Message.ToolCalls, []cc.ToolCall{call("call_1"), call("call_2")}) {
					t.Fatalf("lossy decoded result: %#v", first)
				}
				r.Messages = append(r.Messages, first.Message, cc.Message{Role: cc.RoleTool, ToolCallID: "call_1", Content: ptr(`{ "synthetic" : true }`)})
				before, _ := json.Marshal(r)
				last, e := c.Complete(context.Background(), r)
				if e != nil || last.Message.Content == nil || *last.Message.Content != "fixture answer" || n != 2 {
					t.Fatalf("second explicit call: %#v %v attempts %d", last, e, n)
				}
				after, _ = json.Marshal(r)
				if string(before) != string(after) {
					t.Fatal("history mutated")
				}
			})
		}
	}
}

func TestResponseToolValidation(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown name": func(m map[string]any) {
			m["tool_calls"] = []cc.ToolCall{{ID: "a", Type: "function", Function: cc.FunctionCall{Name: "undeclared", Arguments: `{}`}}}
		},
		"duplicate IDs": func(m map[string]any) { m["tool_calls"] = []cc.ToolCall{call("a"), call("a")} },
		"invalid id":    func(m map[string]any) { m["tool_calls"] = []cc.ToolCall{call("a b")} },
		"bad type":      func(m map[string]any) { v := call("a"); v.Type = "bad"; m["tool_calls"] = []cc.ToolCall{v} },
		"bad args": func(m map[string]any) {
			v := call("a")
			v.Function.Arguments = `null`
			m["tool_calls"] = []cc.ToolCall{v}
		},
		"duplicate args": func(m map[string]any) {
			v := call("a")
			v.Function.Arguments = `{"a":1,"a":2}`
			m["tool_calls"] = []cc.ToolCall{v}
		},
		"invalid text":       func(m map[string]any) { m["content"] = "x\x00y" },
		"extraneous tool ID": func(m map[string]any) { m["tool_call_id"] = "a" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := request()
			r.Tools = []cc.ToolDefinition{tool()}
			m := map[string]any{"role": "assistant", "content": nil, "tool_calls": []cc.ToolCall{call("a")}}
			change(m)
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "message": m, "finish_reason": "tool_calls"}}})
			n := 0
			body := &trackedBody{Reader: strings.NewReader(string(b))}
			c := client(t, func(*http.Request) (*http.Response, error) { n++; return response(body, 200, "application/json"), nil })
			got, e := c.Complete(context.Background(), r)
			safeError(t, got, e, cc.ErrProtocol)
			if n != 1 || !body.closed {
				t.Fatal("retry or unclosed body")
			}
		})
	}
	for _, finish := range []string{"stop", "tool_calls"} {
		t.Run("finish-mismatch-"+finish, func(t *testing.T) {
			r := request()
			r.Tools = []cc.ToolDefinition{tool()}
			calls := "[]"
			if finish == "stop" {
				b, _ := json.Marshal([]cc.ToolCall{call("a")})
				calls = string(b)
			}
			body := `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer","tool_calls":` + calls + `},"finish_reason":"` + finish + `"}]}`
			c := client(t, func(*http.Request) (*http.Response, error) {
				return response(&trackedBody{Reader: strings.NewReader(body)}, 200, "application/json"), nil
			})
			got, e := c.Complete(context.Background(), r)
			safeError(t, got, e, cc.ErrProtocol)
		})
	}
	t.Run("null reasoning decoded absent", func(t *testing.T) {
		c := client(t, func(*http.Request) (*http.Response, error) {
			return response(&trackedBody{Reader: strings.NewReader(strings.Replace(answer, `"content":`, `"reasoning_content":null,"content":`, 1))}, 200, "application/json"), nil
		})
		got, e := c.Complete(context.Background(), request())
		if e != nil || got.Message.ReasoningContent != nil {
			t.Fatalf("null reasoning: %#v %v", got, e)
		}
	})
}

func TestExactResourceBoundaries(t *testing.T) {
	t.Run("name and call ID lengths", func(t *testing.T) {
		r := request()
		v := tool()
		v.Name = strings.Repeat("x", 64)
		r.Tools = []cc.ToolDefinition{v}
		previous := call(strings.Repeat("a", 128))
		previous.Function.Name = v.Name
		r.Messages = append(r.Messages, cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{previous}}, cc.Message{Role: cc.RoleTool, ToolCallID: previous.ID, Content: ptr("")})
		next := previous
		next.ID = strings.Repeat("b", 128)
		raw, _ := json.Marshal(cc.Message{Role: cc.RoleAssistant, ToolCalls: []cc.ToolCall{next}})
		body := `{"choices":[{"index":0,"message":` + string(raw) + `,"finish_reason":"tool_calls"}]}`
		n := 0
		c := client(t, func(*http.Request) (*http.Response, error) {
			n++
			return response(&trackedBody{Reader: strings.NewReader(body)}, 200, "application/json"), nil
		})
		got, e := c.Complete(context.Background(), r)
		if e != nil || n != 1 || !reflect.DeepEqual(got.Message.ToolCalls, []cc.ToolCall{next}) {
			t.Fatalf("exact name/ID boundaries: %#v %v attempts %d", got, e, n)
		}
	})
	t.Run("counts", func(t *testing.T) {
		r := request()
		r.MaxOutputTokens = 1
		for len(r.Messages) < 128 {
			r.Messages = append(r.Messages, cc.Message{Role: cc.RoleUser, Content: ptr("")})
		}
		for i := 0; i < 32; i++ {
			v := tool()
			v.Name = fmt.Sprintf("t%d", i)
			r.Tools = append(r.Tools, v)
		}
		n := 0
		c := client(t, func(*http.Request) (*http.Response, error) {
			n++
			return response(&trackedBody{Reader: strings.NewReader(answer)}, 200, "application/json"), nil
		})
		if _, e := c.Complete(context.Background(), r); e != nil || n != 1 {
			t.Fatalf("exact counts: %v %d", e, n)
		}
	})
	t.Run("response bytes", func(t *testing.T) {
		body := &trackedBody{Reader: strings.NewReader(answer + strings.Repeat(" ", 512*1024-len(answer)))}
		c := client(t, func(*http.Request) (*http.Response, error) { return response(body, 200, "application/json"), nil })
		if _, e := c.Complete(context.Background(), request()); e != nil || body.n != 512*1024 || !body.closed {
			t.Fatalf("exact response: %v bytes %d closed %v", e, body.n, body.closed)
		}
	})
	t.Run("encoded request bytes", func(t *testing.T) {
		r := request()
		size := 0
		n := 0
		c := client(t, func(req *http.Request) (*http.Response, error) {
			n++
			data, _ := io.ReadAll(req.Body)
			size = len(data)
			return response(&trackedBody{Reader: strings.NewReader(answer)}, 200, "application/json"), nil
		})
		if _, e := c.Complete(context.Background(), r); e != nil {
			t.Fatal(e)
		}
		r.Messages[0].Content = ptr(strings.Repeat("x", (1<<20)-size+len("private prompt")))
		if _, e := c.Complete(context.Background(), r); e != nil || size != 1<<20 || n != 2 {
			t.Fatalf("exact request: %v bytes %d attempts %d", e, size, n)
		}
		*r.Messages[0].Content += "x"
		got, e := c.Complete(context.Background(), r)
		safeError(t, got, e, cc.ErrInput)
		if n != 2 {
			t.Fatal("over-limit sent")
		}
	})
}

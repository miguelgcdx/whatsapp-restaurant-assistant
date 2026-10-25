package commandcode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxRequestBytes  = 1 << 20
	maxResponseBytes = 512 << 10
	requestTimeout   = 10 * time.Second
)

type Client struct {
	key  string
	http *http.Client
}

// New validates the explicit key before allocating transport resources. It does
// not consult environment variables or mutate process-global HTTP defaults.
func New(config Config) (*Client, error) {
	key := strings.TrimSpace(config.APIKey)
	if key == "" || len(key) > 4096 || !utf8.ValidString(key) {
		return nil, ErrInput
	}
	for _, r := range key {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) || unicode.IsControl(r) {
			return nil, ErrInput
		}
	}
	transport := config.Transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  5 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			DisableKeepAlives:      true,
		}
	}
	return &Client{key: key, http: &http.Client{
		Transport:     closeErrorTransport{transport},
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// closeErrorTransport also closes a body returned alongside a transport error;
// net/http otherwise ignores that response. It never retries or rewrites errors.
type closeErrorTransport struct{ next http.RoundTripper }

func (t closeErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.next.RoundTrip(r)
	if err != nil && res != nil {
		if res.Body != nil {
			_ = res.Body.Close()
		}
		return nil, err
	}
	return res, err
}

type wireTool struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type wireRequest struct {
	Model      string     `json:"model"`
	Messages   []Message  `json:"messages"`
	Tools      []wireTool `json:"tools,omitempty"`
	ToolChoice string     `json:"tool_choice,omitempty"`
	Stream     bool       `json:"stream"`
	MaxTokens  int        `json:"max_tokens"`
}

// Complete performs at most one HTTP attempt. It neither executes tools nor
// owns conversation state. A cancellation does not prove remote work stopped.
// Callers must not mutate Request's referenced data concurrently with this call.
func (c *Client) Complete(ctx context.Context, input Request) (Result, error) {
	if ctx == nil || c == nil || c.http == nil {
		return Result{}, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	names, ok := validateRequest(input)
	if !ok {
		return Result{}, ErrInput
	}
	payload := wireRequest{Model: Model, Messages: input.Messages, Stream: false, MaxTokens: input.MaxOutputTokens}
	for _, tool := range input.Tools {
		payload.Tools = append(payload.Tools, wireTool{Type: "function", Function: tool})
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxRequestBytes {
		return Result{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, ErrInput
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return Result{}, contextError(ctx, err)
	}
	defer res.Body.Close()
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Result{}, &HTTPError{StatusCode: res.StatusCode}
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return Result{}, ErrProtocol
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, contextError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(data) > maxResponseBytes {
		return Result{}, ErrResource
	}
	return decodeResponse(data, names, input.Messages)
}

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func validName(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validJSON rejects duplicate keys before typed decoding (encoding/json normally
// accepts last-key-wins). Unknown extra metadata remains allowed. Object checks
// are structural, not a full JSON Schema implementation.
func validJSON(data []byte, object bool) bool {
	if !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || object && first != json.Delim('{') {
		return false
	}
	if !jsonValue(decoder, first) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}
func jsonValue(d *json.Decoder, token json.Token) bool {
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		if s, ok := token.(string); ok {
			return validText(s)
		}
		return true
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return false
			}
			s, ok := key.(string)
			if !ok || seen[s] || !validText(s) {
				return false
			}
			seen[s] = true
			next, e := d.Token()
			if e != nil || !jsonValue(d, next) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim('}')
	case '[':
		for d.More() {
			next, e := d.Token()
			if e != nil || !jsonValue(d, next) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim(']')
	default:
		return false
	}
}

func validParameters(data json.RawMessage) bool {
	if !validJSON(data, true) {
		return false
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(data, &schema) != nil {
		return false
	}
	var kind string
	if json.Unmarshal(schema["type"], &kind) != nil || kind != "object" {
		return false
	}
	if properties, exists := schema["properties"]; exists {
		var props map[string]json.RawMessage
		if json.Unmarshal(properties, &props) != nil || props == nil {
			return false
		}
		for _, v := range props {
			if !validJSON(v, true) {
				return false
			}
		}
	}
	if required, exists := schema["required"]; exists {
		var names []string
		if json.Unmarshal(required, &names) != nil || names == nil {
			return false
		}
		seen := map[string]bool{}
		for _, name := range names {
			if name == "" || seen[name] {
				return false
			}
			seen[name] = true
		}
	}
	return true
}

func validateCall(call ToolCall, names map[string]bool, seen map[string]bool) bool {
	if call.Type != "function" || !validName(call.ID, 128) || seen[call.ID] || !names[call.Function.Name] || !validName(call.Function.Name, 64) || !validJSON([]byte(call.Function.Arguments), true) {
		return false
	}
	seen[call.ID] = true
	return true
}

func validateRequest(input Request) (map[string]bool, bool) {
	if len(input.Messages) == 0 || len(input.Messages) > 128 || len(input.Tools) > 32 || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 8192 {
		return nil, false
	}
	// Bound raw input before marshaling or parsing nested JSON. Escaping may make
	// the eventual wire size larger; the encoded payload is checked separately.
	size := 0
	for _, tool := range input.Tools {
		size += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
		if size > maxRequestBytes {
			return nil, false
		}
	}
	for _, m := range input.Messages {
		size += len(m.Role) + len(m.ToolCallID)
		if m.Content != nil {
			size += len(*m.Content)
		}
		if m.ReasoningContent != nil {
			size += len(*m.ReasoningContent)
		}
		for _, call := range m.ToolCalls {
			size += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
			if size > maxRequestBytes {
				return nil, false
			}
		}
		if size > maxRequestBytes {
			return nil, false
		}
	}
	names := make(map[string]bool)
	for _, tool := range input.Tools {
		if !validName(tool.Name, 64) || names[tool.Name] || !validText(tool.Description) || !validParameters(tool.Parameters) {
			return nil, false
		}
		names[tool.Name] = true
	}
	calls := make(map[string]bool)
	consumed := make(map[string]bool)
	for _, m := range input.Messages {
		if !validMessage(m, names, calls, consumed) {
			return nil, false
		}
	}
	return names, true
}

func validMessage(m Message, names, calls, consumed map[string]bool) bool {
	if m.Content != nil && !validText(*m.Content) || m.ReasoningContent != nil && !validText(*m.ReasoningContent) {
		return false
	}
	switch m.Role {
	case RoleSystem, RoleUser:
		return m.Content != nil && m.ReasoningContent == nil && len(m.ToolCalls) == 0 && m.ToolCallID == ""
	case RoleTool:
		if m.Content == nil || m.ReasoningContent != nil || len(m.ToolCalls) != 0 || !calls[m.ToolCallID] || consumed[m.ToolCallID] {
			return false
		}
		consumed[m.ToolCallID] = true
		return true
	case RoleAssistant:
		if m.ToolCallID != "" || len(m.ToolCalls) == 0 && (m.Content == nil || *m.Content == "") {
			return false
		}
		for _, call := range m.ToolCalls {
			if !validateCall(call, names, calls) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// canonicalFields prevents encoding/json's case-insensitive struct matching from
// accepting aliases of critical fields or disguising duplicate authority fields.
// Unrelated metadata keys are still allowed; argument/schema keys are not folded.
func canonicalFields(data []byte, known ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, false
	}
	for key := range fields {
		for _, name := range known {
			if key != name && strings.EqualFold(key, name) {
				return nil, false
			}
		}
	}
	return fields, true
}

func canonicalMessage(data []byte) bool {
	fields, ok := canonicalFields(data, "role", "content", "reasoning_content", "tool_calls", "tool_call_id", "refusal")
	if !ok {
		return false
	}
	if _, exists := fields["tool_call_id"]; exists {
		return false
	}
	if raw, exists := fields["tool_calls"]; exists {
		var calls []json.RawMessage
		if json.Unmarshal(raw, &calls) != nil {
			return false
		}
		for _, rawCall := range calls {
			call, ok := canonicalFields(rawCall, "id", "type", "function")
			if !ok {
				return false
			}
			if _, ok = canonicalFields(call["function"], "name", "arguments"); !ok {
				return false
			}
		}
	}
	return true
}

func decodeResponse(data []byte, names map[string]bool, history []Message) (Result, error) {
	if !validJSON(data, true) {
		return Result{}, ErrProtocol
	}
	fields, ok := canonicalFields(data, "choices")
	if !ok {
		return Result{}, ErrProtocol
	}
	var choices []json.RawMessage
	if json.Unmarshal(fields["choices"], &choices) != nil || len(choices) != 1 {
		return Result{}, ErrProtocol
	}
	if _, ok = canonicalFields(choices[0], "index", "message", "finish_reason"); !ok {
		return Result{}, ErrProtocol
	}
	var envelope struct {
		Choices []struct {
			Index        *int            `json:"index"`
			Message      json.RawMessage `json:"message"`
			FinishReason string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Choices) != 1 {
		return Result{}, ErrProtocol
	}
	choice := envelope.Choices[0]
	if choice.Index == nil || *choice.Index != 0 || !validJSON(choice.Message, true) || !canonicalMessage(choice.Message) {
		return Result{}, ErrProtocol
	}
	var message Message
	var refusal struct {
		Refusal *string `json:"refusal"`
	}
	if json.Unmarshal(choice.Message, &message) != nil || json.Unmarshal(choice.Message, &refusal) != nil || message.Role != RoleAssistant {
		return Result{}, ErrProtocol
	}
	if refusal.Refusal != nil || choice.FinishReason == "content_filter" || choice.FinishReason == "refusal" {
		return Result{}, ErrRefusal
	}
	if choice.FinishReason == "length" {
		return Result{}, ErrIncomplete
	}
	calls := make(map[string]bool)
	for _, m := range history {
		for _, call := range m.ToolCalls {
			calls[call.ID] = true
		}
	}
	if !validMessage(message, names, calls, make(map[string]bool)) {
		return Result{}, ErrProtocol
	}
	switch choice.FinishReason {
	case "stop":
		if message.Content == nil || *message.Content == "" || len(message.ToolCalls) != 0 {
			return Result{}, ErrProtocol
		}
	case "tool_calls":
		if len(message.ToolCalls) == 0 {
			return Result{}, ErrProtocol
		}
	default:
		return Result{}, ErrProtocol
	}
	return Result{Message: message, FinishReason: choice.FinishReason}, nil
}

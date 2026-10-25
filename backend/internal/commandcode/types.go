// Package commandcode provides a bounded, non-streaming, one-turn CommandCode
// boundary. Tool calls are untrusted data, never executable capabilities.
package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const (
	Endpoint = "https://api.commandcode.ai/provider/v1/chat/completions"
	Model    = "deepseek/deepseek-v4.1-flash"
)

// Config is trusted application configuration, not model- or customer-supplied
// input. Transport may inspect credentials and must enforce its own I/O behavior
// and honor request cancellation. Nil selects a private, bounded HTTP transport.
type Config struct {
	APIKey    string
	Transport http.RoundTripper
}

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message preserves ordered history. Nil content encodes as null; a pointer to
// "" encodes as an empty string. Nil reasoning (absent or JSON null on input)
// is omitted on replay, never backfilled. Non-nil reasoning is opaque sensitive
// history and must not be logged or displayed as a fallback answer.
type Message struct {
	Role             Role       `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type FunctionCall struct {
	Name string `json:"name"`
	// Arguments is a JSON object string; validation never re-encodes its bytes.
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // Must be "function".
	Function FunctionCall `json:"function"`
}

// ToolDefinition is a server-owned declaration, not a full JSON Schema contract.
// Parameters must describe an object. No strict/structured-output promise is made.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Messages        []Message
	Tools           []ToolDefinition
	MaxOutputTokens int
}

type Result struct {
	Message      Message
	FinishReason string
}

var (
	ErrInput       = errors.New("commandcode: invalid input")
	ErrProtocol    = errors.New("commandcode: invalid response")
	ErrResource    = errors.New("commandcode: response budget exceeded")
	ErrAccess      = errors.New("commandcode: access denied")
	ErrRateLimit   = errors.New("commandcode: rate limited")
	ErrIncomplete  = errors.New("commandcode: incomplete response")
	ErrRefusal     = errors.New("commandcode: response refused")
	ErrUnavailable = errors.New("commandcode: unavailable or unknown outcome")
	ErrHTTP        = errors.New("commandcode: HTTP failure")
)

// HTTPError exposes only numeric status, never the provider body or headers.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("commandcode: HTTP status %d", e.StatusCode) }
func (e *HTTPError) Unwrap() error {
	switch {
	case e.StatusCode == 401 || e.StatusCode == 403:
		return ErrAccess
	case e.StatusCode == 429:
		return ErrRateLimit
	case e.StatusCode >= 500 && e.StatusCode <= 599:
		return ErrUnavailable
	default:
		return ErrHTTP
	}
}

// contextError preserves only trusted cancellation identities, never arbitrary
// transport/body error strings (including url.Error's URL and nested cause).
func contextError(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Preserve only the known identities, not the potentially sensitive wrapper.
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrUnavailable
}

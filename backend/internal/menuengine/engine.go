// Package menuengine provides a bounded, in-memory menu proposal turn. Its caller
// must establish current read/control authority before invocation; this library
// is not an authorization gate, inbound processor, or order execution path.
package menuengine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
)

var (
	ErrInput           = errors.New("invalid menu turn input")
	ErrProtocol        = errors.New("invalid menu turn response")
	ErrMenuUnavailable = errors.New("menu temporarily unavailable")
	ErrResource        = errors.New("menu turn exceeds local budget")
)

// MenuReader must be server-bound (for example, catalog.Store.Snapshot), honor
// cancellation, and never derive restaurant scope from model/customer parameters.
type MenuReader func(context.Context) (catalog.MenuSnapshot, error)

type Config struct {
	Client          *commandcode.Client
	ReadMenu        MenuReader
	MaxOutputTokens int
}

// Engine owns immutable configuration only. Dependencies are trusted and must
// support concurrent invocation if the caller runs turns concurrently.
type Engine struct{ config Config }

type MenuStatus struct{ Open bool }

// Result contains a model proposal, not permission or business confirmation.
// Menu=nil means no catalog read grounded this proposal. Non-nil Menu carries
// authoritative stored status, including an explicit Open=false when closed.
type Result struct {
	Reply string
	Menu  *MenuStatus
}

func validConfig(c Config) bool {
	return c.Client != nil && c.ReadMenu != nil && c.MaxOutputTokens >= 1 && c.MaxOutputTokens <= 8192
}

// New requires explicit trusted dependencies; it never looks up keys or creates
// a default client, authorization mechanism, or restaurant selector.
func New(c Config) (*Engine, error) {
	if !validConfig(c) {
		return nil, ErrInput
	}
	return &Engine{config: c}, nil
}

const turnTimeout = 20 * time.Second
const toolResultBudget = 256 << 10
const systemMessage = "You propose replies about a restaurant menu. Use get_menu for stored menu facts. Preserve its open status and item availability: a closed menu is browsable but does not authorize an order or reopen the restaurant. Do not invent prices, fees, estimates, order confirmations, or permissions."

// Run starts fresh history and performs at most two model calls and one read.
// The overall deadline is cooperative: trusted dependencies must honor context;
// no detached watchdog goroutine attempts to contain a noncooperative callback.
func (e *Engine) Run(ctx context.Context, text string) (Result, error) {
	if ctx == nil || e == nil || !validConfig(e.config) || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return Result{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	system := systemMessage
	request := commandcode.Request{
		Messages: []commandcode.Message{
			{Role: commandcode.RoleSystem, Content: &system},
			{Role: commandcode.RoleUser, Content: &text},
		},
		Tools: []commandcode.ToolDefinition{{
			Name:        "get_menu",
			Description: "Read the complete stored menu, including open status and unavailable items.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
		MaxOutputTokens: e.config.MaxOutputTokens,
	}
	first, err := e.config.Client.Complete(ctx, request)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		// The concrete client already sanitizes errors.
		return Result{}, err
	}
	if len(first.Message.ToolCalls) == 0 {
		return directReply(first.Message)
	}
	calls := first.Message.ToolCalls
	if len(calls) != 1 || calls[0].Type != "function" || calls[0].ID == "" || calls[0].Function.Name != "get_menu" || !emptyObject(calls[0].Function.Arguments) {
		return Result{}, ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	snapshot, err := e.config.ReadMenu(ctx)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{}, menuError(err)
	}
	if err := snapshot.Validate(); err != nil {
		return Result{}, menuError(err)
	}
	payload, err := encodeMenu(snapshot)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Replay the original message without normalizing reasoning, nullable content,
	// tool IDs or raw argument strings. No caller/model-provided history is added.
	request.Messages = append(request.Messages, first.Message, commandcode.Message{Role: commandcode.RoleTool, ToolCallID: calls[0].ID, Content: &payload})
	last, err := e.config.Client.Complete(ctx, request)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{}, err
	}
	if len(last.Message.ToolCalls) != 0 {
		return Result{}, ErrProtocol
	}
	out, err := directReply(last.Message)
	if err != nil {
		return Result{}, err
	}
	out.Menu = &MenuStatus{Open: snapshot.Settings.Open}
	return out, nil
}

func directReply(message commandcode.Message) (Result, error) {
	if message.Content == nil || *message.Content == "" {
		return Result{}, ErrProtocol
	}
	return Result{Reply: *message.Content}, nil
}

func emptyObject(raw string) bool {
	// The concrete client rejects malformed/duplicate-key JSON first. This check
	// independently enforces this operation's zero-parameter contract, including
	// rejection of null, arrays, trailing data and every nonempty object.
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(raw), &object) == nil && object != nil && len(object) == 0
}

func menuError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, catalog.ErrSnapshotLimit):
		return ErrResource
	default:
		return ErrMenuUnavailable
	}
}

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

func encodeMenu(snapshot catalog.MenuSnapshot) (string, error) {
	wire := menuWire{Open: snapshot.Settings.Open, Items: make([]itemWire, 0, len(snapshot.Items))}
	for _, item := range snapshot.Items {
		wire.Items = append(wire.Items, itemWire{
			ID:        item.ID,
			Name:      item.Name,
			Available: item.Available,
			Price: priceWire{
				Amount:   strconv.FormatInt(item.PriceMinor, 10),
				Currency: item.Currency,
				Exponent: snapshot.Settings.CurrencyExponent,
			},
		})
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return "", ErrMenuUnavailable
	}
	if len(data) > toolResultBudget {
		return "", ErrResource
	}
	return string(data), nil
}

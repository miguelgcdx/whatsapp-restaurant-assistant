// Package carteditflow is a reusable trusted bridge, not a mounted ordering loop.
package carteditflow

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"time"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/editintent"
	"restaurant-assistant/backend/internal/menugate"
)

var (
	ErrInput = errors.New("invalid cart edit flow input")
	ErrBusy  = errors.New("native cart edit is currently leased")
)

type Config struct {
	Store       *cart.Store
	Interpreter editintent.Interpreter
	NativeLease time.Duration
	ReplyGate   ClarificationGate
}

// ClarificationGate is the existing source-bound durable gate, not a sender.
// Injection permits testing queue failure after the separate marker commit.
type ClarificationGate interface {
	QueueClarification(context.Context, cart.MenuClaim, int64) (menugate.Receipt, error)
	ReadQueued(context.Context, cart.Source) (menugate.Receipt, error)
}
type Host struct{ config Config }

// Turn contains only host-owned scope, actual stored claim, cart/version and
// runtime slot. The host reads inbound text from Claim.Job().Event, not prose
// returned by the model. Backend methods reload actual authority.
type Turn struct {
	Local   cart.LocalContext
	Claim   cart.MenuClaim
	Cart    cart.CartID
	Version cart.Version
	Slot    int64
}

// Outcome preserves a known committed marker even if the subsequent queue call
// fails. Other errors return zero. Receipt means queued, never delivered/ACKed.
// Use ReplyGate.ReadQueued with the actual source after an uncertain queue commit;
// do not replay model interpretation. Admission is not application/resolution.
type Outcome struct {
	marked  cart.PendingEditRef
	receipt menugate.Receipt
	native  cart.NativeActionRef
}

func (o Outcome) Marked() cart.PendingEditRef  { return o.marked }
func (o Outcome) Receipt() menugate.Receipt    { return o.receipt }
func (o Outcome) Question() string             { return o.receipt.Content }
func (o Outcome) Native() cart.NativeActionRef { return o.native }

func New(c Config) (*Host, error) {
	if !validConfig(c) {
		return nil, ErrInput
	}
	return &Host{config: c}, nil
}

// Admit reads the actual cart and complete PendingEditView for EACH model
// invocation, retain that exact view and private ordered p1..pN -> Ref binding,
// then interpret outside transactions. No later view/index/kind may select a ref.
// Concrete uses AdmitNativeEdit; ambiguity uses MarkUnresolvedEdit; resolution
// uses AdmitResolvedNativeEdit with the retained view/ref and strict concrete
// proposal. no_edit and pre-mark rejection paths allocate nothing. Mark commits
// first; QueueClarification derives its question from that actual source/slot
// marker. A later queue error cannot undo the marker or be called zero-effect.
func (h *Host) Admit(parent context.Context, turn Turn) (Outcome, error) {
	if parent == nil || h == nil || !validConfig(h.config) || turn.Slot < 0 {
		return Outcome{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	snapshot, err := h.config.Store.Read(ctx, turn.Local, turn.Cart)
	if err != nil {
		return Outcome{}, err
	}
	if snapshot.Version != turn.Version {
		return Outcome{}, cart.ErrStale
	}
	view, err := h.config.Store.ReadPendingEdits(ctx, turn.Local, turn.Claim, turn.Cart, turn.Version)
	if err != nil {
		return Outcome{}, err
	}
	entries := view.Entries()
	input := editintent.Input{InboundText: turn.Claim.Job().Event.Text, Cart: snapshot, Pending: make([]editintent.Pending, 0, len(entries))}
	// This local map and exact view survive only this invocation. Never reread to
	// interpret a selector after model I/O, even if the active set changed.
	refs := make(map[string]cart.PendingEditRef, len(entries))
	for n, entry := range entries {
		selector := "p" + strconv.Itoa(n+1)
		refs[selector] = entry.Ref
		input.Pending = append(input.Pending, editintent.Pending{Selector: selector, Kind: entry.Kind, Request: entry.Request})
	}
	proposal, err := h.config.Interpreter.Interpret(ctx, input)
	if err != nil {
		return Outcome{}, err
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	switch proposal.Variant() {
	case editintent.NoEdit:
		return Outcome{}, nil
	case editintent.UnresolvedTarget:
		marker, err := h.config.Store.MarkUnresolvedEdit(ctx, turn.Local, turn.Claim, turn.Slot, turn.Cart, turn.Version, proposal.Kind())
		if err != nil {
			return Outcome{}, err
		}
		// Known mark commit cannot be discarded on a later queue failure/cancellation.
		out := Outcome{marked: marker}
		receipt, err := h.config.ReplyGate.QueueClarification(ctx, turn.Claim, turn.Slot)
		if err != nil {
			return out, err
		}
		out.receipt = receipt
		return out, nil
	case editintent.Concrete:
		ref, err := h.config.Store.AdmitNativeEdit(ctx, turn.Local, turn.Claim, turn.Slot, proposal.Call())
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{native: ref}, nil
	case editintent.ResolveTarget:
		ref, ok := refs[proposal.Selector()]
		if !ok {
			return Outcome{}, editintent.ErrInput
		}
		native, err := h.config.Store.AdmitResolvedNativeEdit(ctx, turn.Local, turn.Claim, turn.Slot, view, ref, proposal.Call())
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{native: native}, nil
	default:
		return Outcome{}, editintent.ErrInput
	}
}

// Execute uses existing RecoverNativeEdit/ExecuteNativeEdit business leases.
// Only successful native application clears one linked marker atomically.
func (h *Host) Execute(ctx context.Context, ref cart.NativeActionRef) (cart.Result, error) {
	if ctx == nil || h == nil || !validConfig(h.config) {
		return cart.Result{}, ErrInput
	}
	work, ok, err := h.config.Store.RecoverNativeEdit(ctx, ref, h.config.NativeLease)
	if err != nil {
		return cart.Result{}, err
	}
	if !ok {
		return cart.Result{}, ErrBusy
	}
	return h.config.Store.ExecuteNativeEdit(ctx, work)
}

func validConfig(c Config) bool {
	return c.Store != nil && !nilPort(c.Interpreter) && !nilPort(c.ReplyGate) && c.NativeLease >= time.Millisecond && c.NativeLease <= time.Minute
}
func nilPort(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// Package editintent proposes cart edits without business authority.
package editintent

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
)

var ErrInput = errors.New("invalid edit interpretation input")

// Variant is semantic data, not permission to mutate a cart.
type Variant string

const (
	Concrete         Variant = "concrete"
	UnresolvedTarget Variant = "unresolved_target"
	ResolveTarget    Variant = "resolve_target"
	NoEdit           Variant = "no_edit"
)

// Proposal is constructed only by Decode. Its zero value is invalid. Private
// payloads prevent callers from constructing contradictory variant fields.
type Proposal struct {
	variant  Variant
	kind     cart.UnresolvedEditKind
	selector string
	call     cart.Call
}

func (p Proposal) Variant() Variant              { return p.variant }
func (p Proposal) Kind() cart.UnresolvedEditKind { return p.kind }
func (p Proposal) Selector() string              { return p.selector }
func (p Proposal) Call() cart.Call               { return p.call }

// Decode strictly parses distinct carriers or delegates unchanged concrete JSON
// to cartintent.Decode. A concrete version is only a checked echo of host facts.
// Every rejection returns zero; no parsing result supplies business authority.
func Decode(arguments string, trustedCart cart.CartID, trustedVersion cart.Version) (Proposal, error) {
	if _, err := cart.ParseCartID(trustedCart.String()); err != nil || trustedVersion.Int64() < 0 {
		return Proposal{}, ErrInput
	}
	fields, ok := object(arguments)
	if !ok {
		return Proposal{}, ErrInput
	}
	raw, carrier := fields["proposal"]
	if !carrier {
		call, err := concrete(arguments, trustedCart, trustedVersion)
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{variant: Concrete, call: call}, nil
	}
	name, ok := stringValue(raw)
	if !ok {
		return Proposal{}, ErrInput
	}
	switch Variant(name) {
	case NoEdit:
		if len(fields) != 1 {
			return Proposal{}, ErrInput
		}
		return Proposal{variant: NoEdit}, nil
	case UnresolvedTarget:
		kind, ok := stringValue(fields["kind"])
		if !ok || len(fields) != 2 {
			return Proposal{}, ErrInput
		}
		var backend cart.UnresolvedEditKind
		switch kind {
		case "Add":
			backend = cart.UnresolvedAdd
		case "Remove":
			backend = cart.UnresolvedRemove
		case "SetNote":
			backend = cart.UnresolvedSetNote
		default:
			return Proposal{}, ErrInput
		}
		return Proposal{variant: UnresolvedTarget, kind: backend}, nil
	case ResolveTarget:
		selector, ok := stringValue(fields["pending_selector"])
		if !ok || strings.TrimSpace(selector) == "" || len(selector) > 64 || len(fields) != 3 {
			return Proposal{}, ErrInput
		}
		call, err := concrete(string(fields["edit"]), trustedCart, trustedVersion)
		if err != nil {
			return Proposal{}, err
		}
		return Proposal{variant: ResolveTarget, selector: selector, call: call}, nil
	default:
		return Proposal{}, ErrInput
	}
}

func concrete(raw string, id cart.CartID, v cart.Version) (cart.Call, error) {
	// Existing decoder owns all concrete fields, target and Unicode validation.
	call, err := cartintent.Decode(raw, id)
	if err != nil {
		return cart.Call{}, ErrInput
	}
	fields, ok := object(raw)
	echo, valid := stringValue(fields["expected_version"])
	if !ok || !valid || echo != strconv.FormatInt(v.Int64(), 10) {
		return cart.Call{}, ErrInput
	}
	return call, nil
}

// object retains exact decoded keys and raw values; encoding/json struct
// matching would otherwise accept case aliases and overwrite duplicate keys.
func object(raw string) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > 512*1024 || !utf8.ValidString(raw) || !json.Valid([]byte(raw)) || !pairedEscapes(raw) {
		return nil, false
	}
	d := json.NewDecoder(strings.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	_, err = d.Token()
	return fields, err == nil
}
func stringValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

// json.Valid accepts lone UTF-16 surrogates which Unmarshal silently repairs.
// After JSON validation every escape is in a string and has sufficient digits.
func pairedEscapes(raw string) bool {
	for n := 0; n < len(raw); n++ {
		if raw[n] != '\\' {
			continue
		}
		n++
		if raw[n] != 'u' {
			continue
		}
		u, _ := strconv.ParseUint(raw[n+1:n+5], 16, 16)
		n += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return false
		}
		if u < 0xd800 || u > 0xdbff {
			continue
		}
		if n+6 >= len(raw) || raw[n+1:n+3] != `\u` {
			return false
		}
		low, err := strconv.ParseUint(raw[n+3:n+7], 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		n += 6
	}
	return true
}

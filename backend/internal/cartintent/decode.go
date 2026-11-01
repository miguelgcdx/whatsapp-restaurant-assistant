// Package cartintent decodes untrusted model arguments into cart proposals.
// It neither authorizes nor executes the proposed operation.
package cartintent

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
)

// ErrInput is the sole rejection result; it never contains model arguments.
var ErrInput = errors.New("invalid cart intent input")

// Decode returns one constructor-built edit proposal for a server-chosen cart.
// The inclusive byte budget is local to this boundary, not an execution limit.
func Decode(arguments string, trustedCart cart.CartID) (cart.Call, error) {
	if len(arguments) > 512*1024 || !utf8.ValidString(arguments) || !json.Valid([]byte(arguments)) || !pairedEscapes(arguments) {
		return cart.Call{}, ErrInput
	}
	fields, ok := object(arguments)
	if !ok {
		return cart.Call{}, ErrInput
	}
	versionText, vok := stringField(fields["expected_version"])
	actionName, aok := stringField(fields["action"])
	var version cart.Version
	if !vok || !aok || version.UnmarshalText([]byte(versionText)) != nil {
		return cart.Call{}, ErrInput
	}

	var action cart.Action
	var err error
	switch actionName {
	case "add":
		menuText, mok := stringField(fields["menu_id"])
		menu, merr := cart.ParseMenuID(menuText)
		// ParseInt accepts neither fractions nor exponents; json.Valid already
		// excludes leading plus, leading zeros, and non-JSON numeric syntax.
		count, cerr := strconv.ParseInt(string(fields["count"]), 10, 64)
		if len(fields) != 4 || !mok || merr != nil || cerr != nil {
			return cart.Call{}, ErrInput
		}
		action, err = cart.Add(menu, count)
	case "remove", "set_note":
		ids, iok := unitIDs(fields["unit_ids"])
		if !iok {
			return cart.Call{}, ErrInput
		}
		if actionName == "remove" {
			if len(fields) != 3 {
				return cart.Call{}, ErrInput
			}
			action, err = cart.Remove(ids)
		} else {
			note, nok := stringField(fields["note"])
			if len(fields) != 4 || !nok {
				return cart.Call{}, ErrInput
			}
			action, err = cart.SetNote(ids, note)
		}
	default:
		return cart.Call{}, ErrInput
	}
	if err != nil {
		return cart.Call{}, ErrInput
	}
	call, err := cart.EditCall(trustedCart, version, action)
	if err != nil {
		return cart.Call{}, ErrInput
	}
	return call, nil
}

// object retains exact decoded key spelling and rejects duplicate decoded keys.
// Raw values avoid encoding/json's case-insensitive struct field matching and
// numeric float conversion. JSON validity, including trailing data, is checked
// before this helper is called.
func object(input string) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(strings.NewReader(input))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
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

func stringField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

func unitIDs(raw json.RawMessage) ([]cart.UnitID, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	ids := make([]cart.UnitID, len(values))
	for i, rawID := range values {
		text, ok := stringField(rawID)
		id, err := cart.ParseUnitID(text)
		if !ok || err != nil {
			return nil, false
		}
		ids[i] = id
	}
	return ids, true
}

// pairedEscapes supplements encoding/json, which otherwise repairs unpaired
// surrogate escapes to U+FFFD. It runs only on syntactically valid JSON, so each
// backslash is inside a string and any Unicode escape has four hex digits.
// Escaped backslashes are skipped, not mistaken for Unicode escapes. Literal
// U+FFFD and the non-surrogate escape \uFFFD are deliberately accepted.
func pairedEscapes(input string) bool {
	for i := 0; i < len(input); i++ {
		if input[i] != '\\' {
			continue
		}
		i++
		if input[i] != 'u' {
			continue
		}
		u, _ := strconv.ParseUint(input[i+1:i+5], 16, 16)
		i += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return false
		}
		if u < 0xd800 || u > 0xdbff {
			continue
		}
		if i+6 >= len(input) || input[i+1:i+3] != `\u` {
			return false
		}
		low, err := strconv.ParseUint(input[i+3:i+7], 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

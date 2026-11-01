package cartintent_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
)

const (
	menu   = "10000000-0000-4000-8000-000000000001"
	unit   = "10000000-0000-4000-8000-000000000002"
	other  = "10000000-0000-1000-8000-000000000003"
	add    = `{"expected_version":"0","action":"add","menu_id":"` + menu + `","count":2}`
	remove = `{"expected_version":"0","action":"remove","unit_ids":["` + unit + `"]}`
	note   = `{"expected_version":"0","action":"set_note","unit_ids":["` + unit + `"],"note":"one without onions"}`
)

func trusted(t *testing.T) cart.CartID {
	t.Helper()
	id, err := cart.ParseCartID(other)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func expected(t *testing.T, action, version, menuID string, count int64, targets []string, text string) cart.Call {
	t.Helper()
	var v cart.Version
	if err := v.UnmarshalText([]byte(version)); err != nil {
		t.Fatal(err)
	}
	var a cart.Action
	var err error
	if action == "add" {
		id, e := cart.ParseMenuID(menuID)
		if e != nil {
			t.Fatal(e)
		}
		a, err = cart.Add(id, count)
	} else {
		ids := make([]cart.UnitID, len(targets))
		for i, raw := range targets {
			ids[i], err = cart.ParseUnitID(raw)
			if err != nil {
				t.Fatal(err)
			}
		}
		if action == "remove" {
			a, err = cart.Remove(ids)
		} else {
			a, err = cart.SetNote(ids, text)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	call, err := cart.EditCall(trusted(t), v, a)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func quoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDecodeSuccess(t *testing.T) {
	cases := []struct {
		name, input, action, version, menuID string
		count                                int64
		targets                              []string
		text                                 string
	}{
		{"add example", add, "add", "0", menu, 2, nil, ""},
		{"remove example", remove, "remove", "0", "", 0, []string{unit}, ""},
		{"note example", note, "set_note", "0", "", 0, []string{unit}, "one without onions"},
		{"max integers", strings.ReplaceAll(strings.Replace(add, `"0"`, `"9223372036854775807"`, 1), `:2`, `:9223372036854775807`), "add", "9223372036854775807", menu, math.MaxInt64, nil, ""},
		{"non v4 ID and minimum count", strings.Replace(strings.Replace(add, menu, other, 1), `:2`, `:1`, 1), "add", "0", other, 1, nil, ""},
		{"ordered targets", strings.Replace(remove, quoted(unit), quoted(other)+","+quoted(unit), 1), "remove", "0", "", 0, []string{other, unit}, ""},
		{"decoded keys", strings.Replace(add, `"action"`, `"\u0061ction"`, 1), "add", "0", menu, 2, nil, ""},
		{"outer whitespace", " \n\t" + add + "\r ", "add", "0", menu, 2, nil, ""},
		{"inclusive byte budget", add + strings.Repeat(" ", 512*1024-len(add)), "add", "0", menu, 2, nil, ""},
	}
	for _, text := range []string{"", " \t\n  ", "  sin cebolla 🍲\n", strings.Repeat("界", 500), "�", "𝄞", `literal \ud800 and "quotes"`} {
		cases = append(cases, struct {
			name, input, action, version, menuID string
			count                                int64
			targets                              []string
			text                                 string
		}{"note " + strconv.Itoa(len(cases)), strings.Replace(note, quoted("one without onions"), quoted(text), 1), "set_note", "0", "", 0, []string{unit}, text})
	}
	cases = append(cases, struct {
		name, input, action, version, menuID string
		count                                int64
		targets                              []string
		text                                 string
	}{"surrogate pair", strings.Replace(note, quoted("one without onions"), `"\uD834\uDD1E"`, 1), "set_note", "0", "", 0, []string{unit}, "𝄞"})
	cases = append(cases, struct {
		name, input, action, version, menuID string
		count                                int64
		targets                              []string
		text                                 string
	}{"escaped replacement", strings.Replace(note, quoted("one without onions"), `"\uFFFD"`, 1), "set_note", "0", "", 0, []string{unit}, "�"})
	cases = append(cases, struct {
		name, input, action, version, menuID string
		count                                int64
		targets                              []string
		text                                 string
	}{"beyond float precision", strings.Replace(add, `:2`, `:9007199254740993`, 1), "add", "0", menu, 9007199254740993, nil, ""})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cartintent.Decode(tc.input, trusted(t))
			want := expected(t, tc.action, tc.version, tc.menuID, tc.count, tc.targets, tc.text)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Decode mismatch: error=%v, call equal=%v", err, reflect.DeepEqual(got, want))
			}
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"empty": "", "null": "null", "array": "[]", "string": `"text"`, "number": "1", "boolean": "true",
		"malformed": add[:len(add)-1], "trailing object": add + "{}", "trailing scalar": add + "true", "trailing junk": add + "!",
		"over byte budget":      add + strings.Repeat(" ", 512*1024-len(add)+1),
		"duplicate key":         strings.Replace(add, `"action":"add"`, `"action":"add","action":"add"`, 1),
		"escaped duplicate key": strings.Replace(add, `"action":"add"`, `"action":"add","\u0061ction":"add"`, 1),
		"wrong case":            strings.Replace(add, `"action"`, `"Action"`, 1),
		"case extra":            strings.Replace(add, `"action":"add"`, `"action":"add","Action":"add"`, 1),
		"duplicate nested":      strings.Replace(add, `"count":2`, `"count":{"x":1,"x":2}`, 1),
		"empty targets":         strings.Replace(remove, quoted(unit), "", 1),
		"duplicate targets":     strings.Replace(remove, quoted(unit), quoted(unit)+","+quoted(unit), 1),
		"bad target":            strings.Replace(remove, unit, "not-an-id", 1),
		"space target":          strings.Replace(remove, unit, " "+unit, 1),
		"bad menu":              strings.Replace(add, menu, "not-an-id", 1),
		"uppercase menu":        strings.Replace(add, menu, "AAAAAAAA-0000-4000-8000-000000000001", 1),
		"space menu":            strings.Replace(add, menu, menu+" ", 1),
		"NUL note":              strings.Replace(note, quoted("one without onions"), `"\u0000"`, 1),
		"long note":             strings.Replace(note, quoted("one without onions"), quoted(strings.Repeat("界", 501)), 1),
		"raw invalid UTF8":      strings.Replace(note, "one without onions", string([]byte{0xff}), 1),
		"invalid UTF8 key":      strings.Replace(add, "action", "action"+string([]byte{0xff}), 1),
	}
	for _, v := range []string{"+0", "-0", "01", " 0", "0 ", "-1", "9223372036854775808", "1.0", "1e0", ""} {
		cases["version "+quoted(v)] = strings.Replace(add, `"0"`, quoted(v), 1)
	}
	for _, v := range []string{"0", "-0", "-1", "1.0", "1e0", "1E2", "9223372036854775808", `"2"`, "true", "[]", "{}", "null"} {
		cases["count "+v] = strings.Replace(add, `"count":2`, `"count":`+v, 1)
	}
	for _, v := range []string{`"\ud800"`, `"\udfff"`, `"\ud800x"`, `"\ud800\u0041"`, `"\udfff\ud800"`, `"\ud800\ud800"`} {
		cases["surrogate "+v] = strings.Replace(note, quoted("one without onions"), v, 1)
	}
	for _, action := range []string{"create", "read", "batch", "fulfillment", "summary", "submit", "ADD", " add", "add ", ""} {
		cases["action "+quoted(action)] = strings.Replace(add, `"add"`, quoted(action), 1)
	}
	for _, base := range []struct{ name, input string }{{"add", add}, {"remove", remove}, {"note", note}} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(base.input), &fields); err != nil {
			t.Fatal(err)
		}
		for key := range fields {
			copyFields := make(map[string]json.RawMessage)
			for k, v := range fields {
				copyFields[k] = v
			}
			delete(copyFields, key)
			b, _ := json.Marshal(copyFields)
			cases[base.name+" missing "+key] = string(b)
			for _, value := range []string{"null", "false", "{}", "[]", "17"} {
				if key == "count" && value == "17" {
					continue
				}
				copyFields[key] = json.RawMessage(value)
				b, _ = json.Marshal(copyFields)
				cases[base.name+" type "+key+" "+value] = string(b)
			}
		}
		for _, key := range []string{"cart_id", "actor", "price", "operation_id", "quantity", "extra", "count", "menu_id", "unit_ids", "note"} {
			if _, exists := fields[key]; exists {
				continue
			}
			cases[base.name+" extra "+key] = strings.TrimSuffix(base.input, "}") + "," + quoted(key) + `:"untrusted"}`
		}
	}
	for _, v := range []string{`[null]`, `[2]`, `[{}]`, `[["` + unit + `"]]`, quoted(unit)} {
		cases["target type "+v] = strings.Replace(remove, `[`+quoted(unit)+`]`, v, 1)
	}
	// Unique syntactically valid IDs exceed the domain constructor's target
	// budget while remaining below the decoder's independent byte budget.
	ids := make([]string, 8000)
	for i := range ids {
		id := strconv.FormatInt(int64(i), 16)
		ids[i] = quoted("10000000-0000-4000-8000-" + strings.Repeat("0", 12-len(id)) + id)
	}
	cases["domain target budget"] = strings.Replace(remove, quoted(unit), strings.Join(ids, ","), 1)
	for name, input := range cases {
		t.Run(name, func(t *testing.T) { assertRejected(t, input, trusted(t)) })
	}
}

func assertRejected(t *testing.T, input string, id cart.CartID) {
	t.Helper()
	got, err := cartintent.Decode(input, id)
	if err != cartintent.ErrInput || !reflect.DeepEqual(got, cart.Call{}) {
		t.Fatalf("rejection must return exact static ErrInput and zero Call: err=%v, zero=%v", err, reflect.DeepEqual(got, cart.Call{}))
	}
}

func TestDecodeRejectsZeroTrustedCart(t *testing.T) {
	for _, input := range []string{add, remove, note} {
		assertRejected(t, input, cart.CartID{})
	}
}

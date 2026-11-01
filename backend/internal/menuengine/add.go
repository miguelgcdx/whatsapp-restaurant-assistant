package menuengine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/cartintent"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/commandcode"
)

var ErrClarificationRequired = errors.New("cart edit requires clarification")

// ProposeAdd produces a proposal, not authorization or a completed cart edit.
func (e *Engine) ProposeAdd(parent context.Context, text string, id cart.CartID, version cart.Version) (cart.Call, error) {
	if parent == nil || e == nil || !validConfig(e.config) || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return cart.Call{}, ErrInput
	}
	if _, err := cart.ReadCall(id); err != nil {
		return cart.Call{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, turnTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return cart.Call{}, err
	}
	snapshot, err := e.config.ReadMenu(ctx)
	if ctx.Err() != nil {
		return cart.Call{}, ctx.Err()
	}
	if err != nil {
		return cart.Call{}, menuError(err)
	}
	if err = snapshot.Validate(); err != nil {
		return cart.Call{}, menuError(err)
	}
	menu, count, ok := resolveAdd(text, snapshot)
	if !ok {
		return cart.Call{}, ErrClarificationRequired
	}
	action, err := cart.Add(menu, count)
	if err != nil {
		return cart.Call{}, ErrInput
	}
	expected, err := cart.EditCall(id, version, action)
	if err != nil {
		return cart.Call{}, ErrInput
	}
	payload, err := encodeMenu(snapshot)
	if err != nil {
		return cart.Call{}, err
	}
	contextData, err := json.Marshal(struct {
		Menu    json.RawMessage `json:"menu"`
		Cart    string          `json:"cart_id"`
		Version string          `json:"expected_version"`
	}{json.RawMessage(payload), id.String(), strconv.FormatInt(version.Int64(), 10)})
	if err != nil {
		return cart.Call{}, ErrMenuUnavailable
	}
	if len(contextData) > toolResultBudget {
		return cart.Call{}, ErrResource
	}
	system := "Propose exactly the requested add using the supplied menu and version. Never confirm a cart update. " + string(contextData)
	request := commandcode.Request{Messages: []commandcode.Message{{Role: commandcode.RoleSystem, Content: &system}, {Role: commandcode.RoleUser, Content: &text}}, Tools: []commandcode.ToolDefinition{{Name: "propose_add", Description: "Propose one add to the server-selected cart.", Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["add"]},"expected_version":{"type":"string"},"menu_id":{"type":"string"},"count":{"type":"integer","minimum":1}},"required":["action","expected_version","menu_id","count"],"additionalProperties":false}`)}}, MaxOutputTokens: e.config.MaxOutputTokens}
	if err := ctx.Err(); err != nil {
		return cart.Call{}, err
	}
	result, err := e.config.Client.Complete(ctx, request)
	if ctx.Err() != nil {
		return cart.Call{}, ctx.Err()
	}
	if err != nil {
		return cart.Call{}, err
	}
	calls := result.Message.ToolCalls
	if len(calls) != 1 || calls[0].Type != "function" || calls[0].ID == "" || calls[0].Function.Name != "propose_add" || (result.Message.Content != nil && *result.Message.Content != "") {
		return cart.Call{}, ErrProtocol
	}
	decoded, err := cartintent.Decode(calls[0].Function.Arguments, id)
	if err != nil || !reflect.DeepEqual(decoded, expected) {
		return cart.Call{}, ErrProtocol
	}
	return decoded, nil
}

func resolveAdd(text string, snapshot catalog.MenuSnapshot) (cart.MenuID, int64, bool) {
	text = strings.TrimSpace(text)
	verb, remainder, ok := strings.Cut(text, " ")
	if !ok || !strings.EqualFold(verb, "add") {
		return cart.MenuID{}, 0, false
	}
	remainder = strings.TrimLeft(remainder, " ")
	countText, rest, ok := strings.Cut(remainder, " ")
	rest = strings.TrimLeft(rest, " ")
	if !ok || countText == "" || rest == "" {
		return cart.MenuID{}, 0, false
	}
	words := map[string]int64{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10}
	count, ok := words[strings.ToLower(countText)]
	if !ok {
		var err error
		count, err = strconv.ParseInt(countText, 10, 64)
		if err != nil || count <= 0 || strconv.FormatInt(count, 10) != countText {
			return cart.MenuID{}, 0, false
		}
	}
	var found cart.MenuID
	matches := map[cart.MenuID]bool{}
	for _, item := range snapshot.Items {
		if strings.EqualFold(rest, item.Name) || strings.EqualFold(rest, item.Name+"s") {
			id, err := cart.ParseMenuID(item.ID)
			if err != nil {
				return cart.MenuID{}, 0, false
			}
			matches[id] = true
			found = id
		}
	}
	return found, count, len(matches) == 1
}

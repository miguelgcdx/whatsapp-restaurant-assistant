// Package pricing calculates local draft totals from an explicitly supplied,
// trusted catalog. It performs no persistence, fulfillment or order submission.
package pricing

import (
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

// Settings must be supplied explicitly; there are no restaurant defaults.
type Settings struct {
	Currency    string
	Exponent    int
	DeliveryFee int64
}

// Item is a trusted fixed catalog entry, not customer-supplied pricing data.
type Item struct {
	ID        string
	Name      string
	Currency  string
	Price     int64
	Available bool
}

// Unit has identity and an exact, unpriced note, but deliberately no price field.
type Unit struct {
	ID     string
	MenuID string
	Note   string
}

type FulfillmentKind string

const (
	Pickup   FulfillmentKind = "pickup"
	Delivery FulfillmentKind = "delivery"
)

// Fulfillment's zero value is an unselected choice; Quote rejects it.
// Pickup requires empty Address and Phone; delivery requires both.
type Fulfillment struct {
	Kind    FulfillmentKind
	Address string
	Phone   string
}

// Quote is a detached result. Payment is derived, never supplied by the caller.
type Quote struct {
	Units       []Unit
	Currency    string
	Exponent    int
	Subtotal    int64
	Fee         int64
	Total       int64
	Payment     string
	Fulfillment Fulfillment
}

// Engine is an immutable validated settings/catalog snapshot. The zero value is
// invalid. Construct another Engine to consume a new trusted availability view.
type Engine struct {
	settings Settings
	items    map[string]Item
}

// New validates the complete catalog (including unused/unavailable entries),
// then owns a copy. It neither supplies defaults nor mutates the caller's data.
func New(settings Settings, catalog []Item) (*Engine, error) {
	if !currency(settings.Currency) || settings.Exponent < 0 || settings.Exponent > 6 || settings.DeliveryFee < 0 {
		return nil, errors.New("invalid settings")
	}
	items := make(map[string]Item, len(catalog))
	for _, item := range catalog {
		if !uuid(item.ID) || !text(item.Name, 200, true) || item.Price < 0 || item.Currency != settings.Currency {
			return nil, errors.New("invalid catalog")
		}
		if _, exists := items[item.ID]; exists {
			return nil, errors.New("invalid catalog")
		}
		items[item.ID] = item
	}
	return &Engine{settings: settings, items: items}, nil
}

// Add appends one available catalog unit, rejecting duplicate IDs and overflow.
func (e *Engine) Add(draft []Unit, unit Unit) ([]Unit, error) {
	subtotal, err := e.validate(draft)
	if err != nil {
		return nil, err
	}
	if !validUnit(unit) {
		return nil, errors.New("invalid unit")
	}
	for _, existing := range draft {
		if existing.ID == unit.ID {
			return nil, errors.New("duplicate unit ID")
		}
	}
	item, exists := e.items[unit.MenuID]
	if !exists {
		return nil, errors.New("unknown menu item")
	}
	if !item.Available {
		return nil, errors.New("menu item unavailable")
	}
	if _, err := add(subtotal, item.Price); err != nil {
		return nil, err
	}
	return append(clone(draft), unit), nil
}

// Remove deletes exactly the identified unit, retaining order and other notes.
func (e *Engine) Remove(draft []Unit, id string) ([]Unit, error) {
	if _, err := e.validate(draft); err != nil {
		return nil, err
	}
	for i, unit := range draft {
		if unit.ID == id {
			result := clone(draft)
			return append(result[:i], result[i+1:]...), nil
		}
	}
	return nil, errors.New("unit not found")
}

// SetNote replaces only the identified unit's note, with no trimming or pricing.
func (e *Engine) SetNote(draft []Unit, id, note string) ([]Unit, error) {
	if _, err := e.validate(draft); err != nil {
		return nil, err
	}
	if !text(note, 500, false) {
		return nil, errors.New("invalid note")
	}
	for i, unit := range draft {
		if unit.ID == id {
			result := clone(draft)
			result[i].Note = note
			return result, nil
		}
	}
	return nil, errors.New("unit not found")
}

// Quote prices all units from the trusted snapshot, regardless of availability.
// An empty draft is allowed here, not authorization to submit an empty order.
func (e *Engine) Quote(draft []Unit, fulfillment Fulfillment) (Quote, error) {
	subtotal, err := e.validate(draft)
	if err != nil {
		return Quote{}, err
	}
	var fee int64
	var payment string
	switch fulfillment.Kind {
	case Pickup:
		if fulfillment.Address != "" || fulfillment.Phone != "" {
			return Quote{}, errors.New("invalid fulfillment")
		}
		payment = "pay_at_pickup"
	case Delivery:
		if !text(fulfillment.Address, 1000, true) || !text(fulfillment.Phone, 128, true) {
			return Quote{}, errors.New("invalid fulfillment")
		}
		fee = e.settings.DeliveryFee
		payment = "cash_on_delivery"
	default:
		return Quote{}, errors.New("invalid fulfillment")
	}
	total, err := add(subtotal, fee)
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		Units: clone(draft), Currency: e.settings.Currency, Exponent: e.settings.Exponent,
		Subtotal: subtotal, Fee: fee, Total: total, Payment: payment, Fulfillment: fulfillment,
	}, nil
}

func (e *Engine) validate(draft []Unit) (int64, error) {
	if e == nil || e.items == nil {
		return 0, errors.New("invalid engine")
	}
	seen := make(map[string]struct{}, len(draft))
	var subtotal int64
	for _, unit := range draft {
		if !validUnit(unit) {
			return 0, errors.New("invalid unit")
		}
		if _, exists := seen[unit.ID]; exists {
			return 0, errors.New("duplicate unit ID")
		}
		seen[unit.ID] = struct{}{}
		item, exists := e.items[unit.MenuID]
		if !exists {
			return 0, errors.New("unknown menu item")
		}
		var err error
		subtotal, err = add(subtotal, item.Price)
		if err != nil {
			return 0, err
		}
	}
	return subtotal, nil
}

func add(a, b int64) (int64, error) {
	if a > math.MaxInt64-b {
		return 0, errors.New("amount overflow")
	}
	return a + b, nil
}

func clone(draft []Unit) []Unit {
	if draft == nil {
		return nil
	}
	return append(make([]Unit, 0, len(draft)), draft...)
}

func validUnit(unit Unit) bool {
	return uuid(unit.ID) && uuid(unit.MenuID) && text(unit.Note, 500, false)
}

func text(value string, max int, nonblank bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && (!nonblank || strings.TrimSpace(value) != "")
}

func currency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for i := range value {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}

func uuid(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
		} else if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

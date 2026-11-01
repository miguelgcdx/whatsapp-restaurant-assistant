package stafforders

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
)

type money struct {
	Amount   string `json:"amount_minor"`
	Currency string `json:"currency"`
	Exponent int16  `json:"exponent"`
}
type summary struct {
	ID        string                   `json:"id"`
	Version   string                   `json:"version"`
	CreatedAt string                   `json:"created_at"`
	Status    cart.PickupStatus        `json:"fulfillment_status"`
	Payment   cart.PickupPaymentStatus `json:"payment_status"`
	Kind      string                   `json:"fulfillment_kind"`
	Total     money                    `json:"total"`
}
type fulfillment struct {
	Kind          string `json:"kind"`
	PaymentMethod string `json:"payment_method"`
}
type confirmed struct {
	ID          string      `json:"id"`
	UnitCount   int         `json:"unit_count"`
	UnitsHref   string      `json:"units_href"`
	Fulfillment fulfillment `json:"fulfillment"`
	Subtotal    money       `json:"subtotal"`
	DeliveryFee money       `json:"delivery_fee"`
	Total       money       `json:"total"`
	FeeBasis    string      `json:"fee_basis"`
	FeeSource   *string     `json:"fee_source_order_id"`
}
type payment struct {
	State       cart.PickupPaymentStatus `json:"state"`
	CollectedAt *string                  `json:"collected_at"`
}
type estimate struct {
	Minutes     int64  `json:"minutes"`
	AsOf        string `json:"as_of"`
	Provisional bool   `json:"provisional"`
}
type detailData struct {
	summary
	Confirmed        confirmed                `json:"confirmed"`
	Payment          payment                  `json:"payment"`
	Estimate         *estimate                `json:"estimate"`
	StartedAt        *string                  `json:"preparation_started_at"`
	PermittedActions []cart.PickupStaffAction `json:"permitted_actions"`
}
type unit struct {
	ID     string `json:"id"`
	MenuID string `json:"menu_item_id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	Price  money  `json:"unit_price"`
}
type listData struct {
	Items []summary `json:"items"`
	Next  *string   `json:"next_cursor"`
}
type unitsData struct {
	Items     []unit  `json:"items"`
	Next      *string `json:"next_cursor"`
	SummaryID string  `json:"summary_id"`
	UnitCount int     `json:"unit_count"`
}

func validTime(t time.Time) bool   { return !t.IsZero() && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999 }
func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func nullableTime(t *time.Time) (*string, bool) {
	if t == nil {
		return nil, true
	}
	if !validTime(*t) {
		return nil, false
	}
	s := timestamp(*t)
	return &s, true
}
func validMoney(m cart.PickupMoney) bool {
	if m.AmountMinor < 0 || m.Exponent < 0 || m.Exponent > 6 || len(m.Currency) != 3 {
		return false
	}
	for _, c := range m.Currency {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}
func wireMoney(m cart.PickupMoney) money {
	return money{strconv.FormatInt(int64(m.AmountMinor), 10), m.Currency, m.Exponent}
}
func sameCurrency(a, b cart.PickupMoney) bool {
	return a.Currency == b.Currency && a.Exponent == b.Exponent
}
func validText(s string, max int, nonblank bool) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= max && (!nonblank || strings.TrimSpace(s) != "")
}
func validPayment(p cart.PickupPayment) bool {
	switch p.State {
	case cart.PickupUnpaid:
		return p.CollectedAt == nil
	case cart.PickupPaid:
		return p.CollectedAt != nil && validTime(*p.CollectedAt)
	}
	return false
}
func validHeader(o cart.PickupOrderHeader) bool {
	if !validID(o.ID.String()) || o.Version.Int64() < 0 || !validTime(o.CreatedAt) || !validPayment(o.Payment) || !validMoney(o.Total) {
		return false
	}
	switch o.Status {
	case cart.PickupSubmitted, cart.PickupAccepted, cart.PickupPreparing, cart.PickupReady, cart.PickupPickedUp, cart.PickupRejected, cart.PickupCancelled:
		return true
	}
	return false
}
func wireSummary(o cart.PickupOrderHeader) summary {
	return summary{o.ID.String(), strconv.FormatInt(o.Version.Int64(), 10), timestamp(o.CreatedAt), o.Status, o.Payment.State, "pickup", wireMoney(o.Total)}
}
func validNext(next *string, count int) bool { return next == nil || count > 0 && opaque(*next) }
func projectList(p cart.PickupOrderPage, limit int) (listData, error) {
	if len(p.Items) > limit || !validNext(p.NextCursor, len(p.Items)) {
		return listData{}, cart.ErrStoredData
	}
	d := listData{Items: make([]summary, 0, len(p.Items)), Next: p.NextCursor}
	seen := map[cart.PickupOrderID]bool{}
	for i, o := range p.Items {
		if !validHeader(o) || seen[o.ID] || i > 0 && !sameCurrency(p.Items[0].Total, o.Total) {
			return d, cart.ErrStoredData
		}
		seen[o.ID] = true
		d.Items = append(d.Items, wireSummary(o))
	}
	return d, nil
}
func projectDetail(o cart.PickupOrder, id cart.PickupOrderID) (detailData, error) {
	var d detailData
	c := o.Confirmed
	if o.ID != id || !validHeader(o.PickupOrderHeader) || !validID(c.ID.String()) || c.UnitCount < 1 || !validMoney(c.Subtotal) || !validMoney(c.DeliveryFee) || !validMoney(c.Total) || c.Total != o.Total || c.Subtotal != c.Total || c.DeliveryFee.AmountMinor != 0 || !sameCurrency(c.Total, c.DeliveryFee) {
		return d, cart.ErrStoredData
	}
	if o.PermittedActions == nil || len(o.PermittedActions) > 7 {
		return d, cart.ErrStoredData
	}
	actions := make([]cart.PickupStaffAction, len(o.PermittedActions))
	seen := make(map[cart.PickupStaffAction]bool, len(o.PermittedActions))
	for i, action := range o.PermittedActions {
		switch action {
		case cart.PickupAccept, cart.PickupReject, cart.PickupStartPreparing, cart.PickupMarkReady, cart.PickupMarkPickedUp, cart.PickupCancel, cart.PickupMarkPaid:
		default:
			return d, cart.ErrStoredData
		}
		if seen[action] {
			return d, cart.ErrStoredData
		}
		seen[action] = true
		actions[i] = action
	}
	collected, ok := nullableTime(o.Payment.CollectedAt)
	if !ok {
		return d, cart.ErrStoredData
	}
	started, ok := nullableTime(o.PreparationStartedAt)
	if !ok {
		return d, cart.ErrStoredData
	}
	d = detailData{summary: wireSummary(o.PickupOrderHeader), Confirmed: confirmed{ID: c.ID.String(), UnitCount: c.UnitCount, UnitsHref: "/api/staff/orders/" + id.String() + "/summaries/" + c.ID.String() + "/units", Fulfillment: fulfillment{"pickup", "pay_at_pickup"}, Subtotal: wireMoney(c.Subtotal), DeliveryFee: wireMoney(c.DeliveryFee), Total: wireMoney(c.Total), FeeBasis: "independent"}, Payment: payment{o.Payment.State, collected}, StartedAt: started, PermittedActions: actions}
	if o.Estimate != nil {
		e := o.Estimate
		if e.Minutes < 0 || e.Minutes > 9007199254740991 || !validTime(e.AsOf) {
			return detailData{}, cart.ErrStoredData
		}
		d.Estimate = &estimate{e.Minutes, timestamp(e.AsOf), e.Provisional}
	}
	return d, nil
}
func projectUnits(p cart.PickupOrderUnitPage, id cart.PickupCandidateID, limit int) (unitsData, error) {
	if p.SummaryID != id || !validID(p.SummaryID.String()) || p.UnitCount < 1 || len(p.Items) > p.UnitCount || len(p.Items) > limit || !validNext(p.NextCursor, len(p.Items)) {
		return unitsData{}, cart.ErrStoredData
	}
	d := unitsData{Items: make([]unit, 0, len(p.Items)), Next: p.NextCursor, SummaryID: p.SummaryID.String(), UnitCount: p.UnitCount}
	seen := map[cart.UnitID]bool{}
	var currency cart.PickupMoney
	for i, u := range p.Items {
		if !validID(u.ID.String()) || !validID(u.MenuID.String()) || !validText(u.Name, 200, true) || !validText(u.Note, 500, false) || !validMoney(u.Price) || seen[u.ID] || i > 0 && !sameCurrency(currency, u.Price) {
			return d, cart.ErrStoredData
		}
		currency = u.Price
		seen[u.ID] = true
		d.Items = append(d.Items, unit{u.ID.String(), u.MenuID.String(), u.Name, u.Note, wireMoney(u.Price)})
	}
	return d, nil
}

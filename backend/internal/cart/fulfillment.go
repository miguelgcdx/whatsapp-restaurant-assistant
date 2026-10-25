package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/pricing"
)

// ErrFulfillmentRequired is not an implicit pickup choice. Legacy subtotal reads
// remain available before a selection has been made.
var ErrFulfillmentRequired = errors.New("cart fulfillment selection required")

// Fulfillment is closed and immutable; its zero value is invalid. Constructors
// preserve exact text and do not establish geographic or phone ownership.
type Fulfillment struct{ kind, address, phone string }

func PickupFulfillment() Fulfillment { return Fulfillment{kind: "pickup"} }
func DeliveryFulfillment(address, phone string) (Fulfillment, error) {
	if !text(address, 1000, true) || !text(phone, 128, true) {
		return Fulfillment{}, ErrInput
	}
	return Fulfillment{kind: "delivery", address: address, phone: phone}, nil
}
func (f Fulfillment) Kind() string           { return f.kind }
func (f Fulfillment) Address() string        { return f.address }
func (f Fulfillment) RecipientPhone() string { return f.phone }
func (f Fulfillment) valid() bool {
	switch f.kind {
	case "pickup":
		return f.address == "" && f.phone == ""
	case "delivery":
		return text(f.address, 1000, true) && text(f.phone, 128, true)
	}
	return false
}
func (f Fulfillment) payment() string {
	if f.kind == "pickup" {
		return "pay_at_pickup"
	}
	return "cash_on_delivery"
}

// DraftQuote records the complete sampled basis, separately from cart version.
// Values returned to a caller are detached; canonical replay validates them again.
type DraftQuote struct {
	Currency              string `json:"currency"`
	Exponent              int16  `json:"exponent"`
	Subtotal              int64  `json:"subtotal"`
	Fee                   int64  `json:"fee"`
	Total                 int64  `json:"total"`
	PaymentMethod         string `json:"payment_method"`
	SettingsVersion       int64  `json:"settings_version"`
	ConfiguredDeliveryFee int64  `json:"configured_delivery_fee"`
}
type DraftResult struct {
	OperationID OperationID
	Cart        Snapshot
	Fulfillment Fulfillment
	Quote       DraftQuote
}

// Wire DTOs deliberately do not extend legacy request, Snapshot or Result.
// Payment is derived in native code and checked when decoding persisted bytes.
type fulfillmentWire struct {
	Kind    string `json:"kind"`
	Payment string `json:"payment_method"`
	Address string `json:"address,omitempty"`
	Phone   string `json:"recipient_phone,omitempty"`
}

func fulfillmentDTO(f Fulfillment) fulfillmentWire {
	return fulfillmentWire{f.kind, f.payment(), f.address, f.phone}
}
func parseFulfillment(w fulfillmentWire) (Fulfillment, error) {
	var f Fulfillment
	var err error
	switch w.Kind {
	case "pickup":
		f = PickupFulfillment()
	case "delivery":
		f, err = DeliveryFulfillment(w.Address, w.Phone)
	default:
		return Fulfillment{}, ErrStoredData
	}
	if err != nil || fulfillmentDTO(f) != w {
		return Fulfillment{}, ErrStoredData
	}
	return f, nil
}

type draftRequest struct {
	Codec       string           `json:"codec"`
	Action      string           `json:"action"`
	Cart        string           `json:"cart"`
	Expected    int64            `json:"expected"`
	Fulfillment *fulfillmentWire `json:"fulfillment"`
}

func SetFulfillmentCall(id CartID, v Version, f Fulfillment) (Call, error) {
	if !validID(id.value) || v.value < 0 || !f.valid() {
		return Call{}, ErrInput
	}
	w := fulfillmentDTO(f)
	return Call{req: request{Action: "set_fulfillment", Cart: id.value, Expected: v.value}, draft: &draftRequest{"draft-v1", "set_fulfillment", id.value, v.value, &w}}, nil
}
func QuoteCall(id CartID) (Call, error) {
	if !validID(id.value) {
		return Call{}, ErrInput
	}
	return Call{req: request{Action: "quote", Cart: id.value}, draft: &draftRequest{"draft-v1", "quote", id.value, 0, nil}}, nil
}
func encodeCall(c Call) ([]byte, error) {
	if c.draft != nil {
		return json.Marshal(c.draft)
	}
	return json.Marshal(c.req)
}
func decodeDraftCall(body string) (Call, error) {
	var r draftRequest
	if json.Unmarshal([]byte(body), &r) != nil || r.Codec != "draft-v1" {
		return Call{}, ErrStoredData
	}
	id, e := ParseCartID(r.Cart)
	if e != nil {
		return Call{}, ErrStoredData
	}
	var c Call
	switch r.Action {
	case "quote":
		c, e = QuoteCall(id)
	case "set_fulfillment":
		if r.Fulfillment == nil {
			return Call{}, ErrStoredData
		}
		f, err := parseFulfillment(*r.Fulfillment)
		if err != nil {
			return Call{}, ErrStoredData
		}
		v, err := ParseVersion(r.Expected)
		if err != nil {
			return Call{}, ErrStoredData
		}
		c, e = SetFulfillmentCall(id, v, f)
	default:
		return Call{}, ErrStoredData
	}
	b, err := encodeCall(c)
	if e != nil || err != nil || string(b) != body {
		return Call{}, ErrStoredData
	}
	return c, nil
}

type draftResultWire struct {
	Codec       string          `json:"codec"`
	OperationID OperationID     `json:"operation_id"`
	Cart        Snapshot        `json:"cart"`
	Fulfillment fulfillmentWire `json:"fulfillment"`
	Quote       DraftQuote      `json:"quote"`
}

func quoteSnapshot(s Snapshot, f Fulfillment, fee, settingsVersion int64) (DraftQuote, error) {
	if !f.valid() || fee < 0 || settingsVersion < 0 {
		return DraftQuote{}, ErrStoredData
	}
	engine, units, err := snapshotPricing(s, fee)
	if err != nil {
		return DraftQuote{}, err
	}
	// Validate persisted subtotal independently before using the same engine to
	// derive payment and separate fee/total. No current menu lookup is involved.
	subtotal, err := engine.Subtotal(units)
	if err != nil || subtotal != s.Subtotal {
		return DraftQuote{}, ErrStoredData
	}
	applied := int64(0)
	if f.kind == "delivery" {
		applied = fee
	}
	if subtotal > math.MaxInt64-applied {
		return DraftQuote{}, ErrLimit
	}
	q, err := engine.Quote(units, pricing.Fulfillment{Kind: pricing.FulfillmentKind(f.kind), Address: f.address, Phone: f.phone})
	if err != nil {
		return DraftQuote{}, ErrStoredData
	}
	return DraftQuote{q.Currency, int16(q.Exponent), q.Subtotal, q.Fee, q.Total, q.Payment, settingsVersion, fee}, nil
}
func encodeDraftResult(r DraftResult) ([]byte, error) {
	if !validID(r.OperationID.value) {
		return nil, ErrStoredData
	}
	expected, err := quoteSnapshot(r.Cart, r.Fulfillment, r.Quote.ConfiguredDeliveryFee, r.Quote.SettingsVersion)
	if err != nil || expected != r.Quote {
		return nil, ErrStoredData
	}
	b, err := json.Marshal(draftResultWire{"draft-v1", r.OperationID, r.Cart, fulfillmentDTO(r.Fulfillment), r.Quote})
	if err != nil {
		return nil, ErrStoredData
	}
	if len(b) > resultBudget {
		return nil, ErrLimit
	}
	return b, nil
}
func decodeDraftResult(body string) (DraftResult, error) {
	if len(body) == 0 || len(body) > resultBudget {
		return DraftResult{}, ErrStoredData
	}
	var w draftResultWire
	if json.Unmarshal([]byte(body), &w) != nil || w.Codec != "draft-v1" {
		return DraftResult{}, ErrStoredData
	}
	f, err := parseFulfillment(w.Fulfillment)
	if err != nil {
		return DraftResult{}, ErrStoredData
	}
	r := DraftResult{w.OperationID, w.Cart, f, w.Quote}
	b, err := encodeDraftResult(r)
	if err != nil || string(b) != body {
		return DraftResult{}, ErrStoredData
	}
	return r, nil
}

func (s *Store) replayDraft(ctx context.Context, tx pgx.Tx, local LocalContext, op OperationID, req draftRequest) (DraftResult, bool, error) {
	var action, body, id string
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT action,fingerprint,result,cart_id::text FROM app.cart_operations WHERE restaurant_id=$1 AND conversation_id=$2 AND operation_id=$3`, s.restaurant, local.conversation.value, op.value).Scan(&action, &hash, &body, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DraftResult{}, false, nil
	}
	if err != nil {
		return DraftResult{}, false, ErrUnavailable
	}
	b, err := json.Marshal(req)
	if err != nil {
		return DraftResult{}, false, ErrStoredData
	}
	expected := sha256.Sum256(b)
	if action != req.Action || !bytes.Equal(hash, expected[:]) {
		return DraftResult{}, false, ErrConflict
	}
	out, err := decodeDraftResult(body)
	if err != nil {
		return DraftResult{}, false, err
	}
	f, err := parseFulfillment(*req.Fulfillment)
	if err != nil || out.OperationID != op || out.Cart.ID.value != id || id != req.Cart || out.Cart.ConversationID != local.conversation || req.Expected == math.MaxInt64 || out.Cart.Version.value != req.Expected+1 || out.Fulfillment != f {
		return DraftResult{}, false, ErrStoredData
	}
	return out, true, nil
}

// ExecuteDraft accepts ONLY rich descriptors. It shares registration, actual job
// loading and authority barriers with Execute; neither seam dispatches workers.
func (s *Store) ExecuteDraft(ctx context.Context, local LocalContext, execution Execution, job foundation.Job) (DraftResult, error) {
	if !validID(execution.event) || execution.slot < 0 || !validID(execution.operation.value) {
		return DraftResult{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return DraftResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	call, guard, err := s.loadExecution(ctx, tx, local, execution, job)
	if err != nil {
		return DraftResult{}, err
	}
	if call.draft == nil {
		return DraftResult{}, ErrInput
	}
	req := *call.draft
	if req.Action == "set_fulfillment" {
		previous, ok, e := s.replayDraft(ctx, tx, local, execution.operation, req)
		if e != nil {
			return DraftResult{}, e
		}
		if ok {
			if e = guard(); e != nil {
				return DraftResult{}, e
			}
			return previous, nil
		}
	}
	snapshot, err := s.read(ctx, tx, local, CartID{req.Cart})
	if err != nil {
		return DraftResult{}, err
	}
	if req.Action == "set_fulfillment" && (snapshot.Version.value != req.Expected || req.Expected == math.MaxInt64) {
		return DraftResult{}, ErrStale
	}
	var c string
	var exponent int16
	var fee, settingsVersion int64
	if err = tx.QueryRow(ctx, `SELECT currency,currency_exponent,flat_fee_minor,version FROM app.restaurant_settings WHERE restaurant_id=$1 FOR SHARE`, s.restaurant).Scan(&c, &exponent, &fee, &settingsVersion); err != nil {
		return DraftResult{}, unavailable(err)
	}
	if c != snapshot.Currency || exponent != snapshot.Exponent || fee < 0 || settingsVersion < 0 {
		return DraftResult{}, ErrStoredData
	}
	var selected Fulfillment
	if req.Action == "set_fulfillment" {
		selected, err = parseFulfillment(*req.Fulfillment)
	} else {
		var kind string
		var address, phone pgtype.Text
		err = tx.QueryRow(ctx, `SELECT kind,address,recipient_phone FROM app.cart_fulfillments WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3 FOR UPDATE`, s.restaurant, local.conversation.value, req.Cart).Scan(&kind, &address, &phone)
		if errors.Is(err, pgx.ErrNoRows) {
			if e := guard(); e != nil {
				return DraftResult{}, e
			}
			return DraftResult{}, ErrFulfillmentRequired
		}
		if err != nil {
			return DraftResult{}, ErrUnavailable
		}
		switch kind {
		case "pickup":
			if address.Valid || phone.Valid {
				return DraftResult{}, ErrStoredData
			}
			selected = PickupFulfillment()
		case "delivery":
			if !address.Valid || !phone.Valid {
				return DraftResult{}, ErrStoredData
			}
			selected, err = DeliveryFulfillment(address.String, phone.String)
		default:
			return DraftResult{}, ErrStoredData
		}
	}
	if err != nil {
		return DraftResult{}, ErrStoredData
	}
	if req.Action == "set_fulfillment" {
		snapshot.Version = Version{req.Expected + 1}
	}
	quote, err := quoteSnapshot(snapshot, selected, fee, settingsVersion)
	if err != nil {
		return DraftResult{}, err
	}
	out := DraftResult{execution.operation, snapshot, selected, quote}
	body, err := encodeDraftResult(out)
	if err != nil {
		return DraftResult{}, err
	}
	// This check follows ALL blocking cart/settings/selection reads, before any
	// writes or fresh read release. The ledger helper checks again after inserts.
	if err = guard(); err != nil {
		return DraftResult{}, err
	}
	if req.Action == "quote" {
		return out, nil
	}
	var address, phone any
	if selected.kind == "delivery" {
		address = selected.address
		phone = selected.phone
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.cart_fulfillments(restaurant_id,conversation_id,cart_id,kind,address,recipient_phone) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(restaurant_id,conversation_id,cart_id) DO UPDATE SET kind=EXCLUDED.kind,address=EXCLUDED.address,recipient_phone=EXCLUDED.recipient_phone`, s.restaurant, local.conversation.value, req.Cart, selected.kind, address, phone)
	if err != nil {
		return DraftResult{}, ErrUnavailable
	}
	tag, err := tx.Exec(ctx, `UPDATE app.carts SET version=$1 WHERE restaurant_id=$2 AND conversation_id=$3 AND id=$4 AND version=$5`, snapshot.Version.value, s.restaurant, local.conversation.value, req.Cart, req.Expected)
	if err != nil {
		return DraftResult{}, ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return DraftResult{}, ErrStale
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return DraftResult{}, ErrStoredData
	}
	hash := sha256.Sum256(canonical)
	if err = s.commitOperation(ctx, tx, local, execution.operation, snapshot.ID, req.Action, hash, body, guard); err != nil {
		return DraftResult{}, err
	}
	return out, nil
}

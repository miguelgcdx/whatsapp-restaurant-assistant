package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

type summaryPreviewKind struct{}

// SummaryPreviewID identifies a LOCAL immutable draft, never a submitted order
// or evidence of presentation. Parsing proves syntax, not access authority.
type SummaryPreviewID = ID[summaryPreviewKind]

func ParseSummaryPreviewID(s string) (SummaryPreviewID, error) {
	return parseID[summaryPreviewKind](s)
}

type SummaryEstimateReason string

const WorkloadNotConnected SummaryEstimateReason = "workload_not_connected"

type SummaryPreviewUnit struct {
	Unit
	Name string `json:"name"`
}

// SummaryPreview is detached output. Only ExecuteSummaryPreview can capture
// persisted authority; supplying or changing this value authorizes nothing.
// CapturedAt is a capture clock, NOT workload freshness or presentation time.
type SummaryPreview struct {
	ID                SummaryPreviewID
	SourceOperationID OperationID
	CartID            CartID
	ConversationID    ConversationID
	SourceCartVersion Version
	CapturedAt        time.Time
	Currency          string
	Exponent          int16
	Units             []SummaryPreviewUnit
	UnitCount         int
	Fulfillment       Fulfillment
	Quote             DraftQuote
	NumericEstimate   *int64
	EstimateReason    SummaryEstimateReason
	Text              string
}

type SummaryPreviewResult struct {
	OperationID OperationID
	Preview     SummaryPreview
}

// Separate codec: legacy request and draft-v1 bytes are deliberately unchanged.
type summaryPreviewRequest struct {
	Codec    string `json:"codec"`
	Action   string `json:"action"`
	Cart     string `json:"cart,omitempty"`
	Expected *int64 `json:"expected,omitempty"`
	Preview  string `json:"preview_id,omitempty"`
}

func CaptureSummaryPreviewCall(id CartID, v Version) (Call, error) {
	if !validID(id.value) || v.value < 0 {
		return Call{}, ErrInput
	}
	expected := v.value
	return Call{req: request{Action: "capture_preview", Cart: id.value, Expected: v.value}, preview: &summaryPreviewRequest{Codec: "local-preview-v1", Action: "capture_preview", Cart: id.value, Expected: &expected}}, nil
}
func ReadSummaryPreviewCall(id SummaryPreviewID) (Call, error) {
	if !validID(id.value) {
		return Call{}, ErrInput
	}
	return Call{req: request{Action: "read_preview"}, preview: &summaryPreviewRequest{Codec: "local-preview-v1", Action: "read_preview", Preview: id.value}}, nil
}
func decodeSummaryPreviewCall(body string) (Call, error) {
	var w summaryPreviewRequest
	if len(body) == 0 || len(body) > resultBudget || json.Unmarshal([]byte(body), &w) != nil || w.Codec != "local-preview-v1" {
		return Call{}, ErrStoredData
	}
	var c Call
	var err error
	switch w.Action {
	case "capture_preview":
		id, e := ParseCartID(w.Cart)
		if e != nil || w.Expected == nil {
			return Call{}, ErrStoredData
		}
		v, e := ParseVersion(*w.Expected)
		if e != nil {
			return Call{}, ErrStoredData
		}
		c, err = CaptureSummaryPreviewCall(id, v)
	case "read_preview":
		id, e := ParseSummaryPreviewID(w.Preview)
		if e != nil {
			return Call{}, ErrStoredData
		}
		c, err = ReadSummaryPreviewCall(id)
	default:
		return Call{}, ErrStoredData
	}
	b, e := encodeCall(c)
	if err != nil || e != nil || string(b) != body {
		return Call{}, ErrStoredData
	}
	return c, nil
}

type summaryPreviewWire struct {
	Codec             string                `json:"codec"`
	ID                SummaryPreviewID      `json:"id"`
	SourceOperationID OperationID           `json:"source_operation_id"`
	CartID            CartID                `json:"cart_id"`
	ConversationID    ConversationID        `json:"conversation_id"`
	SourceCartVersion Version               `json:"source_cart_version"`
	CapturedAt        time.Time             `json:"captured_at"`
	Currency          string                `json:"currency"`
	Exponent          int16                 `json:"exponent"`
	Units             []SummaryPreviewUnit  `json:"units"`
	UnitCount         int                   `json:"unit_count"`
	Fulfillment       fulfillmentWire       `json:"fulfillment"`
	Quote             DraftQuote            `json:"quote"`
	NumericEstimate   *int64                `json:"numeric_estimate"`
	EstimateReason    SummaryEstimateReason `json:"estimate_reason"`
	Text              string                `json:"text"`
}

func previewDTO(p SummaryPreview) summaryPreviewWire {
	return summaryPreviewWire{"local-preview-v1", p.ID, p.SourceOperationID, p.CartID, p.ConversationID, p.SourceCartVersion, p.CapturedAt, p.Currency, p.Exponent, p.Units, p.UnitCount, fulfillmentDTO(p.Fulfillment), p.Quote, p.NumericEstimate, p.EstimateReason, p.Text}
}
func previewFromDTO(w summaryPreviewWire) (SummaryPreview, error) {
	f, e := parseFulfillment(w.Fulfillment)
	if e != nil || w.Codec != "local-preview-v1" {
		return SummaryPreview{}, ErrStoredData
	}
	return SummaryPreview{w.ID, w.SourceOperationID, w.CartID, w.ConversationID, w.SourceCartVersion, w.CapturedAt, w.Currency, w.Exponent, w.Units, w.UnitCount, f, w.Quote, w.NumericEstimate, w.EstimateReason, w.Text}, nil
}
func validatePreview(p SummaryPreview) error {
	if !validID(p.ID.value) || !validID(p.SourceOperationID.value) || p.CapturedAt.IsZero() || p.CapturedAt.Location() != time.UTC || p.CapturedAt.Year() < 1 || p.CapturedAt.Year() > 9999 || p.NumericEstimate != nil || p.EstimateReason != WorkloadNotConnected || p.Units == nil || p.UnitCount != len(p.Units) || len(p.Units) > resultBudget/72 {
		return ErrStoredData
	}
	snapshot := Snapshot{ID: p.CartID, ConversationID: p.ConversationID, Version: p.SourceCartVersion, Currency: p.Currency, Exponent: p.Exponent, Units: make([]Unit, 0, len(p.Units)), Subtotal: p.Quote.Subtotal}
	seen := make(map[UnitID]bool, len(p.Units))
	for _, u := range p.Units {
		if !text(u.Name, 200, true) || seen[u.ID] {
			return ErrStoredData
		}
		seen[u.ID] = true
		snapshot.Units = append(snapshot.Units, u.Unit)
	}
	expected, e := quoteSnapshot(snapshot, p.Fulfillment, p.Quote.ConfiguredDeliveryFee, p.Quote.SettingsVersion)
	if e != nil || expected != p.Quote || p.Text != summaryPreviewText(p) {
		return ErrStoredData
	}
	return nil
}
func encodePreview(p SummaryPreview) ([]byte, error) {
	if err := validatePreview(p); err != nil {
		return nil, err
	}
	b, e := json.Marshal(previewDTO(p))
	if e != nil {
		return nil, ErrStoredData
	}
	if len(b) > resultBudget {
		return nil, ErrLimit
	}
	return b, nil
}
func decodePreview(body string) (SummaryPreview, error) {
	if len(body) == 0 || len(body) > resultBudget {
		return SummaryPreview{}, ErrStoredData
	}
	var w summaryPreviewWire
	if json.Unmarshal([]byte(body), &w) != nil {
		return SummaryPreview{}, ErrStoredData
	}
	p, e := previewFromDTO(w)
	if e != nil {
		return SummaryPreview{}, e
	}
	b, e := encodePreview(p)
	if e != nil || string(b) != body {
		return SummaryPreview{}, ErrStoredData
	}
	return p, nil
}

type summaryPreviewResultWire struct {
	Codec       string             `json:"codec"`
	OperationID OperationID        `json:"operation_id"`
	Preview     summaryPreviewWire `json:"preview"`
}

func encodePreviewResult(r SummaryPreviewResult) ([]byte, error) {
	if !validID(r.OperationID.value) {
		return nil, ErrStoredData
	}
	if _, err := encodePreview(r.Preview); err != nil {
		return nil, err
	}
	b, e := json.Marshal(summaryPreviewResultWire{"local-preview-v1", r.OperationID, previewDTO(r.Preview)})
	if e != nil {
		return nil, ErrStoredData
	}
	if len(b) > resultBudget {
		return nil, ErrLimit
	}
	return b, nil
}
func decodePreviewResult(body string) (SummaryPreviewResult, error) {
	if len(body) == 0 || len(body) > resultBudget {
		return SummaryPreviewResult{}, ErrStoredData
	}
	var w summaryPreviewResultWire
	if json.Unmarshal([]byte(body), &w) != nil || w.Codec != "local-preview-v1" {
		return SummaryPreviewResult{}, ErrStoredData
	}
	p, e := previewFromDTO(w.Preview)
	if e != nil {
		return SummaryPreviewResult{}, e
	}
	r := SummaryPreviewResult{w.OperationID, p}
	b, e := encodePreviewResult(r)
	if e != nil || string(b) != body {
		return SummaryPreviewResult{}, ErrStoredData
	}
	return r, nil
}

// moneyPreview is called only after checked quote/unit validation. Formatting
// uses decimal integers, including currencies with zero or six decimal places.
func moneyPreview(currency string, exponent int16, amount int64) string {
	digits := strconv.FormatInt(amount, 10)
	if exponent > 0 {
		n := int(exponent)
		if len(digits) <= n {
			digits = strings.Repeat("0", n+1-len(digits)) + digits
		}
		digits = digits[:len(digits)-n] + "." + digits[len(digits)-n:]
	}
	return currency + " " + digits
}
func summaryPreviewText(p SummaryPreview) string {
	var b strings.Builder
	b.WriteString("Local draft preview (not submitted)\nItems:\n")
	if len(p.Units) == 0 {
		b.WriteString("(empty)\n")
	}
	for _, u := range p.Units {
		b.WriteString("- " + strconv.Quote(u.Name) + " | quantity: 1 | price: " + moneyPreview(u.Currency, u.Exponent, u.PriceMinor) + " | note: " + strconv.Quote(u.Note) + "\n")
	}
	b.WriteString("Fulfillment: " + p.Fulfillment.kind + "\n")
	if p.Fulfillment.kind == "delivery" {
		b.WriteString("Delivery address: " + strconv.Quote(p.Fulfillment.address) + "\nRecipient phone: " + strconv.Quote(p.Fulfillment.phone) + "\n")
	}
	b.WriteString("Payment method: " + p.Quote.PaymentMethod + "\nSubtotal: " + moneyPreview(p.Currency, p.Exponent, p.Quote.Subtotal) + "\nDelivery fee: " + moneyPreview(p.Currency, p.Exponent, p.Quote.Fee) + "\nTotal: " + moneyPreview(p.Currency, p.Exponent, p.Quote.Total) + "\nPreparation estimate: unavailable (workload not connected).\nWould you like anything else?")
	return b.String()
}

// storedPreview validates BOTH immutable copies, row metadata, and originating
// registered intent without consulting today's cart units, names or settings.
func (s *Store) storedPreview(ctx context.Context, tx pgx.Tx, local LocalContext, id SummaryPreviewID) (SummaryPreview, error) {
	var rid, restaurant, conversation, cart, op, body string
	var version int64
	var captured pgtype.Timestamptz
	err := tx.QueryRow(ctx, `SELECT id::text,restaurant_id::text,conversation_id::text,cart_id::text,source_cart_version,source_operation_id::text,captured_at,body FROM app.cart_summary_previews WHERE id=$1 AND restaurant_id=$2 AND conversation_id=$3 FOR SHARE`, id.value, s.restaurant, local.conversation.value).Scan(&rid, &restaurant, &conversation, &cart, &version, &op, &captured, &body)
	if err != nil {
		return SummaryPreview{}, unavailable(err)
	}
	p, err := decodePreview(body)
	if err != nil || rid != p.ID.value || restaurant != s.restaurant || conversation != p.ConversationID.value || cart != p.CartID.value || version != p.SourceCartVersion.value || op != p.SourceOperationID.value || !captured.Valid || captured.InfinityModifier != pgtype.Finite || !captured.Time.Equal(p.CapturedAt) {
		return SummaryPreview{}, ErrStoredData
	}
	var action, intent string
	var hash []byte
	err = tx.QueryRow(ctx, `SELECT action,canonical_intent,fingerprint FROM app.cart_execution_calls WHERE operation_id=$1 AND restaurant_id=$2 AND conversation_id=$3 FOR SHARE`, op, s.restaurant, local.conversation.value).Scan(&action, &intent, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return SummaryPreview{}, ErrStoredData
	}
	if err != nil {
		return SummaryPreview{}, ErrUnavailable
	}
	source, err := decodeCall(intent)
	expected := sha256.Sum256([]byte(intent))
	if err != nil || source.preview == nil || action != "capture_preview" || source.req.Action != action || source.req.Cart != cart || source.req.Expected != version || !bytes.Equal(hash, expected[:]) {
		return SummaryPreview{}, ErrStoredData
	}
	var ledgerAction, ledgerBody, ledgerCart string
	var ledgerHash []byte
	err = tx.QueryRow(ctx, `SELECT action,fingerprint,result,cart_id::text FROM app.cart_operations WHERE restaurant_id=$1 AND conversation_id=$2 AND operation_id=$3 FOR SHARE`, s.restaurant, local.conversation.value, op).Scan(&ledgerAction, &ledgerHash, &ledgerBody, &ledgerCart)
	if errors.Is(err, pgx.ErrNoRows) {
		return SummaryPreview{}, ErrStoredData
	}
	if err != nil {
		return SummaryPreview{}, ErrUnavailable
	}
	expectedBody, err := encodePreviewResult(SummaryPreviewResult{p.SourceOperationID, p})
	if err != nil || ledgerAction != action || ledgerCart != cart || !bytes.Equal(ledgerHash, expected[:]) || ledgerBody != string(expectedBody) {
		return SummaryPreview{}, ErrStoredData
	}
	return p, nil
}

func (s *Store) replayPreview(ctx context.Context, tx pgx.Tx, local LocalContext, op OperationID, req summaryPreviewRequest) (SummaryPreviewResult, bool, error) {
	var action, body, cart string
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT action,fingerprint,result,cart_id::text FROM app.cart_operations WHERE restaurant_id=$1 AND conversation_id=$2 AND operation_id=$3 FOR SHARE`, s.restaurant, local.conversation.value, op.value).Scan(&action, &hash, &body, &cart)
	if errors.Is(err, pgx.ErrNoRows) {
		return SummaryPreviewResult{}, false, nil
	}
	if err != nil {
		return SummaryPreviewResult{}, false, ErrUnavailable
	}
	b, err := json.Marshal(req)
	if err != nil {
		return SummaryPreviewResult{}, false, ErrStoredData
	}
	expected := sha256.Sum256(b)
	if action != req.Action || !bytes.Equal(hash, expected[:]) {
		return SummaryPreviewResult{}, false, ErrConflict
	}
	r, err := decodePreviewResult(body)
	if err != nil || r.OperationID != op || r.Preview.SourceOperationID != op || cart != req.Cart || r.Preview.CartID.value != cart || r.Preview.ConversationID != local.conversation || req.Expected == nil || r.Preview.SourceCartVersion.value != *req.Expected {
		return SummaryPreviewResult{}, false, ErrStoredData
	}
	p, err := s.storedPreview(ctx, tx, local, r.Preview.ID)
	if errors.Is(err, ErrNotFound) {
		// A caller's unknown read ID is not found; a missing immutable row
		// referenced by a committed result is instead corrupt stored data.
		return SummaryPreviewResult{}, false, ErrStoredData
	}
	if err != nil {
		return SummaryPreviewResult{}, false, err
	}
	canonical, err := encodePreviewResult(SummaryPreviewResult{op, p})
	if err != nil || string(canonical) != body {
		return SummaryPreviewResult{}, false, ErrStoredData
	}
	return r, true, nil
}

// ExecuteSummaryPreview accepts only local-preview-v1 descriptors. The current
// actual job claim is checked before cached output and after blocking resources.
// No worker completion, outbox, order, payment or presentation is performed.
func (s *Store) ExecuteSummaryPreview(ctx context.Context, local LocalContext, execution Execution, job foundation.Job) (SummaryPreviewResult, error) {
	if !validID(execution.event) || execution.slot < 0 || !validID(execution.operation.value) {
		return SummaryPreviewResult{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	call, guard, err := s.loadExecution(ctx, tx, local, execution, job)
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	if call.preview == nil {
		return SummaryPreviewResult{}, ErrInput
	}
	req := *call.preview
	if req.Action == "read_preview" {
		p, err := s.storedPreview(ctx, tx, local, SummaryPreviewID{req.Preview})
		if err != nil {
			return SummaryPreviewResult{}, err
		}
		if err = guard(); err != nil {
			return SummaryPreviewResult{}, err
		}
		return SummaryPreviewResult{execution.operation, p}, nil
	}
	if previous, ok, e := s.replayPreview(ctx, tx, local, execution.operation, req); e != nil {
		return SummaryPreviewResult{}, e
	} else if ok {
		if e = guard(); e != nil {
			return SummaryPreviewResult{}, e
		}
		return previous, nil
	}
	snapshot, err := s.read(ctx, tx, local, CartID{req.Cart})
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	if snapshot.Version.value != *req.Expected {
		return SummaryPreviewResult{}, ErrStale
	}
	selected, quote, err := s.loadDraftQuote(ctx, tx, local, snapshot, nil, guard)
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	// Acquire catalog locks in deterministic menu-ID order, retaining stored unit
	// prices and notes even if current price/availability changed.
	menus := make(map[MenuID]string)
	for _, u := range snapshot.Units {
		menus[u.MenuID] = ""
	}
	ids := make([]MenuID, 0, len(menus))
	for id := range menus {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].value < ids[j].value })
	for _, id := range ids {
		var name string
		if err = tx.QueryRow(ctx, `SELECT name FROM app.menu_items WHERE restaurant_id=$1 AND id=$2 FOR SHARE`, s.restaurant, id.value).Scan(&name); err != nil {
			return SummaryPreviewResult{}, unavailable(err)
		}
		if !text(name, 200, true) {
			return SummaryPreviewResult{}, ErrStoredData
		}
		menus[id] = name
	}
	// A separate trusted clock statement follows ALL blocking resource reads.
	var captured time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&captured); err != nil {
		return SummaryPreviewResult{}, ErrUnavailable
	}
	id, err := newID[summaryPreviewKind]()
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	p := SummaryPreview{ID: id, SourceOperationID: execution.operation, CartID: snapshot.ID, ConversationID: snapshot.ConversationID, SourceCartVersion: snapshot.Version, CapturedAt: captured.UTC(), Currency: snapshot.Currency, Exponent: snapshot.Exponent, Units: make([]SummaryPreviewUnit, 0, len(snapshot.Units)), UnitCount: len(snapshot.Units), Fulfillment: selected, Quote: quote, EstimateReason: WorkloadNotConnected}
	for _, u := range snapshot.Units {
		p.Units = append(p.Units, SummaryPreviewUnit{u, menus[u.MenuID]})
	}
	p.Text = summaryPreviewText(p)
	body, err := encodePreview(p)
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	out := SummaryPreviewResult{execution.operation, p}
	result, err := encodePreviewResult(out)
	if err != nil {
		return SummaryPreviewResult{}, err
	}
	if err = guard(); err != nil {
		return SummaryPreviewResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.cart_summary_previews(id,restaurant_id,conversation_id,cart_id,source_cart_version,source_operation_id,captured_at,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.ID.value, s.restaurant, local.conversation.value, p.CartID.value, p.SourceCartVersion.value, p.SourceOperationID.value, p.CapturedAt, string(body))
	if err != nil {
		return SummaryPreviewResult{}, ErrUnavailable
	}
	if err = guard(); err != nil {
		return SummaryPreviewResult{}, err
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return SummaryPreviewResult{}, ErrStoredData
	}
	hash := sha256.Sum256(canonical)
	if err = s.commitOperation(ctx, tx, local, execution.operation, p.CartID, req.Action, hash, result, guard); err != nil {
		return SummaryPreviewResult{}, err
	}
	return out, nil
}

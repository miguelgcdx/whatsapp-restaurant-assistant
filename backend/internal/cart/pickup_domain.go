package cart

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/preparation"
)

// PickupStore borrows its cart store. It has deliberately no Close method.
// Cursor keys are process-local cryptographic randomness, not public identities;
// reopening the facade invalidates old read cursors, never durable operations.
type PickupStore struct {
	cart       *Store
	assessment string
	cursorKey  [32]byte
}

func NewPickupStore(s *Store, restaurantID, acceptedAssessmentVersion string) (*PickupStore, error) {
	if s == nil || s.pool == nil || !validID(restaurantID) || s.restaurant != restaurantID || acceptedAssessmentVersion != PickupCompletionAssessmentVersion {
		return nil, ErrInput
	}
	p := &PickupStore{cart: s, assessment: acceptedAssessmentVersion}
	if _, err := rand.Read(p.cursorKey[:]); err != nil {
		return nil, ErrUnavailable
	}
	return p, nil
}

var _ PickupRuntime = (*PickupStore)(nil)
var _ PickupStaffPort = (*PickupStore)(nil)

const pickupQuestion = "Would you like anything else?"

type pickupCandidate struct {
	Prepared  PreparedPickupSummary
	Snapshot  Snapshot
	Quote     DraftQuote
	Units     []SummaryPreviewUnit
	Control   Version
	Operation OperationID
	Created   time.Time
}

func pickupJSON(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, ErrStoredData
	}
	if len(b) > resultBudget-2048 {
		return nil, ErrLimit
	}
	return b, nil
}
func pickupDecode(body string, out any) error {
	if len(body) == 0 || len(body) > resultBudget || json.Unmarshal([]byte(body), out) != nil {
		return ErrStoredData
	}
	b, e := pickupJSON(out)
	if e != nil || string(b) != body {
		return ErrStoredData
	}
	return nil
}
func pickupText(c pickupCandidate) string {
	var b strings.Builder
	b.WriteString("Pickup summary (not submitted)\nItems:\n")
	for _, u := range c.Units {
		b.WriteString("- " + strconv.Quote(u.Name) + " | quantity: 1 | price: " + moneyPreview(u.Currency, u.Exponent, u.PriceMinor) + " | note: " + strconv.Quote(u.Note) + "\n")
	}
	b.WriteString("Fulfillment: pickup\nPayment method: pay_at_pickup\nSubtotal: " + moneyPreview(c.Snapshot.Currency, c.Snapshot.Exponent, c.Quote.Subtotal) + "\nDelivery fee: " + moneyPreview(c.Snapshot.Currency, c.Snapshot.Exponent, 0) + "\nTotal: " + moneyPreview(c.Snapshot.Currency, c.Snapshot.Exponent, c.Quote.Total) + "\n")
	if c.Prepared.Estimate == nil {
		b.WriteString("Preparation estimate: unavailable (active work overdue).\n")
	} else {
		b.WriteString("Preparation estimate: " + strconv.FormatInt(c.Prepared.Estimate.Minutes, 10) + " minutes (provisional, preparation only).\n")
	}
	b.WriteString(pickupQuestion)
	return b.String()
}
func pickupMoney(s Snapshot, n int64) PickupMoney {
	return PickupMoney{PickupMinor(n), s.Currency, s.Exponent}
}
func validPickupEstimate(e *PickupEstimate) bool {
	return e == nil || e.Minutes > 0 && !e.AsOf.IsZero() && e.AsOf.Location() == time.UTC && e.Provisional
}
func validatePickupCandidate(c pickupCandidate) error {
	s := c.Prepared
	total, e := validateSnapshot(c.Snapshot)
	if e != nil || len(c.Snapshot.Units) == 0 || total != c.Snapshot.Subtotal || !validID(s.CandidateID.value) || !validID(s.Intent.ID.value) || !validID(c.Operation.value) || c.Created.IsZero() || c.Created.Location() != time.UTC || c.Control.value < 0 || s.CartID != c.Snapshot.ID || s.CartVersion != c.Snapshot.Version || s.UnitCount != len(c.Units) || len(c.Units) != len(c.Snapshot.Units) || s.Total != pickupMoney(c.Snapshot, total) || !validPickupEstimate(s.Estimate) || s.ClosingQuestion != pickupQuestion || s.Intent.CandidateID != s.CandidateID || s.Intent.ConversationID != c.Snapshot.ConversationID {
		return ErrStoredData
	}
	quote, e := quoteSnapshot(c.Snapshot, PickupFulfillment(), c.Quote.ConfiguredDeliveryFee, c.Quote.SettingsVersion)
	if e != nil || quote != c.Quote {
		return ErrStoredData
	}
	for i, u := range c.Units {
		if u.Unit != c.Snapshot.Units[i] || !text(u.Name, 200, true) {
			return ErrStoredData
		}
	}
	if s.Intent.Text != pickupText(c) || s.Intent.TextDigest != sha256.Sum256([]byte(s.Intent.Text)) {
		return ErrStoredData
	}
	return nil
}

// pickupAuthority follows the established restaurant/conversation/source/call/
// control/job prefix. Loading a registered descriptor is never authorization.
func (p *PickupStore) pickupAuthority(ctx context.Context, tx pgx.Tx, x PickupExecution, registered bool) (Call, authorityGuard, error) {
	if x.Source.restaurant != p.cart.restaurant || x.Source.conversation != x.Local.conversation || x.Source.business != x.Local.business || x.Source.sender != x.Local.sender || !validID(x.Source.eventUUID) {
		return Call{}, nil, ErrNotFound
	}
	source, e := p.cart.bindSourceTx(ctx, tx, x.Local, x.Source.eventProviderID)
	if e != nil {
		return Call{}, nil, e
	}
	if source != x.Source {
		return Call{}, nil, ErrNotFound
	}
	var call Call
	if registered {
		if x.Execution.event != source.eventUUID || x.Execution.slot < 0 || !validID(x.Execution.operation.value) {
			return Call{}, nil, ErrInput
		}
		call, e = p.cart.loadRegisteredExecution(ctx, tx, x.Local, x.Execution)
		if e != nil {
			return Call{}, nil, e
		}
	}
	var mode string
	var epoch int64
	if e = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, p.cart.restaurant, x.Local.conversation.value).Scan(&mode, &epoch); e != nil {
		return Call{}, nil, unavailable(e)
	}
	if (mode != "ai" && mode != "staff") || epoch < 0 {
		return Call{}, nil, ErrStoredData
	}
	if epoch != x.ControlVersion.value {
		return Call{}, nil, ErrStale
	}
	a, e := readMenuAdmissionTx(ctx, tx, source)
	if e != nil {
		return Call{}, nil, e
	}
	guard, e := postgres.LockMenuExecutionJob(ctx, tx, postgres.MenuExecutionBinding{EventUUID: source.eventUUID, RestaurantID: source.restaurant, ConversationID: source.conversation.value, Business: source.business, Sender: source.sender, AdmissionMode: a.Mode, AdmissionVersion: a.Version}, x.Claim)
	if e != nil {
		return Call{}, nil, cartClaimError(e)
	}
	checked := func() error {
		if e := guard(); e != nil {
			return cartClaimError(e)
		}
		return sourceContextError(ctx)
	}
	if e = checked(); e != nil {
		return Call{}, nil, e
	}
	// Bounded completion may use fresh Staff-control authority, but producing
	// another conversational question must not bypass the existing AI pause.
	if registered && call.req.Action == pickupPrepareAction && (mode != "ai" || epoch != a.Version) {
		return Call{}, nil, ErrStale
	}
	return call, checked, nil
}
func (p *PickupStore) begin(ctx context.Context, x PickupExecution, registered bool) (pgx.Tx, Call, authorityGuard, error) {
	if ctx == nil || p == nil || p.cart == nil {
		return nil, Call{}, nil, ErrInput
	}
	tx, e := p.cart.begin(ctx, x.Local)
	if e != nil {
		return nil, Call{}, nil, e
	}
	call, g, e := p.pickupAuthority(ctx, tx, x, registered)
	if e != nil {
		rollbackNative(tx)
		return nil, Call{}, nil, e
	}
	return tx, call, g, nil
}
func (p *PickupStore) ledger(ctx context.Context, tx pgx.Tx, x PickupExecution, call Call) (string, bool, error) {
	var action, body string
	var hash []byte
	e := tx.QueryRow(ctx, `SELECT action,fingerprint,result FROM app.cart_operations WHERE restaurant_id=$1 AND conversation_id=$2 AND operation_id=$3 FOR SHARE`, p.cart.restaurant, x.Local.conversation.value, x.Execution.operation.value).Scan(&action, &hash, &body)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", false, nil
	}
	if e != nil {
		return "", false, ErrUnavailable
	}
	b, e := encodeCall(call)
	if e != nil {
		return "", false, ErrStoredData
	}
	digest := sha256.Sum256(b)
	if action != call.req.Action || !bytes.Equal(hash, digest[:]) {
		return "", false, ErrConflict
	}
	return body, true, nil
}
func (p *PickupStore) candidate(ctx context.Context, tx pgx.Tx, local LocalContext, id PickupCandidateID) (pickupCandidate, error) {
	var body, cart, op string
	var v, control int64
	var created time.Time
	e := tx.QueryRow(ctx, `SELECT body,cart_id::text,cart_version,control_version,operation_id::text,created_at FROM app.pickup_candidates WHERE restaurant_id=$1 AND conversation_id=$2 AND id=$3 FOR SHARE`, p.cart.restaurant, local.conversation.value, id.value).Scan(&body, &cart, &v, &control, &op, &created)
	if e != nil {
		return pickupCandidate{}, unavailable(e)
	}
	var c pickupCandidate
	if pickupDecode(body, &c) != nil || validatePickupCandidate(c) != nil || c.Prepared.CandidateID != id || c.Snapshot.ConversationID != local.conversation || c.Snapshot.ID.value != cart || c.Snapshot.Version.value != v || c.Control.value != control || c.Operation.value != op || !c.Created.Equal(created) {
		return pickupCandidate{}, ErrStoredData
	}
	// Validate the originating registered intent and its original committed copy;
	// neither an isolated candidate body nor a detached preview grants authority.
	var action, intent, ledgerAction, ledgerBody, ledgerCart string
	var hash, ledgerHash []byte
	e = tx.QueryRow(ctx, `SELECT e.action,e.canonical_intent,e.fingerprint,o.action,o.fingerprint,o.result,o.cart_id::text FROM app.cart_execution_calls e JOIN app.cart_operations o ON o.restaurant_id=e.restaurant_id AND o.conversation_id=e.conversation_id AND o.operation_id=e.operation_id WHERE e.operation_id=$1 AND e.restaurant_id=$2 AND e.conversation_id=$3 FOR SHARE OF e,o`, c.Operation.value, p.cart.restaurant, local.conversation.value).Scan(&action, &intent, &hash, &ledgerAction, &ledgerHash, &ledgerBody, &ledgerCart)
	if errors.Is(e, pgx.ErrNoRows) {
		return pickupCandidate{}, ErrStoredData
	}
	if e != nil {
		return pickupCandidate{}, ErrUnavailable
	}
	call, e := decodeCall(intent)
	expected := sha256.Sum256([]byte(intent))
	result, encodeErr := pickupJSON(c.Prepared)
	if e != nil || call.pickup == nil || action != pickupPrepareAction || call.pickup.Action != action || call.pickup.Cart != cart || call.pickup.Expected == nil || *call.pickup.Expected != v || !bytes.Equal(hash, expected[:]) || ledgerAction != action || !bytes.Equal(ledgerHash, expected[:]) || encodeErr != nil || ledgerBody != string(result) || ledgerCart != cart {
		return pickupCandidate{}, ErrStoredData
	}
	rows, e := tx.Query(ctx, `SELECT unit_id::text,menu_id::text,name,note,price_minor FROM app.pickup_candidate_units WHERE candidate_id=$1 ORDER BY position`, id.value)
	if e != nil {
		return pickupCandidate{}, ErrUnavailable
	}
	i := 0
	for rows.Next() {
		var uid, mid, name, note string
		var price int64
		if e = rows.Scan(&uid, &mid, &name, &note, &price); e != nil {
			rows.Close()
			return pickupCandidate{}, ErrUnavailable
		}
		if i >= len(c.Units) {
			rows.Close()
			return pickupCandidate{}, ErrStoredData
		}
		u := c.Units[i]
		if uid != u.ID.value || mid != u.MenuID.value || name != u.Name || note != u.Note || price != u.PriceMinor {
			rows.Close()
			return pickupCandidate{}, ErrStoredData
		}
		i++
	}
	rows.Close()
	if rows.Err() != nil {
		return pickupCandidate{}, ErrUnavailable
	}
	if i != len(c.Units) {
		return pickupCandidate{}, ErrStoredData
	}
	return c, nil
}
func (p *PickupStore) material(ctx context.Context, tx pgx.Tx, local LocalContext, s Snapshot, g authorityGuard) (DraftQuote, []SummaryPreviewUnit, error) {
	if err := p.cart.guardUnresolvedEdits(ctx, tx, local, s.ID); err != nil {
		return DraftQuote{}, nil, err
	}
	f, q, e := p.cart.loadDraftQuote(ctx, tx, local, s, nil, g)
	if e != nil {
		return DraftQuote{}, nil, e
	}
	if f.kind != "pickup" || len(s.Units) == 0 {
		return DraftQuote{}, nil, ErrPickupSummaryRequired
	}
	var open bool
	if e = tx.QueryRow(ctx, `SELECT open FROM app.restaurant_settings WHERE restaurant_id=$1 FOR SHARE`, p.cart.restaurant).Scan(&open); e != nil {
		return DraftQuote{}, nil, ErrUnavailable
	}
	if !open {
		return DraftQuote{}, nil, ErrPickupSummaryRequired
	}
	var pending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.native_cart_actions WHERE restaurant_id=$1 AND conversation_id=$2 AND committed_at IS NULL)`, p.cart.restaurant, local.conversation.value).Scan(&pending); e != nil {
		return DraftQuote{}, nil, ErrUnavailable
	}
	if pending {
		return DraftQuote{}, nil, ErrPickupSummaryRequired
	}
	// Same deterministic menu ordering as immutable previews, with stronger current
	// availability/price checks. No reprice of already selected unit terms.
	ids := make([]MenuID, 0)
	seen := map[MenuID]bool{}
	for _, u := range s.Units {
		if !seen[u.MenuID] {
			ids = append(ids, u.MenuID)
			seen[u.MenuID] = true
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].value < ids[j].value })
	type menuTerms struct {
		name      string
		price     int64
		currency  string
		available bool
	}
	menus := map[MenuID]menuTerms{}
	for _, id := range ids {
		var m menuTerms
		if e = tx.QueryRow(ctx, `SELECT name,price_minor,currency,available FROM app.menu_items WHERE restaurant_id=$1 AND id=$2 FOR SHARE`, p.cart.restaurant, id.value).Scan(&m.name, &m.price, &m.currency, &m.available); e != nil {
			return DraftQuote{}, nil, unavailable(e)
		}
		if !text(m.name, 200, true) || m.price < 0 {
			return DraftQuote{}, nil, ErrStoredData
		}
		menus[id] = m
	}
	units := make([]SummaryPreviewUnit, 0, len(s.Units))
	for _, u := range s.Units {
		m := menus[u.MenuID]
		if !m.available || m.price != u.PriceMinor || m.currency != u.Currency {
			return DraftQuote{}, nil, ErrPickupSummaryRequired
		}
		units = append(units, SummaryPreviewUnit{Unit: u, Name: m.name})
	}
	return q, units, nil
}
func (p *PickupStore) workload(ctx context.Context, tx pgx.Tx) (*PickupEstimate, error) {
	rows, e := tx.Query(ctx, `SELECT status,queue_sequence,preparation_started_at FROM app.pickup_orders WHERE restaurant_id=$1 AND status IN ('accepted','preparing') ORDER BY queue_sequence FOR SHARE`, p.cart.restaurant)
	if e != nil {
		return nil, ErrUnavailable
	}
	entries := []preparation.Entry{}
	for rows.Next() {
		var state preparation.State
		var seq int64
		var started *time.Time
		if e = rows.Scan(&state, &seq, &started); e != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		entry := preparation.Entry{State: state, Sequence: seq}
		if started != nil {
			entry.StartedAt = *started
		}
		entries = append(entries, entry)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	now, e := nativeClock(ctx, tx)
	if e != nil {
		return nil, e
	}
	estimate, e := preparation.Calculate(now, entries)
	if e != nil {
		return nil, ErrStoredData
	}
	if estimate == nil {
		return nil, nil
	}
	return &PickupEstimate{Minutes: estimate.Minutes, AsOf: estimate.AsOf, Provisional: estimate.Provisional}, nil
}
func (p *PickupStore) Prepare(ctx context.Context, x PickupExecution, id CartID, v Version) (PreparedPickupSummary, error) {
	if !validID(id.value) || v.value < 0 {
		return PreparedPickupSummary{}, ErrInput
	}
	tx, call, g, e := p.begin(ctx, x, true)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	defer rollbackNative(tx)
	expected, e := PreparePickupCall(id, v)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	a, _ := encodeCall(call)
	b, _ := encodeCall(expected)
	if !bytes.Equal(a, b) {
		return PreparedPickupSummary{}, ErrConflict
	}
	body, found, e := p.ledger(ctx, tx, x, call)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	if found {
		var out PreparedPickupSummary
		if pickupDecode(body, &out) != nil {
			return PreparedPickupSummary{}, ErrStoredData
		}
		c, e := p.candidate(ctx, tx, x.Local, out.CandidateID)
		if e != nil {
			return PreparedPickupSummary{}, e
		}
		encoded, _ := pickupJSON(c.Prepared)
		if string(encoded) != body {
			return PreparedPickupSummary{}, ErrStoredData
		}
		if e = g(); e != nil {
			return PreparedPickupSummary{}, e
		}
		return out, nil
	}
	s, e := p.cart.read(ctx, tx, x.Local, id)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	if s.Version != v {
		return PreparedPickupSummary{}, ErrStale
	}
	var submitted bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.pickup_orders WHERE restaurant_id=$1 AND cart_id=$2)`, p.cart.restaurant, id.value).Scan(&submitted); e != nil {
		return PreparedPickupSummary{}, ErrUnavailable
	}
	if submitted {
		return PreparedPickupSummary{}, ErrPickupPolicyUnresolved
	}
	q, units, e := p.material(ctx, tx, x.Local, s, g)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	estimate, e := p.workload(ctx, tx)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	candidate, e := newID[pickupCandidateKind]()
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	intent, e := newID[pickupIntentKind]()
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	now, e := nativeClock(ctx, tx)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	c := pickupCandidate{Snapshot: s, Quote: q, Units: units, Control: x.ControlVersion, Operation: x.Execution.operation, Created: now.UTC()}
	c.Prepared = PreparedPickupSummary{CandidateID: candidate, CartID: id, CartVersion: v, UnitCount: len(units), Total: pickupMoney(s, q.Total), Estimate: estimate, ClosingQuestion: pickupQuestion, Intent: PickupSummaryIntent{ID: intent, CandidateID: candidate, ConversationID: x.Local.conversation}}
	c.Prepared.Intent.Text = pickupText(c)
	c.Prepared.Intent.TextDigest = sha256.Sum256([]byte(c.Prepared.Intent.Text))
	if e = validatePickupCandidate(c); e != nil {
		return PreparedPickupSummary{}, e
	}
	stored, e := pickupJSON(c)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	result, e := pickupJSON(c.Prepared)
	if e != nil {
		return PreparedPickupSummary{}, e
	}
	if e = g(); e != nil {
		return PreparedPickupSummary{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_candidates(id,restaurant_id,conversation_id,cart_id,cart_version,control_version,operation_id,created_at,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, candidate.value, p.cart.restaurant, x.Local.conversation.value, id.value, v.value, x.ControlVersion.value, x.Execution.operation.value, now, string(stored)); e != nil {
		return PreparedPickupSummary{}, ErrUnavailable
	}
	for i, u := range units {
		if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_candidate_units(candidate_id,position,unit_id,menu_id,name,note,price_minor) VALUES($1,$2,$3,$4,$5,$6,$7)`, candidate.value, i, u.ID.value, u.MenuID.value, u.Name, u.Note, u.PriceMinor); e != nil {
			return PreparedPickupSummary{}, ErrUnavailable
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_intents(id,restaurant_id,conversation_id,candidate_id,kind,content,created_at) VALUES($1,$2,$3,$4,'summary',$5,$6)`, intent.value, p.cart.restaurant, x.Local.conversation.value, candidate.value, c.Prepared.Intent.Text, now); e != nil {
		return PreparedPickupSummary{}, ErrUnavailable
	}
	hash := sha256.Sum256(b)
	if e = p.cart.commitOperation(ctx, tx, x.Local, x.Execution.operation, id, pickupPrepareAction, hash, result, g); e != nil {
		return PreparedPickupSummary{}, e
	}
	return c.Prepared, nil
}
func (p *PickupStore) RecordLocalPresentation(ctx context.Context, local LocalContext, in PickupLocalPresentation) (PickupPresentation, error) {
	if ctx == nil || !validID(in.IntentID.value) || !validID(in.CandidateID.value) || !text(in.ReceiverReference, 256, true) || len(in.ReceiverReference) > 256 {
		return PickupPresentation{}, ErrInput
	}
	tx, e := p.cart.begin(ctx, local)
	if e != nil {
		return PickupPresentation{}, e
	}
	defer rollbackNative(tx)
	c, e := p.candidate(ctx, tx, local, in.CandidateID)
	if e != nil {
		return PickupPresentation{}, e
	}
	if c.Prepared.Intent.ID != in.IntentID || c.Prepared.Intent.TextDigest != in.ReceivedTextDigest {
		return PickupPresentation{}, ErrConflict
	}
	var content string
	if e = tx.QueryRow(ctx, `SELECT content FROM app.pickup_intents WHERE id=$1 AND candidate_id=$2 AND restaurant_id=$3 AND conversation_id=$4 AND kind='summary' FOR SHARE`, in.IntentID.value, in.CandidateID.value, p.cart.restaurant, local.conversation.value).Scan(&content); e != nil {
		return PickupPresentation{}, unavailable(e)
	}
	if content != c.Prepared.Intent.Text {
		return PickupPresentation{}, ErrStoredData
	}
	var out PickupPresentation
	var id, ref string
	var digest []byte
	e = tx.QueryRow(ctx, `SELECT id::text,receiver_reference,text_digest,received_at FROM app.pickup_presentations WHERE candidate_id=$1 FOR SHARE`, in.CandidateID.value).Scan(&id, &ref, &digest, &out.ReceivedAt)
	if e == nil {
		if ref != in.ReceiverReference || !bytes.Equal(digest, in.ReceivedTextDigest[:]) {
			return PickupPresentation{}, ErrConflict
		}
		out.ID = PickupPresentationID{id}
		out.IntentID = in.IntentID
		out.ReceivedAt = out.ReceivedAt.UTC()
		return out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return PickupPresentation{}, ErrUnavailable
	}
	// A late callback cannot resurrect an obsolete summary or control epoch.
	s, e := p.cart.read(ctx, tx, local, c.Snapshot.ID)
	if e != nil {
		return PickupPresentation{}, e
	}
	if s.Version != c.Snapshot.Version {
		return PickupPresentation{}, ErrPickupSummaryRequired
	}
	var control int64
	if e = tx.QueryRow(ctx, `SELECT version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, p.cart.restaurant, local.conversation.value).Scan(&control); e != nil {
		return PickupPresentation{}, unavailable(e)
	}
	if control != c.Control.value {
		return PickupPresentation{}, ErrStale
	}
	pid, e := newID[pickupPresentationKind]()
	if e != nil {
		return PickupPresentation{}, e
	}
	var received time.Time
	e = tx.QueryRow(ctx, `INSERT INTO app.pickup_presentations(id,candidate_id,intent_id,receiver_reference,text_digest,received_at,after_sequence) SELECT $1,$2,$3,$4,$5,clock_timestamp(),max(sequence) FROM app.inbound_events WHERE sender=$6 RETURNING received_at`, pid.value, in.CandidateID.value, in.IntentID.value, in.ReceiverReference, in.ReceivedTextDigest[:], local.sender).Scan(&received)
	if e != nil {
		return PickupPresentation{}, ErrConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return PickupPresentation{}, ErrUnavailable
	}
	return PickupPresentation{ID: pid, IntentID: in.IntentID, ReceivedAt: received.UTC()}, nil
}

func (p *PickupStore) completion(ctx context.Context, tx pgx.Tx, x PickupExecution, g authorityGuard) (PickupCompletionContext, pickupCandidate, error) {
	var id string
	e := tx.QueryRow(ctx, `SELECT c.id::text FROM app.pickup_candidates c JOIN app.pickup_presentations r ON r.candidate_id=c.id WHERE c.restaurant_id=$1 AND c.conversation_id=$2 ORDER BY r.received_at DESC,r.id DESC LIMIT 1 FOR SHARE OF c,r`, p.cart.restaurant, x.Local.conversation.value).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return PickupCompletionContext{}, pickupCandidate{}, ErrPickupSummaryRequired
	}
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	c, e := p.candidate(ctx, tx, x.Local, PickupCandidateID{id})
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, e
	}
	s, e := p.cart.read(ctx, tx, x.Local, c.Snapshot.ID)
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, e
	}
	if s.Version != c.Snapshot.Version {
		return PickupCompletionContext{}, pickupCandidate{}, ErrPickupSummaryRequired
	}
	q, units, e := p.material(ctx, tx, x.Local, s, g)
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, e
	}
	left, _ := json.Marshal(units)
	right, _ := json.Marshal(c.Units)
	if q != c.Quote || !bytes.Equal(left, right) {
		return PickupCompletionContext{}, pickupCandidate{}, ErrPickupSummaryRequired
	}
	out := PickupCompletionContext{CandidateID: c.Prepared.CandidateID, CartID: s.ID, CartVersion: s.Version, ControlVersion: x.ControlVersion, SummaryText: c.Prepared.Intent.Text, ClosingQuestion: pickupQuestion, IntentID: c.Prepared.Intent.ID, History: []PickupContextMessage{}}
	var base, current int64
	var pid string
	if e = tx.QueryRow(ctx, `SELECT id::text,after_sequence FROM app.pickup_presentations WHERE candidate_id=$1`, id).Scan(&pid, &base); e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	out.PresentationID = PickupPresentationID{pid}
	if e = tx.QueryRow(ctx, `SELECT id::text,content,sequence FROM app.inbound_events WHERE id=$1 AND sender=$2 FOR SHARE`, x.Source.eventUUID, x.Local.sender).Scan(&out.InboundEventID, &out.InboundText, &current); e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, unavailable(e)
	}
	if current <= base {
		return PickupCompletionContext{}, pickupCandidate{}, ErrPickupSummaryRequired
	}
	// Serialize against actual inbound allocation as well as scoped cart writes.
	// A newer unresolved inbound makes this an obsolete decision window. Replay
	// of an already committed operation is handled before this freshness check.
	var allocated int64
	if e = tx.QueryRow(ctx, `SELECT next_sequence FROM app.conversation_sequences WHERE sender=$1 FOR UPDATE`, x.Local.sender).Scan(&allocated); e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	if allocated != current {
		return PickupCompletionContext{}, pickupCandidate{}, ErrStale
	}
	// Every intervening customer turn must have the same actual source binding.
	// Missing scope is unavailable, not an empty or semantically filtered history.
	rows, e := tx.Query(ctx, `SELECT e.id::text,e.content,e.sequence,s.restaurant_id::text,s.conversation_id::text FROM app.inbound_events e LEFT JOIN app.cart_execution_sources s ON s.event_id=e.id WHERE e.sender=$1 AND e.sequence>$2 AND e.sequence<$3 ORDER BY e.sequence`, x.Local.sender, base, current)
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	type turn struct {
		seq   int64
		reply bool
		stamp time.Time
		m     PickupContextMessage
	}
	turns := []turn{}
	for rows.Next() {
		var eid, content string
		var sequence int64
		var restaurant, conversation *string
		if e = rows.Scan(&eid, &content, &sequence, &restaurant, &conversation); e != nil {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
		}
		if restaurant == nil || conversation == nil || *restaurant != p.cart.restaurant || *conversation != x.Local.conversation.value {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
		}
		turns = append(turns, turn{seq: sequence, m: PickupContextMessage{SourceID: eid, Speaker: PickupSpeakerCustomer, Text: content}})
		if len(turns) > PickupCompletionHistoryLimit {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrLimit
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	// Only actual LOCAL receiver acknowledgments contribute restaurant turns.
	rows, e = tx.Query(ctx, `SELECT r.outbox_id::text,o.content,r.after_sequence,r.received_at,r.text_digest FROM app.pickup_local_replies r JOIN app.outbox o ON o.id=r.outbox_id WHERE r.restaurant_id=$1 AND r.conversation_id=$2 AND r.after_sequence>=$3 AND r.after_sequence<$4 AND r.received_at>(SELECT received_at FROM app.pickup_presentations WHERE candidate_id=$5) ORDER BY r.after_sequence,r.received_at,r.outbox_id`, p.cart.restaurant, x.Local.conversation.value, base, current, id)
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	for rows.Next() {
		var t turn
		var oid, content string
		var digest []byte
		if e = rows.Scan(&oid, &content, &t.seq, &t.stamp, &digest); e != nil {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
		}
		expectedDigest := sha256.Sum256([]byte(content))
		if !bytes.Equal(digest, expectedDigest[:]) {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrStoredData
		}
		t.reply = true
		t.m = PickupContextMessage{SourceID: oid, Speaker: PickupSpeakerRestaurant, Text: content}
		turns = append(turns, t)
		if len(turns) > PickupCompletionHistoryLimit {
			rows.Close()
			return PickupCompletionContext{}, pickupCandidate{}, ErrLimit
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return PickupCompletionContext{}, pickupCandidate{}, ErrUnavailable
	}
	sort.SliceStable(turns, func(i, j int) bool {
		if turns[i].seq != turns[j].seq {
			return turns[i].seq < turns[j].seq
		}
		if turns[i].reply != turns[j].reply {
			return !turns[i].reply
		}
		if !turns[i].stamp.Equal(turns[j].stamp) {
			return turns[i].stamp.Before(turns[j].stamp)
		}
		return turns[i].m.SourceID < turns[j].m.SourceID
	})
	for _, t := range turns {
		if !text(t.m.Text, 4000, true) {
			return PickupCompletionContext{}, pickupCandidate{}, ErrStoredData
		}
		out.History = append(out.History, t.m)
	}
	// Match exactly the portable AI input's JSON size, not this larger authority DTO.
	input := struct {
		SummaryText     string                 `json:"summary_text"`
		ClosingQuestion string                 `json:"closing_question"`
		InboundText     string                 `json:"inbound_text"`
		History         []PickupContextMessage `json:"history"`
	}{out.SummaryText, out.ClosingQuestion, out.InboundText, out.History}
	encoded, e := json.Marshal(input)
	if e != nil || len(encoded) > PickupCompletionInputByteLimit {
		return PickupCompletionContext{}, pickupCandidate{}, ErrLimit
	}
	bound := struct {
		Context                                    PickupCompletionContext
		Restaurant, Conversation, Business, Sender string
		CandidateControl                           Version
	}{out, p.cart.restaurant, x.Local.conversation.value, x.Local.business, x.Local.sender, c.Control}
	encoded, e = pickupJSON(bound)
	if e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, e
	}
	out.Digest = sha256.Sum256(encoded)
	if e = g(); e != nil {
		return PickupCompletionContext{}, pickupCandidate{}, e
	}
	return out, c, nil
}
func (p *PickupStore) CompletionContext(ctx context.Context, x PickupExecution) (PickupCompletionContext, error) {
	tx, _, g, e := p.begin(ctx, x, false)
	if e != nil {
		return PickupCompletionContext{}, e
	}
	defer rollbackNative(tx)
	out, _, e := p.completion(ctx, tx, x, g)
	return out, e
}
func (p *PickupStore) Submit(ctx context.Context, x PickupExecution, in PickupCompletionEvidence) (PickupSubmission, error) {
	tx, call, g, e := p.begin(ctx, x, true)
	if e != nil {
		return PickupSubmission{}, e
	}
	defer rollbackNative(tx)
	expected, e := SubmitPickupCall(in)
	if e != nil {
		return PickupSubmission{}, e
	}
	a, _ := encodeCall(call)
	b, _ := encodeCall(expected)
	if !bytes.Equal(a, b) {
		return PickupSubmission{}, ErrConflict
	}
	if in.AssessmentVersion != p.assessment || in.Decision != PickupComplete {
		return PickupSubmission{}, ErrPickupConfirmationRequired
	}
	body, found, e := p.ledger(ctx, tx, x, call)
	if e != nil {
		return PickupSubmission{}, e
	}
	if found {
		var out PickupSubmission
		if pickupDecode(body, &out) != nil || out.OperationID != x.Execution.operation || validatePickupOrder(out.Order) != nil {
			return PickupSubmission{}, ErrStoredData
		}
		c, e := p.candidate(ctx, tx, x.Local, in.CandidateID)
		if e != nil {
			return PickupSubmission{}, e
		}
		var orderID, receiptID, receiptText string
		var digest []byte
		e = tx.QueryRow(ctx, `SELECT o.id::text,i.id::text,i.content,e.context_digest FROM app.pickup_orders o JOIN app.pickup_evidence e ON e.order_id=o.id JOIN app.pickup_intents i ON i.order_id=o.id AND i.kind='receipt' WHERE o.restaurant_id=$1 AND o.conversation_id=$2 AND o.operation_id=$3 AND o.candidate_id=$4 AND o.cart_id=$5 AND e.event_id=$6 AND e.assessment_version=$7`, p.cart.restaurant, x.Local.conversation.value, x.Execution.operation.value, in.CandidateID.value, c.Snapshot.ID.value, in.InboundEventID, p.assessment).Scan(&orderID, &receiptID, &receiptText, &digest)
		if errors.Is(e, pgx.ErrNoRows) {
			return PickupSubmission{}, ErrStoredData
		}
		if e != nil {
			return PickupSubmission{}, ErrUnavailable
		}
		if out.Order.ID.value != orderID || out.Order.Confirmed.ID != in.CandidateID || out.Order.Total != c.Prepared.Total || out.Order.Confirmed.UnitCount != c.Prepared.UnitCount || out.Order.Status != PickupSubmitted || out.Order.Payment.State != PickupUnpaid || out.Order.Version.value != 0 || out.Receipt.ID.value != receiptID || out.Receipt.OrderID != out.Order.ID || out.Receipt.Text != receiptText || !bytes.Equal(digest, in.ContextDigest[:]) {
			return PickupSubmission{}, ErrStoredData
		}
		if e = g(); e != nil {
			return PickupSubmission{}, e
		}
		return out, nil
	}
	context, c, e := p.completion(ctx, tx, x, g)
	if e != nil {
		return PickupSubmission{}, e
	}
	if context.CandidateID != in.CandidateID || context.InboundEventID != in.InboundEventID || context.Digest != in.ContextDigest {
		return PickupSubmission{}, ErrPickupConfirmationRequired
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.pickup_orders WHERE cart_id=$1 OR candidate_id=$2)`, c.Snapshot.ID.value, in.CandidateID.value).Scan(&exists); e != nil {
		return PickupSubmission{}, ErrUnavailable
	}
	if exists {
		return PickupSubmission{}, ErrConflict
	}
	estimate, e := p.workload(ctx, tx)
	if e != nil {
		return PickupSubmission{}, e
	}
	oid, e := newID[pickupOrderKind]()
	if e != nil {
		return PickupSubmission{}, e
	}
	rid, e := newID[pickupIntentKind]()
	if e != nil {
		return PickupSubmission{}, e
	}
	now, e := nativeClock(ctx, tx)
	if e != nil {
		return PickupSubmission{}, e
	}
	// Refresh only provisional workload in the receipt, never confirmed terms,
	// and do not ask a second anything-else question after completion.
	receiptCandidate := c
	receiptCandidate.Prepared.Estimate = estimate
	receiptText := strings.TrimSuffix(pickupText(receiptCandidate), "\n"+pickupQuestion)
	receiptText = strings.Replace(receiptText, "Pickup summary (not submitted)", "Pickup order submitted (unpaid)", 1)
	out := PickupSubmission{OperationID: x.Execution.operation, Order: PickupOrder{PickupOrderHeader: PickupOrderHeader{ID: oid, Version: Version{0}, CreatedAt: now.UTC(), Status: PickupSubmitted, Payment: PickupPayment{State: PickupUnpaid}, Total: c.Prepared.Total}, Confirmed: PickupConfirmedSnapshot{ID: in.CandidateID, UnitCount: c.Prepared.UnitCount, Subtotal: c.Prepared.Total, DeliveryFee: pickupMoney(c.Snapshot, 0), Total: c.Prepared.Total}, Estimate: estimate}, Receipt: PickupReceiptIntent{ID: rid, OrderID: oid, Text: receiptText}}
	out.Order.PermittedActions = pickupActions(out.Order)
	if e = validatePickupOrder(out.Order); e != nil {
		return PickupSubmission{}, e
	}
	result, e := pickupJSON(out)
	if e != nil {
		return PickupSubmission{}, e
	}
	evidence, e := pickupJSON(context)
	if e != nil {
		return PickupSubmission{}, e
	}
	est, _ := json.Marshal(estimate)
	if e = g(); e != nil {
		return PickupSubmission{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_orders(id,restaurant_id,conversation_id,cart_id,candidate_id,operation_id,created_at,version,status,payment,estimate) VALUES($1,$2,$3,$4,$5,$6,$7,0,'submitted','unpaid',$8)`, oid.value, p.cart.restaurant, x.Local.conversation.value, c.Snapshot.ID.value, in.CandidateID.value, x.Execution.operation.value, now, string(est)); e != nil {
		return PickupSubmission{}, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_evidence(event_id,restaurant_id,conversation_id,candidate_id,order_id,presentation_id,assessment_version,context_digest,context_body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.InboundEventID, p.cart.restaurant, x.Local.conversation.value, in.CandidateID.value, oid.value, context.PresentationID.value, p.assessment, in.ContextDigest[:], string(evidence)); e != nil {
		return PickupSubmission{}, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_intents(id,restaurant_id,conversation_id,candidate_id,order_id,kind,content,created_at) VALUES($1,$2,$3,$4,$5,'receipt',$6,$7)`, rid.value, p.cart.restaurant, x.Local.conversation.value, in.CandidateID.value, oid.value, out.Receipt.Text, now); e != nil {
		return PickupSubmission{}, ErrUnavailable
	}
	hash := sha256.Sum256(b)
	if e = p.cart.commitOperation(ctx, tx, x.Local, x.Execution.operation, c.Snapshot.ID, pickupSubmitAction, hash, result, g); e != nil {
		return PickupSubmission{}, e
	}
	return out, nil
}

package cart

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func pickupActions(o PickupOrder) []PickupStaffAction {
	a := []PickupStaffAction{}
	switch o.Status {
	case PickupSubmitted:
		a = append(a, PickupAccept)
		if o.Payment.State == PickupUnpaid {
			a = append(a, PickupReject, PickupCancel)
		}
	case PickupAccepted:
		a = append(a, PickupStartPreparing)
	case PickupPreparing:
		a = append(a, PickupMarkReady)
	case PickupReady:
		a = append(a, PickupMarkPickedUp)
	}
	if o.Payment.State == PickupUnpaid && o.Status != PickupRejected && o.Status != PickupCancelled {
		a = append(a, PickupMarkPaid)
	}
	return a
}
func validatePickupOrder(o PickupOrder) error {
	if !validID(o.ID.value) || !validID(o.Confirmed.ID.value) || o.Version.value < 0 || o.CreatedAt.IsZero() || o.CreatedAt.Location() != time.UTC || o.CreatedAt.Year() < 1 || o.CreatedAt.Year() > 9999 || o.Total.AmountMinor < 0 || !currency(o.Total.Currency) || o.Total.Exponent < 0 || o.Total.Exponent > 6 || o.Confirmed.UnitCount < 1 || o.Confirmed.Total != o.Total || o.Confirmed.Subtotal != o.Total || o.Confirmed.DeliveryFee != (PickupMoney{Currency: o.Total.Currency, Exponent: o.Total.Exponent}) || !validPickupEstimate(o.Estimate) {
		return ErrStoredData
	}
	switch o.Status {
	case PickupSubmitted, PickupAccepted, PickupRejected, PickupCancelled:
		if o.PreparationStartedAt != nil {
			return ErrStoredData
		}
	case PickupPreparing, PickupReady, PickupPickedUp:
		if o.PreparationStartedAt == nil || o.PreparationStartedAt.IsZero() {
			return ErrStoredData
		}
	default:
		return ErrStoredData
	}
	switch o.Payment.State {
	case PickupUnpaid:
		if o.Payment.CollectedAt != nil {
			return ErrStoredData
		}
	case PickupPaid:
		if o.Payment.CollectedAt == nil || o.Payment.CollectedAt.IsZero() {
			return ErrStoredData
		}
	default:
		return ErrStoredData
	}
	if o.PermittedActions == nil {
		return ErrStoredData
	}
	return nil
}
func (p *PickupStore) staffBegin(ctx context.Context, subject PickupStaffSubject) (pgx.Tx, error) {
	if ctx == nil || p == nil || p.cart == nil || !text(string(subject), 256, true) || len(subject) > 256 {
		return nil, ErrInput
	}
	tx, e := p.cart.pool.Begin(ctx)
	if e != nil {
		return nil, ErrUnavailable
	}
	fail := func(e error) (pgx.Tx, error) { rollbackNative(tx); return nil, e }
	var id string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM app.restaurants WHERE id=$1 FOR UPDATE`, p.cart.restaurant).Scan(&id); e != nil {
		return fail(unavailable(e))
	}
	var active bool
	e = tx.QueryRow(ctx, `SELECT active FROM app.staff_memberships WHERE restaurant_id=$1 AND auth_user_id=$2 FOR SHARE`, p.cart.restaurant, string(subject)).Scan(&active)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !active {
		return fail(ErrPickupForbidden)
	}
	if e != nil {
		return fail(ErrUnavailable)
	}
	return tx, nil
}
func (p *PickupStore) order(ctx context.Context, tx pgx.Tx, id PickupOrderID) (PickupOrder, LocalContext, error) {
	var o PickupOrder
	var candidate, conversation, business, sender, estimate string
	var v int64
	e := tx.QueryRow(ctx, `SELECT o.version,o.created_at,o.status,o.payment,o.collected_at,o.preparation_started_at,o.candidate_id::text,o.conversation_id::text,c.business,c.sender,o.estimate FROM app.pickup_orders o JOIN app.cart_conversations c ON c.restaurant_id=o.restaurant_id AND c.id=o.conversation_id WHERE o.restaurant_id=$1 AND o.id=$2 FOR UPDATE OF o FOR SHARE OF c`, p.cart.restaurant, id.value).Scan(&v, &o.CreatedAt, &o.Status, &o.Payment.State, &o.Payment.CollectedAt, &o.PreparationStartedAt, &candidate, &conversation, &business, &sender, &estimate)
	if e != nil {
		return PickupOrder{}, LocalContext{}, unavailable(e)
	}
	local := LocalContext{restaurant: p.cart.restaurant, conversation: ConversationID{conversation}, business: business, sender: sender}
	c, e := p.candidate(ctx, tx, local, PickupCandidateID{candidate})
	if e != nil {
		return PickupOrder{}, LocalContext{}, e
	}
	o.ID = id
	o.Version = Version{v}
	o.CreatedAt = o.CreatedAt.UTC()
	if o.Payment.CollectedAt != nil {
		t := o.Payment.CollectedAt.UTC()
		o.Payment.CollectedAt = &t
	}
	if o.PreparationStartedAt != nil {
		t := o.PreparationStartedAt.UTC()
		o.PreparationStartedAt = &t
	}
	if json.Unmarshal([]byte(estimate), &o.Estimate) != nil {
		return PickupOrder{}, LocalContext{}, ErrStoredData
	}
	if o.Estimate != nil {
		o.Estimate.AsOf = o.Estimate.AsOf.UTC()
	}
	o.Total = c.Prepared.Total
	o.Confirmed = PickupConfirmedSnapshot{ID: c.Prepared.CandidateID, UnitCount: c.Prepared.UnitCount, Subtotal: c.Prepared.Total, DeliveryFee: pickupMoney(c.Snapshot, 0), Total: c.Prepared.Total}
	o.PermittedActions = pickupActions(o)
	if e = validatePickupOrder(o); e != nil {
		return PickupOrder{}, LocalContext{}, e
	}
	return o, local, nil
}
func (p *PickupStore) ReadOrder(ctx context.Context, subject PickupStaffSubject, id PickupOrderID) (PickupOrder, error) {
	if !validID(id.value) {
		return PickupOrder{}, ErrInput
	}
	tx, e := p.staffBegin(ctx, subject)
	if e != nil {
		return PickupOrder{}, e
	}
	defer rollbackNative(tx)
	o, _, e := p.order(ctx, tx, id)
	if e != nil {
		return PickupOrder{}, e
	}
	if _, e = pickupJSON(o); e != nil {
		return PickupOrder{}, e
	}
	if e = sourceContextError(ctx); e != nil {
		return PickupOrder{}, e
	}
	return o, nil
}

type pickupCursor struct {
	Restaurant string
	Subject    PickupStaffSubject
	Resource   string
	After      string
	Created    time.Time
	Position   int
}

func pickupLimit(n int) (int, error) {
	if n == 0 {
		return 50, nil
	}
	if n < 1 || n > 100 {
		return 0, ErrInput
	}
	return n, nil
}
func (p *PickupStore) encodeCursor(c pickupCursor) string {
	body, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, p.cursorKey[:])
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (p *PickupStore) decodeCursor(raw string, subject PickupStaffSubject, resource string) (pickupCursor, error) {
	zero := pickupCursor{Restaurant: p.cart.restaurant, Subject: subject, Resource: resource}
	if raw == "" {
		return zero, nil
	}
	if len(raw) > 2048 {
		return pickupCursor{}, ErrInput
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return pickupCursor{}, ErrInput
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return pickupCursor{}, ErrInput
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return pickupCursor{}, ErrInput
	}
	mac := hmac.New(sha256.New, p.cursorKey[:])
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return pickupCursor{}, ErrInput
	}
	var c pickupCursor
	if json.Unmarshal(b, &c) != nil || c.Restaurant != p.cart.restaurant || c.Subject != subject || c.Resource != resource || p.encodeCursor(c) != raw {
		return pickupCursor{}, ErrInput
	}
	return c, nil
}
func (p *PickupStore) ListOrders(ctx context.Context, subject PickupStaffSubject, in PickupPageRequest) (PickupOrderPage, error) {
	limit, e := pickupLimit(in.Limit)
	if e != nil {
		return PickupOrderPage{}, e
	}
	tx, e := p.staffBegin(ctx, subject)
	if e != nil {
		return PickupOrderPage{}, e
	}
	defer rollbackNative(tx)
	pos, e := p.decodeCursor(in.Cursor, subject, "/v1/staff/orders")
	if e != nil {
		return PickupOrderPage{}, e
	}
	var after, created any
	if in.Cursor != "" {
		if !validID(pos.After) || pos.Created.IsZero() || pos.Position != 0 {
			return PickupOrderPage{}, ErrInput
		}
		after = pos.After
		created = pos.Created
	}
	rows, e := tx.Query(ctx, `SELECT id::text,created_at FROM app.pickup_orders WHERE restaurant_id=$1 AND ($2::timestamptz IS NULL OR (created_at,id)>($2::timestamptz,$3::uuid)) ORDER BY created_at,id LIMIT $4`, p.cart.restaurant, created, after, limit+1)
	if e != nil {
		return PickupOrderPage{}, ErrUnavailable
	}
	type pointer struct {
		id      PickupOrderID
		created time.Time
	}
	ids := []pointer{}
	for rows.Next() {
		var raw string
		var t time.Time
		if e = rows.Scan(&raw, &t); e != nil {
			rows.Close()
			return PickupOrderPage{}, ErrUnavailable
		}
		if !validID(raw) || t.IsZero() {
			rows.Close()
			return PickupOrderPage{}, ErrStoredData
		}
		ids = append(ids, pointer{PickupOrderID{raw}, t.UTC()})
	}
	rows.Close()
	if rows.Err() != nil {
		return PickupOrderPage{}, ErrUnavailable
	}
	out := PickupOrderPage{Items: []PickupOrderHeader{}}
	for i, key := range ids {
		if i == limit {
			last := ids[i-1]
			cursor := p.encodeCursor(pickupCursor{Restaurant: p.cart.restaurant, Subject: subject, Resource: "/v1/staff/orders", After: last.id.value, Created: last.created})
			out.NextCursor = &cursor
			break
		}
		o, _, e := p.order(ctx, tx, key.id)
		if e != nil {
			return PickupOrderPage{}, e
		}
		out.Items = append(out.Items, o.PickupOrderHeader)
	}
	if _, e = pickupJSON(out); e != nil {
		return PickupOrderPage{}, e
	}
	if e = sourceContextError(ctx); e != nil {
		return PickupOrderPage{}, e
	}
	return out, nil
}
func (p *PickupStore) ReadUnits(ctx context.Context, subject PickupStaffSubject, oid PickupOrderID, sid PickupCandidateID, in PickupPageRequest) (PickupOrderUnitPage, error) {
	if !validID(oid.value) || !validID(sid.value) {
		return PickupOrderUnitPage{}, ErrInput
	}
	limit, e := pickupLimit(in.Limit)
	if e != nil {
		return PickupOrderUnitPage{}, e
	}
	tx, e := p.staffBegin(ctx, subject)
	if e != nil {
		return PickupOrderUnitPage{}, e
	}
	defer rollbackNative(tx)
	o, local, e := p.order(ctx, tx, oid)
	if e != nil {
		return PickupOrderUnitPage{}, e
	}
	if o.Confirmed.ID != sid {
		return PickupOrderUnitPage{}, ErrNotFound
	}
	resource := "/v1/staff/orders/" + oid.value + "/summaries/" + sid.value + "/units"
	pos, e := p.decodeCursor(in.Cursor, subject, resource)
	if e != nil {
		return PickupOrderUnitPage{}, e
	}
	c, e := p.candidate(ctx, tx, local, sid)
	if e != nil {
		return PickupOrderUnitPage{}, e
	}
	if pos.Position < 0 || pos.Position >= len(c.Units) || pos.After != "" || !pos.Created.IsZero() {
		return PickupOrderUnitPage{}, ErrInput
	}
	out := PickupOrderUnitPage{Items: []PickupOrderUnit{}, SummaryID: sid, UnitCount: len(c.Units)}
	end := pos.Position + limit
	if end > len(c.Units) {
		end = len(c.Units)
	}
	for _, u := range c.Units[pos.Position:end] {
		out.Items = append(out.Items, PickupOrderUnit{ID: u.ID, MenuID: u.MenuID, Name: u.Name, Note: u.Note, Price: pickupMoney(c.Snapshot, u.PriceMinor)})
	}
	if end < len(c.Units) {
		cursor := p.encodeCursor(pickupCursor{Restaurant: p.cart.restaurant, Subject: subject, Resource: resource, Position: end})
		out.NextCursor = &cursor
	}
	if _, e = pickupJSON(out); e != nil {
		return PickupOrderUnitPage{}, e
	}
	if e = sourceContextError(ctx); e != nil {
		return PickupOrderUnitPage{}, e
	}
	return out, nil
}
func pickupKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range []byte(key) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func validPickupAction(a PickupStaffAction) bool {
	switch a {
	case PickupAccept, PickupReject, PickupStartPreparing, PickupMarkReady, PickupMarkPickedUp, PickupCancel, PickupMarkPaid:
		return true
	}
	return false
}
func (p *PickupStore) ApplyAction(ctx context.Context, subject PickupStaffSubject, id PickupOrderID, in PickupStaffActionRequest) (PickupOrder, error) {
	if !validID(id.value) || !validPickupAction(in.Action) || !pickupKey(in.IdempotencyKey) || in.ExpectedVersion.value < 0 {
		return PickupOrder{}, ErrInput
	}
	tx, e := p.staffBegin(ctx, subject)
	if e != nil {
		return PickupOrder{}, e
	}
	defer rollbackNative(tx)
	resource := "/v1/staff/orders/" + id.value + "/actions"
	canonical, _ := json.Marshal(struct {
		Action   PickupStaffAction
		Expected Version
	}{in.Action, in.ExpectedVersion})
	hash := sha256.Sum256(canonical)
	var previous string
	var saved []byte
	e = tx.QueryRow(ctx, `SELECT fingerprint,result FROM app.pickup_staff_operations WHERE restaurant_id=$1 AND auth_user_id=$2 AND method='POST' AND resource=$3 AND operation_key=$4 FOR SHARE`, p.cart.restaurant, string(subject), resource, in.IdempotencyKey).Scan(&saved, &previous)
	if e == nil {
		if !bytes.Equal(saved, hash[:]) {
			return PickupOrder{}, ErrConflict
		}
		var o PickupOrder
		if pickupDecode(previous, &o) != nil || o.ID != id || validatePickupOrder(o) != nil {
			return PickupOrder{}, ErrStoredData
		}
		if e = sourceContextError(ctx); e != nil {
			return PickupOrder{}, e
		}
		return o, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return PickupOrder{}, ErrUnavailable
	}
	o, local, e := p.order(ctx, tx, id)
	if e != nil {
		return PickupOrder{}, e
	}
	if o.Version != in.ExpectedVersion || o.Version.value == math.MaxInt64 {
		return PickupOrder{}, ErrStale
	}
	legal := false
	for _, a := range pickupActions(o) {
		if a == in.Action {
			legal = true
		}
	}
	if !legal {
		if (in.Action == PickupCancel || in.Action == PickupReject) && (o.Payment.State == PickupPaid || o.Status == PickupAccepted || o.Status == PickupPreparing || o.Status == PickupReady) {
			return PickupOrder{}, ErrPickupPolicyUnresolved
		}
		return PickupOrder{}, ErrPickupInvalidTransition
	}
	now, e := nativeClock(ctx, tx)
	if e != nil {
		return PickupOrder{}, e
	}
	now = now.UTC()
	var sequence any
	switch in.Action {
	case PickupAccept:
		var seq int64
		if e = tx.QueryRow(ctx, `SELECT COALESCE(max(queue_sequence),0) FROM app.pickup_orders WHERE restaurant_id=$1`, p.cart.restaurant).Scan(&seq); e != nil {
			return PickupOrder{}, ErrUnavailable
		}
		if seq == math.MaxInt64 {
			return PickupOrder{}, ErrLimit
		}
		sequence = seq + 1
		o.Status = PickupAccepted
	case PickupReject:
		o.Status = PickupRejected
	case PickupCancel:
		o.Status = PickupCancelled
	case PickupStartPreparing:
		var count int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM app.pickup_orders WHERE restaurant_id=$1 AND status='preparing'`, p.cart.restaurant).Scan(&count); e != nil {
			return PickupOrder{}, ErrUnavailable
		}
		if count >= 3 {
			return PickupOrder{}, ErrPickupInvalidTransition
		}
		o.Status = PickupPreparing
		o.PreparationStartedAt = &now
	case PickupMarkReady:
		o.Status = PickupReady
	case PickupMarkPickedUp:
		o.Status = PickupPickedUp
	case PickupMarkPaid:
		o.Payment.State = PickupPaid
		o.Payment.CollectedAt = &now
	}
	o.Version = Version{o.Version.value + 1}
	o.PermittedActions = pickupActions(o)
	if e = validatePickupOrder(o); e != nil {
		return PickupOrder{}, e
	}
	body, e := pickupJSON(o)
	if e != nil {
		return PickupOrder{}, e
	}
	tag, e := tx.Exec(ctx, `UPDATE app.pickup_orders SET status=$1,payment=$2,collected_at=$3,preparation_started_at=$4,queue_sequence=COALESCE($5,queue_sequence),version=$6 WHERE restaurant_id=$7 AND id=$8 AND version=$9`, string(o.Status), string(o.Payment.State), o.Payment.CollectedAt, o.PreparationStartedAt, sequence, o.Version.value, p.cart.restaurant, id.value, in.ExpectedVersion.value)
	if e != nil {
		return PickupOrder{}, ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return PickupOrder{}, ErrStale
	}
	intent, e := newID[pickupIntentKind]()
	if e != nil {
		return PickupOrder{}, e
	}
	content := "Pickup order " + id.value + ": " + string(o.Status) + "; payment: " + string(o.Payment.State) + "."
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_intents(id,restaurant_id,conversation_id,candidate_id,order_id,kind,content,created_at) VALUES($1,$2,$3,$4,$5,'status',$6,$7)`, intent.value, p.cart.restaurant, local.conversation.value, o.Confirmed.ID.value, id.value, content, now); e != nil {
		return PickupOrder{}, ErrUnavailable
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.pickup_staff_operations(restaurant_id,auth_user_id,method,resource,operation_key,fingerprint,result) VALUES($1,$2,'POST',$3,$4,$5,$6)`, p.cart.restaurant, string(subject), resource, in.IdempotencyKey, hash[:], string(body)); e != nil {
		return PickupOrder{}, ErrUnavailable
	}
	if e = sourceContextError(ctx); e != nil {
		return PickupOrder{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return PickupOrder{}, ErrUnavailable
	}
	return o, nil
}

// PickupLocalIntent is the actual durable LOCAL receiver input, not a diagnostic
// counter or a live transport claim. A receiver must acknowledge the exact text.
type PickupLocalIntent struct {
	ID          PickupIntentID
	CandidateID PickupCandidateID
	OrderID     *PickupOrderID
	Kind        string
	Text        string
}

// ReadLocalIntents returns bounded, scoped durable intents after an optional
// intent ID. It is deliberately not an HTTP or model-facing capability.
func (p *PickupStore) ReadLocalIntents(ctx context.Context, local LocalContext, after PickupIntentID, limit int) ([]PickupLocalIntent, error) {
	if limit < 1 || limit > 100 || after.value != "" && !validID(after.value) {
		return nil, ErrInput
	}
	tx, e := p.cart.begin(ctx, local)
	if e != nil {
		return nil, e
	}
	defer rollbackNative(tx)
	var stamp any
	var raw any
	if after.value != "" {
		var t time.Time
		if e = tx.QueryRow(ctx, `SELECT created_at FROM app.pickup_intents WHERE restaurant_id=$1 AND conversation_id=$2 AND id=$3`, p.cart.restaurant, local.conversation.value, after.value).Scan(&t); e != nil {
			return nil, unavailable(e)
		}
		stamp = t
		raw = after.value
	}
	rows, e := tx.Query(ctx, `SELECT id::text,candidate_id::text,order_id::text,kind,content FROM app.pickup_intents WHERE restaurant_id=$1 AND conversation_id=$2 AND ($3::timestamptz IS NULL OR (created_at,id)>($3::timestamptz,$4::uuid)) ORDER BY created_at,id LIMIT $5`, p.cart.restaurant, local.conversation.value, stamp, raw, limit)
	if e != nil {
		return nil, ErrUnavailable
	}
	out := []PickupLocalIntent{}
	for rows.Next() {
		var id, candidate string
		var oid *string
		var i PickupLocalIntent
		if e = rows.Scan(&id, &candidate, &oid, &i.Kind, &i.Text); e != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		i.ID = PickupIntentID{id}
		i.CandidateID = PickupCandidateID{candidate}
		if oid != nil {
			o := PickupOrderID{*oid}
			i.OrderID = &o
		}
		out = append(out, i)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	if _, e = pickupJSON(out); e != nil {
		return nil, e
	}
	return out, nil
}

// RecordLocalReply records a real fake-receiver callback for an existing exact
// conversational outbox message. Pending/enqueued/provider-accepted alone is NOT
// a presentation. This narrow capability is not a model or staff HTTP method.
func (p *PickupStore) RecordLocalReply(ctx context.Context, local LocalContext, outboxID, receiverReference string, digest [32]byte) error {
	if !validID(outboxID) || !text(receiverReference, 256, true) || len(receiverReference) > 256 {
		return ErrInput
	}
	tx, e := p.cart.begin(ctx, local)
	if e != nil {
		return e
	}
	defer rollbackNative(tx)
	var content string
	e = tx.QueryRow(ctx, `SELECT o.content FROM app.outbox o JOIN app.cart_execution_sources s ON s.event_id=o.event_id WHERE o.id=$1 AND s.restaurant_id=$2 AND s.conversation_id=$3 AND o.kind='ai_reply' AND o.restaurant_id=$2 AND o.conversation_id=$3 FOR SHARE OF o,s`, outboxID, p.cart.restaurant, local.conversation.value).Scan(&content)
	if e != nil {
		return unavailable(e)
	}
	if sha256.Sum256([]byte(content)) != digest {
		return ErrConflict
	}
	var ref string
	var saved []byte
	e = tx.QueryRow(ctx, `SELECT receiver_reference,text_digest FROM app.pickup_local_replies WHERE outbox_id=$1 FOR SHARE`, outboxID).Scan(&ref, &saved)
	if e == nil {
		if ref != receiverReference || !bytes.Equal(saved, digest[:]) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ErrUnavailable
	}
	_, e = tx.Exec(ctx, `INSERT INTO app.pickup_local_replies(outbox_id,restaurant_id,conversation_id,receiver_reference,text_digest,received_at,after_sequence) SELECT $1,$2,$3,$4,$5,clock_timestamp(),max(sequence) FROM app.inbound_events WHERE sender=$6`, outboxID, p.cart.restaurant, local.conversation.value, receiverReference, digest[:], local.sender)
	if e != nil {
		return ErrConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return ErrUnavailable
	}
	return nil
}

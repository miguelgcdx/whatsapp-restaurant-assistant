package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/pricing"
)

type Store struct {
	pool       *pgxpool.Pool
	restaurant string
}

// OpenStore is lazy, guarded, and never migrates, configures or seeds.
func OpenStore(ctx context.Context, dsn, restaurant string) (*Store, error) {
	if !validID(restaurant) {
		return nil, ErrInput
	}
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		return nil, ErrInput
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Store{p, restaurant}, nil
}
func (s *Store) Close() { s.pool.Close() }
func unavailable(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return ErrUnavailable
}

// ProvisionLocal explicitly creates fictional/local ownership; it does not
// create restaurant configuration or establish customer/provider identity.
func (s *Store) ProvisionLocal(ctx context.Context, business, sender string) (LocalContext, error) {
	if !text(business, 256, true) || !text(sender, 256, true) {
		return LocalContext{}, ErrInput
	}
	id, err := newID[conversationKind]()
	if err != nil {
		return LocalContext{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LocalContext{}, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked string
	if err = tx.QueryRow(ctx, `SELECT r.id::text FROM app.restaurants r JOIN app.restaurant_settings s ON s.restaurant_id=r.id WHERE r.id=$1 FOR UPDATE OF r`, s.restaurant).Scan(&locked); err != nil {
		return LocalContext{}, unavailable(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.cart_conversations(id,restaurant_id,source,business,sender,created_at) VALUES($1,$2,'local_fixture',$3,$4,$5)`, id.value, s.restaurant, business, sender, time.Now().UTC())
	if err != nil {
		return LocalContext{}, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return LocalContext{}, ErrUnavailable
	}
	return LocalContext{s.restaurant, id, business, sender}, nil
}

// LoadLocal restores explicit local fixture context after reopen. Its strings
// are application-owned setup data, never trusted customer claims.
func (s *Store) LoadLocal(ctx context.Context, id ConversationID, business, sender string) (LocalContext, error) {
	local := LocalContext{s.restaurant, id, business, sender}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return LocalContext{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return local, nil
}

// Every path, including reads and cached replays, locks and validates fresh
// stored ownership first. Availability is not customer browsing authorization.
func (s *Store) begin(ctx context.Context, local LocalContext) (pgx.Tx, error) {
	if !local.valid() || local.restaurant != s.restaurant {
		return nil, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	fail := func(err error) (pgx.Tx, error) { _ = tx.Rollback(ctx); return nil, err }
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.restaurants WHERE id=$1 FOR UPDATE`, s.restaurant).Scan(&id); err != nil {
		return fail(unavailable(err))
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.cart_conversations WHERE restaurant_id=$1 AND id=$2 AND source='local_fixture' AND business=$3 AND sender=$4 FOR UPDATE`, s.restaurant, local.conversation.value, local.business, local.sender).Scan(&id); err != nil {
		return fail(unavailable(err))
	}
	return tx, nil
}

func (s *Store) Read(ctx context.Context, local LocalContext, id CartID) (Snapshot, error) {
	if !validID(id.value) {
		return Snapshot{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return s.read(ctx, tx, local, id)
}
func (s *Store) read(ctx context.Context, tx pgx.Tx, local LocalContext, id CartID) (Snapshot, error) {
	out := Snapshot{ID: id, ConversationID: local.conversation, Units: []Unit{}}
	var v int64
	err := tx.QueryRow(ctx, `SELECT currency,exponent,version FROM app.carts WHERE restaurant_id=$1 AND conversation_id=$2 AND id=$3 FOR UPDATE`, s.restaurant, local.conversation.value, id.value).Scan(&out.Currency, &out.Exponent, &v)
	if err != nil {
		return Snapshot{}, unavailable(err)
	}
	if v < 0 {
		return Snapshot{}, ErrStoredData
	}
	out.Version = Version{v}
	var count, noteBytes int64
	err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(note)),0)::bigint FROM app.cart_units WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3`, s.restaurant, local.conversation.value, id.value).Scan(&count, &noteBytes)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	if count > resultBudget/72 || noteBytes > resultBudget {
		return Snapshot{}, ErrLimit
	}
	rows, err := tx.Query(ctx, `SELECT id::text,menu_id::text,price_minor,currency,exponent,note FROM app.cart_units WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3 ORDER BY position`, s.restaurant, local.conversation.value, id.value)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var u Unit
		var uid, mid string
		if err = rows.Scan(&uid, &mid, &u.PriceMinor, &u.Currency, &u.Exponent, &u.Note); err != nil {
			return Snapshot{}, ErrUnavailable
		}
		u.ID, err = ParseUnitID(uid)
		if err != nil {
			return Snapshot{}, ErrStoredData
		}
		u.MenuID, err = ParseMenuID(mid)
		if err != nil {
			return Snapshot{}, ErrStoredData
		}
		out.Units = append(out.Units, u)
	}
	if rows.Err() != nil {
		return Snapshot{}, ErrUnavailable
	}
	subtotal, err := validateSnapshot(out)
	if err != nil {
		return Snapshot{}, err
	}
	out.Subtotal = subtotal
	// Include the complete result envelope in the read resource budget as well.
	if _, err = encodeResult(Result{OperationID: OperationID{value: "00000000-0000-4000-8000-000000000000"}, Cart: out}); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

// validateSnapshot uses only persisted prices, never a current catalog lookup.
func validateSnapshot(s Snapshot) (int64, error) {
	if !validID(s.ID.value) || !validID(s.ConversationID.value) || s.Version.value < 0 || !currency(s.Currency) || s.Exponent < 0 || s.Exponent > 6 || s.Units == nil || len(s.Units) > resultBudget/72 {
		return 0, ErrStoredData
	}
	catalog := make(map[MenuID]pricing.Item)
	units := make([]pricing.Unit, 0, len(s.Units))
	for _, u := range s.Units {
		if !validID(u.ID.value) || !validID(u.MenuID.value) || u.PriceMinor < 0 || u.Currency != s.Currency || u.Exponent != s.Exponent || !text(u.Note, 500, false) {
			return 0, ErrStoredData
		}
		item := pricing.Item{ID: u.MenuID.value, Name: "stored snapshot", Currency: u.Currency, Price: u.PriceMinor, Available: true}
		if previous, ok := catalog[u.MenuID]; ok && previous != item {
			return 0, ErrStoredData
		}
		catalog[u.MenuID] = item
		units = append(units, pricing.Unit{ID: u.ID.value, MenuID: u.MenuID.value, Note: u.Note})
	}
	items := make([]pricing.Item, 0, len(catalog))
	for _, i := range catalog {
		items = append(items, i)
	}
	engine, err := pricing.New(pricing.Settings{Currency: s.Currency, Exponent: int(s.Exponent)}, items)
	if err != nil {
		return 0, ErrStoredData
	}
	subtotal, err := engine.Subtotal(units)
	if err != nil {
		return 0, ErrStoredData
	}
	return subtotal, nil
}
func encodeResult(r Result) ([]byte, error) {
	subtotal, err := validateSnapshot(r.Cart)
	if err != nil || subtotal != r.Cart.Subtotal || !validID(r.OperationID.value) {
		return nil, ErrStoredData
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, ErrStoredData
	}
	if len(b) > resultBudget {
		return nil, ErrLimit
	}
	return b, nil
}

// A fixed struct and sorted target set give a canonical semantic request body.
// Exact notes are deliberately not normalized.
type request struct {
	Action   string   `json:"action"`
	Cart     string   `json:"cart"`
	Expected int64    `json:"expected"`
	Menu     string   `json:"menu"`
	Count    int64    `json:"count"`
	Targets  []string `json:"targets"`
	Note     string   `json:"note"`
}

func canonical(id CartID, v Version, a Action) (request, error) {
	r := request{Cart: id.value, Expected: v.value, Targets: []string{}}
	switch a := a.(type) {
	case addAction:
		r.Action = "add"
		r.Menu = a.menu.value
		r.Count = a.count
	case removeAction:
		r.Action = "remove"
		for _, id := range a.units {
			r.Targets = append(r.Targets, id.value)
		}
	case noteAction:
		r.Action = "set_note"
		r.Note = a.note
		for _, id := range a.units {
			r.Targets = append(r.Targets, id.value)
		}
	default:
		return request{}, ErrInput
	}
	sort.Strings(r.Targets)
	return r, nil
}
func fingerprint(r request) [32]byte { b, _ := json.Marshal(r); return sha256.Sum256(b) }
func (s *Store) replay(ctx context.Context, tx pgx.Tx, local LocalContext, op OperationID, r request) (Result, bool, error) {
	var action, body, cartID string
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT action,fingerprint,result,cart_id::text FROM app.cart_operations WHERE restaurant_id=$1 AND conversation_id=$2 AND operation_id=$3`, s.restaurant, local.conversation.value, op.value).Scan(&action, &hash, &body, &cartID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, ErrUnavailable
	}
	expected := fingerprint(r)
	if action != r.Action || !bytes.Equal(hash, expected[:]) {
		return Result{}, false, ErrConflict
	}
	if len(body) == 0 || len(body) > resultBudget {
		return Result{}, false, ErrStoredData
	}
	var out Result
	if json.Unmarshal([]byte(body), &out) != nil {
		return Result{}, false, ErrStoredData
	}
	b, err := encodeResult(out)
	if err != nil || !bytes.Equal(b, []byte(body)) || out.OperationID != op || out.Cart.ConversationID != local.conversation || out.Cart.ID.value != cartID {
		return Result{}, false, ErrStoredData
	}
	if r.Action != "create" && (out.Cart.ID.value != r.Cart || r.Expected == math.MaxInt64 || out.Cart.Version.value != r.Expected+1) {
		return Result{}, false, ErrStoredData
	}
	return out, true, nil
}
func (s *Store) commit(ctx context.Context, tx pgx.Tx, local LocalContext, op OperationID, req request, snapshot Snapshot) (Result, error) {
	result := Result{op, snapshot}
	b, err := encodeResult(result)
	if err != nil {
		return Result{}, err
	}
	hash := fingerprint(req)
	_, err = tx.Exec(ctx, `INSERT INTO app.cart_operations(restaurant_id,conversation_id,operation_id,cart_id,action,fingerprint,result,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.restaurant, local.conversation.value, op.value, snapshot.ID.value, req.Action, hash[:], string(b), time.Now().UTC())
	if err != nil {
		return Result{}, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, ErrUnavailable
	}
	return result, nil
}

// Create serializes on an existing conversation before a draft exists. Another
// creation identity returns the current sole draft; an exact replay returns the
// original complete result, even after later edits.
func (s *Store) Create(ctx context.Context, local LocalContext, op OperationID) (Result, error) {
	if !validID(op.value) {
		return Result{}, ErrInput
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	req := request{Action: "create", Targets: []string{}}
	if previous, ok, err := s.replay(ctx, tx, local, op, req); err != nil || ok {
		return previous, err
	}
	var raw string
	err = tx.QueryRow(ctx, `SELECT id::text FROM app.carts WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		var c string
		var e int16
		if err = tx.QueryRow(ctx, `SELECT currency,currency_exponent FROM app.restaurant_settings WHERE restaurant_id=$1 FOR SHARE`, s.restaurant).Scan(&c, &e); err != nil {
			return Result{}, unavailable(err)
		}
		if !currency(c) || e < 0 || e > 6 {
			return Result{}, ErrStoredData
		}
		id, err := newID[cartKind]()
		if err != nil {
			return Result{}, err
		}
		raw = id.value
		_, err = tx.Exec(ctx, `INSERT INTO app.carts(id,restaurant_id,conversation_id,currency,exponent,version,created_at) VALUES($1,$2,$3,$4,$5,0,$6)`, raw, s.restaurant, local.conversation.value, c, e, time.Now().UTC())
		if err != nil {
			return Result{}, ErrUnavailable
		}
	} else if err != nil {
		return Result{}, ErrUnavailable
	}
	id, err := ParseCartID(raw)
	if err != nil {
		return Result{}, ErrStoredData
	}
	out, err := s.read(ctx, tx, local, id)
	if err != nil {
		return Result{}, err
	}
	return s.commit(ctx, tx, local, op, req, out)
}

func (s *Store) Apply(ctx context.Context, local LocalContext, op OperationID, id CartID, v Version, a Action) (Result, error) {
	if !validID(op.value) || !validID(id.value) || v.value < 0 {
		return Result{}, ErrInput
	}
	req, err := canonical(id, v, a)
	if err != nil {
		return Result{}, err
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if previous, ok, err := s.replay(ctx, tx, local, op, req); err != nil || ok {
		return previous, err
	}
	out, err := s.read(ctx, tx, local, id)
	if err != nil {
		return Result{}, err
	}
	if out.Version != v || v.value == math.MaxInt64 {
		return Result{}, ErrStale
	}
	switch action := a.(type) {
	case addAction:
		// Row locks, not the restaurant lock alone, serialize legacy availability.
		var price int64
		var c string
		var e int16
		var available bool
		err = tx.QueryRow(ctx, `SELECT m.price_minor,m.currency,s.currency_exponent,m.available FROM app.menu_items m JOIN app.restaurant_settings s ON s.restaurant_id=m.restaurant_id WHERE m.restaurant_id=$1 AND m.id=$2 FOR SHARE OF m,s`, s.restaurant, action.menu.value).Scan(&price, &c, &e, &available)
		if err != nil {
			return Result{}, unavailable(err)
		}
		if price < 0 || c != out.Currency || e != out.Exponent {
			return Result{}, ErrStoredData
		}
		if !available {
			return Result{}, ErrUnavailableItem
		}
		// A lower bound on the entire encoded result is checked before count-sized
		// allocation or UUID generation, including huge counts of free items.
		if action.count > int64(resultBudget/72-len(out.Units)) {
			return Result{}, ErrLimit
		}
		if price > 0 && action.count > (math.MaxInt64-out.Subtotal)/price {
			return Result{}, ErrLimit
		}
		var position int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(position),-1) FROM app.cart_units WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3`, s.restaurant, local.conversation.value, id.value).Scan(&position); err != nil {
			return Result{}, ErrUnavailable
		}
		if position > math.MaxInt64-action.count {
			return Result{}, ErrLimit
		}
		added := make([]Unit, 0, int(action.count))
		for n := int64(0); n < action.count; n++ {
			uid, err := newID[unitKind]()
			if err != nil {
				return Result{}, err
			}
			added = append(added, Unit{uid, action.menu, price, c, e, ""})
		}
		out.Units = append(out.Units, added...)
		out.Subtotal += price * action.count
		out.Version = Version{v.value + 1}
		if _, err = encodeResult(Result{op, out}); err != nil {
			return Result{}, err
		}
		for n, u := range added {
			_, err = tx.Exec(ctx, `INSERT INTO app.cart_units(id,restaurant_id,conversation_id,cart_id,menu_id,position,price_minor,currency,exponent,note,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, u.ID.value, s.restaurant, local.conversation.value, id.value, u.MenuID.value, position+int64(n)+1, u.PriceMinor, u.Currency, u.Exponent, u.Note, time.Now().UTC())
			if err != nil {
				return Result{}, ErrUnavailable
			}
		}
	case removeAction:
		out, err = editTargets(out, action.units, "", true)
		if err != nil {
			return Result{}, err
		}
		for _, uid := range action.units {
			if _, err = tx.Exec(ctx, `DELETE FROM app.cart_units WHERE restaurant_id=$1 AND conversation_id=$2 AND cart_id=$3 AND id=$4`, s.restaurant, local.conversation.value, id.value, uid.value); err != nil {
				return Result{}, ErrUnavailable
			}
		}
	case noteAction:
		out, err = editTargets(out, action.units, action.note, false)
		if err != nil {
			return Result{}, err
		}
		out.Version = Version{v.value + 1}
		if _, err = encodeResult(Result{op, out}); err != nil {
			return Result{}, err
		}
		for _, uid := range action.units {
			if _, err = tx.Exec(ctx, `UPDATE app.cart_units SET note=$1 WHERE restaurant_id=$2 AND conversation_id=$3 AND cart_id=$4 AND id=$5`, action.note, s.restaurant, local.conversation.value, id.value, uid.value); err != nil {
				return Result{}, ErrUnavailable
			}
		}
	default:
		return Result{}, ErrInput
	}
	out.Version = Version{v.value + 1}
	out.Subtotal, err = validateSnapshot(out)
	if err != nil {
		return Result{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE app.carts SET version=$1 WHERE restaurant_id=$2 AND conversation_id=$3 AND id=$4 AND version=$5`, out.Version.value, s.restaurant, local.conversation.value, id.value, v.value)
	if err != nil {
		return Result{}, ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return Result{}, ErrStale
	}
	return s.commit(ctx, tx, local, op, req, out)
}
func editTargets(out Snapshot, ids []UnitID, note string, remove bool) (Snapshot, error) {
	wanted := make(map[UnitID]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	found := 0
	units := make([]Unit, 0, len(out.Units))
	for _, u := range out.Units {
		if wanted[u.ID] {
			found++
			if remove {
				continue
			}
			u.Note = note
		}
		units = append(units, u)
	}
	if found != len(ids) {
		return Snapshot{}, ErrNotFound
	}
	out.Units = units
	return out, nil
}

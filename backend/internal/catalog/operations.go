package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrForbidden           = errors.New("staff access is not available")
	ErrItemNotFound        = errors.New("catalog item not found")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with committed request")
)

// AvailabilityIntent is server-scoped. Empty ItemID targets restaurant settings.
// Subject must come from the verified JWT, never a caller-supplied claim header.
type AvailabilityIntent struct {
	Subject         string
	Key             string
	RequestID       string
	ItemID          string
	ExpectedVersion int64
	Value           bool
}

func canonicalV4(raw string) bool {
	_, err := uuid(raw)
	return err == nil && len(raw) == 36 && raw[14] == '4' && strings.ContainsRune("89ab", rune(raw[19]))
}
func validKey(key string) bool {
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

type availabilityDTO struct {
	Open    bool   `json:"open"`
	Version string `json:"version"`
}
type moneyDTO struct {
	Amount   string `json:"amount_minor"`
	Currency string `json:"currency"`
	Exponent int16  `json:"exponent"`
}
type itemDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Price     moneyDTO `json:"price"`
	Available bool     `json:"available"`
	Version   string   `json:"version"`
}

// ApplyAvailability serializes on the existing restaurant identity before a fresh
// membership check and replay lookup. authorize must perform a current local DB
// membership check only (e.g. staff.Store.Access); never JWT/JWKS or provider I/O.
// The complete bounded response and domain change commit together, with no claim
// transaction or automatic retry. A replay returns the original bytes/request ID.
func (s *Store) ApplyAvailability(ctx context.Context, in AvailabilityIntent, authorize func(context.Context, string) (bool, error)) ([]byte, error) {
	if in.ExpectedVersion < 0 || !validKey(in.Key) || !utf8.ValidString(in.Subject) || len(in.Subject) > 256 || strings.TrimSpace(in.Subject) == "" || strings.ContainsRune(in.Subject, 0) || in.RequestID == "" || len(in.RequestID) > 128 || !utf8.ValidString(in.RequestID) || authorize == nil || (in.ItemID != "" && !canonicalV4(in.ItemID)) {
		return nil, ErrInput
	}
	resource := "/v1/staff/restaurant/availability"
	field := "open"
	if in.ItemID != "" {
		resource = "/v1/staff/catalog/items/" + in.ItemID + "/availability"
		field = "available"
	}
	// Marshal parsed values, not raw request bytes or key ordering.
	canonical, _ := json.Marshal(map[string]any{"expected_version": strconv.FormatInt(in.ExpectedVersion, 10), field: in.Value})
	fingerprint := sha256.Sum256(canonical)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var identity pgtype.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM app.restaurants WHERE id=$1 FOR UPDATE", s.restaurant).Scan(&identity)
	if err != nil {
		return nil, ErrUnavailable
	}
	active, err := authorize(ctx, in.Subject)
	if err != nil {
		return nil, ErrUnavailable
	}
	if !active {
		return nil, ErrForbidden
	}
	var previous []byte
	var body string
	err = tx.QueryRow(ctx, `SELECT request_fingerprint,response_body FROM app.operations WHERE restaurant_id=$1 AND auth_user_id=$2 AND method='PATCH' AND resource=$3 AND idempotency_key=$4`, s.restaurant, in.Subject, resource, in.Key).Scan(&previous, &body)
	if err == nil {
		if !bytes.Equal(previous, fingerprint[:]) {
			return nil, ErrIdempotencyConflict
		}
		if !validSavedResponse(body, in.ItemID != "") {
			return nil, ErrStoredData
		}
		return []byte(body), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnavailable
	}
	settings, err := readSettings(ctx, tx, s.restaurant)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if validate(settings, nil) != nil {
		return nil, ErrStoredData
	}
	var result any
	if in.ItemID == "" {
		if in.ExpectedVersion == math.MaxInt64 {
			return nil, ErrStale
		}
		err = tx.QueryRow(ctx, `UPDATE app.restaurant_settings SET open=$1,version=version+1 WHERE restaurant_id=$2 AND version=$3 RETURNING open,version`, in.Value, s.restaurant, in.ExpectedVersion).Scan(&settings.Open, &settings.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStale
		}
		if err != nil {
			return nil, safe(err)
		}
		result = availabilityDTO{settings.Open, strconv.FormatInt(settings.Version, 10)}
	} else {
		id, _ := uuid(in.ItemID)
		// Distinguish absent/cross-scope rows without disclosing another scope.
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.menu_items WHERE restaurant_id=$1 AND id=$2)`, s.restaurant, id).Scan(&exists); err != nil {
			return nil, ErrUnavailable
		}
		if !exists {
			return nil, ErrItemNotFound
		}
		item, err := setAvailability(ctx, tx, s.restaurant, id, in.ExpectedVersion, in.Value)
		if err != nil {
			return nil, err
		}
		if validate(settings, []Item{item}) != nil || !canonicalV4(item.ID) {
			return nil, ErrStoredData
		}
		result = itemDTO{item.ID, item.Name, moneyDTO{strconv.FormatInt(item.PriceMinor, 10), item.Currency, settings.CurrencyExponent}, item.Available, strconv.FormatInt(item.Version, 10)}
	}
	encoded, err := json.Marshal(struct {
		Data      any    `json:"data"`
		RequestID string `json:"request_id"`
	}{result, in.RequestID})
	if err != nil || len(encoded)+1 > 512*1024 {
		return nil, ErrStoredData
	}
	encoded = append(encoded, '\n')
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, ErrUnavailable
	}
	random[6] = (random[6] & 15) | 64
	random[8] = (random[8] & 63) | 128
	raw := hex.EncodeToString(random[:])
	operationID := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	_, err = tx.Exec(ctx, `INSERT INTO app.operations(id,restaurant_id,auth_user_id,method,resource,idempotency_key,request_fingerprint,response_status,response_body,created_at) VALUES($1,$2,$3,'PATCH',$4,$5,$6,200,$7,transaction_timestamp())`, operationID, s.restaurant, in.Subject, resource, in.Key, fingerprint[:], string(encoded))
	if err != nil {
		return nil, safe(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, safe(err)
	}
	return encoded, nil
}

// Validate replayed scalars without consulting current versions or rewriting any
// committed bytes. Round-trip equality also rejects missing/unknown/null fields.
func validSavedResponse(body string, isItem bool) bool {
	if len(body) == 0 || len(body) > 512*1024 || !utf8.ValidString(body) {
		return false
	}
	var envelope struct {
		Data      json.RawMessage `json:"data"`
		RequestID string          `json:"request_id"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil || envelope.RequestID == "" || len(envelope.RequestID) > 128 {
		return false
	}
	var data any
	if isItem {
		var item itemDTO
		if json.Unmarshal(envelope.Data, &item) != nil || !canonicalV4(item.ID) {
			return false
		}
		price, e1 := strconv.ParseInt(item.Price.Amount, 10, 64)
		version, e2 := strconv.ParseInt(item.Version, 10, 64)
		if e1 != nil || e2 != nil || strconv.FormatInt(price, 10) != item.Price.Amount || strconv.FormatInt(version, 10) != item.Version {
			return false
		}
		if validate(Settings{Currency: item.Price.Currency, CurrencyExponent: item.Price.Exponent}, []Item{{ID: item.ID, Name: item.Name, PriceMinor: price, Currency: item.Price.Currency, Version: version}}) != nil {
			return false
		}
		data = item
	} else {
		var settings availabilityDTO
		if json.Unmarshal(envelope.Data, &settings) != nil {
			return false
		}
		version, err := strconv.ParseInt(settings.Version, 10, 64)
		if err != nil || version < 0 || strconv.FormatInt(version, 10) != settings.Version {
			return false
		}
		data = settings
	}
	encoded, err := json.Marshal(struct {
		Data      any    `json:"data"`
		RequestID string `json:"request_id"`
	}{data, envelope.RequestID})
	return err == nil && string(append(encoded, '\n')) == body
}

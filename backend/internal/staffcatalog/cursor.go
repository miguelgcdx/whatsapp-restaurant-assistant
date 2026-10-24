// Package staffcatalog implements the protected, read-only catalog wire boundary.
package staffcatalog

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/staff"
)

const purpose = "restaurant-assistant/staff-catalog/cursor/v1\x00"

type cursor struct {
	Version    int    `json:"v"`
	Kind       string `json:"kind"`
	Restaurant string `json:"restaurant"`
	Time       string `json:"time"`
	ID         string `json:"id"`
}

func canonicalRestaurant(raw string) bool {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !id.Valid {
		return false
	}
	value, err := id.Value()
	return err == nil && value == raw
}

func canonicalID(raw string) bool {
	if !canonicalRestaurant(raw) {
		return false
	}
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return false
	}
	return id.Bytes[6]>>4 == 4 && id.Bytes[8]>>6 == 2
}

func parseKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) != 64 || strings.ToLower(raw) != raw {
		return nil, staff.ErrInput
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, staff.ErrInput
	}
	return key, nil
}

func (h *handler) mac(body []byte) []byte {
	m := hmac.New(sha256.New, h.key)
	_, _ = m.Write([]byte(purpose))
	_, _ = m.Write(body)
	return m.Sum(nil)
}

func (h *handler) encode(p catalog.Position) string {
	body, _ := json.Marshal(cursor{1, "catalog", h.restaurant, p.CreatedAt.UTC().Format(time.RFC3339Nano), p.ID})
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(h.mac(body))
}

func (h *handler) decode(raw string) (*catalog.Position, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, staff.ErrInput
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return nil, staff.ErrInput
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, h.mac(body)) {
		return nil, staff.ErrInput
	}
	var c cursor
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, staff.ErrInput
	}
	canonical, _ := json.Marshal(c)
	stamp, err := time.Parse(time.RFC3339Nano, c.Time)
	if !bytes.Equal(body, canonical) || c.Version != 1 || c.Kind != "catalog" || c.Restaurant != h.restaurant || !canonicalID(c.ID) || err != nil || stamp.IsZero() || stamp.Year() < 1 || stamp.Year() > 9999 || stamp.Nanosecond()%1000 != 0 || stamp.UTC().Format(time.RFC3339Nano) != c.Time {
		return nil, staff.ErrInput
	}
	p := catalog.Position{CreatedAt: stamp, ID: c.ID}
	if h.encode(p) != raw {
		return nil, staff.ErrInput
	}
	return &p, nil
}

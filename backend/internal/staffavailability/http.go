// Package staffavailability exposes only the two versioned staff catalog controls.
package staffavailability

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/staff"
)

const restaurantPath = "/v1/staff/restaurant/availability"
const itemPrefix = "/v1/staff/catalog/items/"
const itemSuffix = "/availability"

type handler struct {
	store    *catalog.Store
	members  staff.Membership
	verifier *staff.Verifier
}

// NewHandler is lazy and has no cursor-key dependency. Pass store and membership
// configured for the same server-owned restaurant; callers cannot select scope.
func NewHandler(store *catalog.Store, members staff.Membership, verifier *staff.Verifier) (http.Handler, error) {
	if store == nil || members == nil || verifier == nil {
		return nil, staff.ErrInput
	}
	return &handler{store, members, verifier}, nil
}

// Matches deliberately does no path cleaning, decoding or redirects. Malformed
// item IDs on the selected resource shape reach the strict input validator.
func Matches(path string) bool {
	return path == restaurantPath || (strings.HasPrefix(path, itemPrefix) && strings.HasSuffix(path, itemSuffix) && !strings.Contains(strings.TrimSuffix(strings.TrimPrefix(path, itemPrefix), itemSuffix), "/"))
}

func failure(w http.ResponseWriter, id string, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}{struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}, id})
}
func validID(id string) bool {
	if len(id) != 36 || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
		return false
	}
	for i, c := range []byte(id) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func key(r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || len(values[0]) < 1 || len(values[0]) > 128 {
		return "", false
	}
	for _, c := range []byte(values[0]) {
		if c < 33 || c > 126 {
			return "", false
		}
	}
	return values[0], true
}

// parseBody accepts exactly two typed fields and rejects duplicate decoded keys.
func parseBody(raw []byte, field string) (int64, bool, error) {
	if !utf8.Valid(raw) {
		return 0, false, catalog.ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return 0, false, catalog.ErrInput
	}
	seen := map[string]bool{}
	var version int64
	var value bool
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return 0, false, catalog.ErrInput
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return 0, false, catalog.ErrInput
		}
		seen[name] = true
		switch name {
		case "expected_version":
			var raw json.RawMessage
			if d.Decode(&raw) != nil || len(raw) == 0 || raw[0] != '"' {
				return 0, false, catalog.ErrInput
			}
			var text string
			if json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > 19 {
				return 0, false, catalog.ErrInput
			}
			version, err = strconv.ParseInt(text, 10, 64)
			if err != nil || version < 0 || strconv.FormatInt(version, 10) != text {
				return 0, false, catalog.ErrInput
			}
		case field:
			var raw json.RawMessage
			if d.Decode(&raw) != nil || (!bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false"))) {
				return 0, false, catalog.ErrInput
			}
			value = bytes.Equal(raw, []byte("true"))
		default:
			return 0, false, catalog.ErrInput
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 {
		return 0, false, catalog.ErrInput
	}
	if _, err = d.Token(); err != io.EOF {
		return 0, false, catalog.ErrInput
	}
	return version, value, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !Matches(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		failure(w, "", 500, "internal_error", "Availability could not be completed.")
		return
	}
	id := hex.EncodeToString(random[:])
	fail := func(status int, code, message string) { failure(w, id, status, code, message) }
	unavailable := func() { fail(503, "temporarily_unavailable", "Availability is temporarily unavailable.") }
	if r.Method != "PATCH" {
		w.Header().Set("Allow", "PATCH")
		fail(405, "method_not_allowed", "The method is not allowed.")
		return
	}
	itemID := ""
	field := "open"
	if r.URL.Path != restaurantPath {
		itemID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, itemPrefix), itemSuffix)
		field = "available"
		if !validID(itemID) {
			fail(400, "invalid_input", "Invalid request.")
			return
		}
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(400, "invalid_input", "Invalid request.")
		return
	}
	keys, ok := key(r)
	if !ok {
		fail(400, "invalid_input", "Invalid request.")
		return
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		fail(415, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}
	media, params, err := mime.ParseMediaType(types[0])
	if err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		fail(415, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(413, "payload_too_large", "Request body is too large.")
		} else {
			fail(400, "invalid_input", "Invalid request.")
		}
		return
	}
	version, value, err := parseBody(raw, field)
	if err != nil {
		fail(400, "invalid_input", "Invalid request.")
		return
	}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(headers[0], "Bearer "), " \t\r\n") {
		fail(401, "unauthenticated", "Authentication is required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	subject, err := h.verifier.Verify(ctx, strings.TrimPrefix(headers[0], "Bearer "))
	if errors.Is(err, staff.ErrUnavailable) {
		unavailable()
		return
	}
	if err != nil {
		fail(401, "unauthenticated", "Authentication is required.")
		return
	}
	body, err := h.store.ApplyAvailability(ctx, catalog.AvailabilityIntent{Subject: subject, Key: keys, RequestID: id, ItemID: itemID, ExpectedVersion: version, Value: value}, h.members.Access)
	switch {
	case err == nil:
		_, _ = w.Write(body)
	case errors.Is(err, catalog.ErrForbidden):
		fail(403, "forbidden", "Staff access is not available.")
	case errors.Is(err, catalog.ErrItemNotFound):
		fail(404, "not_found", "Catalog item was not found.")
	case errors.Is(err, catalog.ErrStale):
		fail(409, "version_conflict", "Refresh availability before trying again.")
	case errors.Is(err, catalog.ErrIdempotencyConflict):
		fail(409, "idempotency_conflict", "Idempotency key was already used for a different request.")
	case errors.Is(err, catalog.ErrStoredData):
		fail(500, "internal_error", "Availability could not be completed.")
	default:
		unavailable()
	}
}

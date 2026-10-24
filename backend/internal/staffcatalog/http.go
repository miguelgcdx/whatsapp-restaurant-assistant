package staffcatalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/staff"
)

type handler struct {
	store      *catalog.Store
	members    staff.Membership
	restaurant string
	verifier   *staff.Verifier
	key        []byte
}

// NewHandler is lazy and read-only. An absent key disables catalog reads only.
func NewHandler(store *catalog.Store, members staff.Membership, restaurant string, verifier *staff.Verifier, rawKey string) (http.Handler, error) {
	key, err := parseKey(rawKey)
	if err != nil || store == nil || members == nil || verifier == nil || !canonicalRestaurant(restaurant) {
		return nil, staff.ErrInput
	}
	return &handler{store, members, restaurant, verifier, key}, nil
}

type money struct {
	Amount   string `json:"amount_minor"`
	Currency string `json:"currency"`
	Exponent int16  `json:"exponent"`
}
type item struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Price     money  `json:"price"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
}
type availability struct {
	Open    bool   `json:"open"`
	Version string `json:"version"`
}
type data struct {
	Items      []item       `json:"items"`
	Next       *string      `json:"next_cursor"`
	Restaurant availability `json:"restaurant"`
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

func pagination(r *http.Request) (int, string, error) {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 || r.Header.Get("Content-Length") != "" || r.Header.Get("Transfer-Encoding") != "" || r.Header.Get("Trailer") != "" || r.URL.ForceQuery {
		return 0, "", staff.ErrInput
	}
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(b) != 0 {
			return 0, "", staff.ErrInput
		}
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", staff.ErrInput
	}
	limit := 50
	for k, v := range q {
		if len(v) != 1 {
			return 0, "", staff.ErrInput
		}
		switch k {
		case "limit":
			limit, err = strconv.Atoi(v[0])
			if err != nil || limit < 1 || limit > 100 {
				return 0, "", staff.ErrInput
			}
		case "cursor":
			if len(v[0]) == 0 || len(v[0]) > 2048 {
				return 0, "", staff.ErrInput
			}
			for _, c := range []byte(v[0]) {
				if c > 127 || c < 33 {
					return 0, "", staff.ErrInput
				}
			}
		default:
			return 0, "", staff.ErrInput
		}
	}
	return limit, q.Get("cursor"), nil
}

func validPage(p catalog.Page) bool {
	s := p.Settings
	if len(s.Currency) != 3 || s.CurrencyExponent < 0 || s.CurrencyExponent > 6 || s.Version < 0 || s.FlatFeeMinor < 0 {
		return false
	}
	for _, c := range s.Currency {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	for _, i := range p.Items {
		if !canonicalID(i.ID) || !utf8.ValidString(i.Name) || strings.TrimSpace(i.Name) == "" || utf8.RuneCountInString(i.Name) > 200 || strings.ContainsRune(i.Name, 0) || i.PriceMinor < 0 || i.Version < 0 || i.Currency != s.Currency {
			return false
		}
	}
	if p.Next != nil && (len(p.Items) == 0 || !canonicalID(p.Next.ID) || p.Next.CreatedAt.IsZero() || p.Next.CreatedAt.Year() < 1 || p.Next.CreatedAt.Year() > 9999 || p.Next.CreatedAt.Nanosecond()%1000 != 0) {
		return false
	}
	return true
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/staff/catalog" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		failure(w, "", 500, "internal_error", "Catalog could not be completed.")
		return
	}
	id := hex.EncodeToString(random[:])
	fail := func(status int, code, message string) { failure(w, id, status, code, message) }
	unavailable := func() { fail(503, "temporarily_unavailable", "Catalog is temporarily unavailable.") }
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		fail(405, "method_not_allowed", "The method is not allowed.")
		return
	}
	limit, raw, err := pagination(r)
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
	active, err := h.members.Access(ctx, subject)
	if err != nil {
		unavailable()
		return
	}
	if !active {
		fail(403, "forbidden", "Staff access is not available.")
		return
	}
	if len(h.key) == 0 {
		unavailable()
		return
	}
	var after *catalog.Position
	if raw != "" {
		after, err = h.decode(raw)
		if err != nil {
			fail(400, "invalid_input", "Invalid request.")
			return
		}
	}
	p, err := h.store.Page(ctx, limit, after)
	if errors.Is(err, catalog.ErrStoredData) {
		fail(500, "internal_error", "Catalog could not be completed.")
		return
	}
	if err != nil {
		unavailable()
		return
	}
	if !validPage(p) {
		fail(500, "internal_error", "Catalog could not be completed.")
		return
	}
	d := data{Items: make([]item, 0, len(p.Items)), Restaurant: availability{p.Settings.Open, strconv.FormatInt(p.Settings.Version, 10)}}
	for _, i := range p.Items {
		d.Items = append(d.Items, item{i.ID, i.Name, money{strconv.FormatInt(i.PriceMinor, 10), i.Currency, p.Settings.CurrencyExponent}, i.Available, strconv.FormatInt(i.Version, 10)})
	}
	if p.Next != nil {
		next := h.encode(*p.Next)
		d.Next = &next
	}
	body, err := json.Marshal(struct {
		Data      data   `json:"data"`
		RequestID string `json:"request_id"`
	}{d, id})
	if err != nil || len(body)+1 > 512*1024 {
		fail(500, "internal_error", "Catalog could not be completed.")
		return
	}
	body = append(body, '\n')
	_, _ = w.Write(body)
}

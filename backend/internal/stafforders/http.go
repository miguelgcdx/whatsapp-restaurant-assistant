// Package stafforders implements the initial pickup-only staff HTTP projection.
// Current membership, resource scope, replay and transitions belong to the port;
// every request, including a replay, reaches it only after bearer verification.
package stafforders

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
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/cart"
	"restaurant-assistant/backend/internal/staff"
)

const prefix = "/v1/staff/orders"
const responseLimit = 512 * 1024

type handler struct {
	port     cart.PickupStaffPort
	verifier *staff.Verifier
}

// NewHandler is lazy and borrows both dependencies. The port is configured for
// the server-owned restaurant; no HTTP field can select scope or an actor.
func NewHandler(port cart.PickupStaffPort, verifier *staff.Verifier) (http.Handler, error) {
	if port == nil || verifier == nil {
		return nil, staff.ErrInput
	}
	return &handler{port: port, verifier: verifier}, nil
}

type routeKind uint8

const (
	unknown routeKind = iota
	listRoute
	detailRoute
	unitsRoute
	actionRoute
)

func route(path string) (routeKind, string, string) {
	if path == prefix {
		return listRoute, "", ""
	}
	if !strings.HasPrefix(path, prefix+"/") {
		return unknown, "", ""
	}
	p := strings.Split(strings.TrimPrefix(path, prefix+"/"), "/")
	if len(p) == 1 {
		return detailRoute, p[0], ""
	}
	if len(p) == 2 && p[1] == "actions" {
		return actionRoute, p[0], ""
	}
	if len(p) == 4 && p[1] == "summaries" && p[3] == "units" {
		return unitsRoute, p[0], p[2]
	}
	return unknown, "", ""
}

// Matches selects just the four resource shapes, without path cleaning or redirects.
func Matches(path string) bool { k, _, _ := route(path); return k != unknown }

func validID(s string) bool {
	if len(s) != 36 || s[14] != '4' || !strings.ContainsRune("89ab", rune(s[19])) {
		return false
	}
	for i, c := range []byte(s) {
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
func opaque(s string) bool {
	if len(s) == 0 || len(s) > 2048 {
		return false
	}
	for _, c := range []byte(s) {
		if c > 127 {
			return false
		}
	}
	return true
}
func pagination(r *http.Request) (cart.PickupPageRequest, error) {
	p := cart.PickupPageRequest{Limit: 50}
	if r.URL.ForceQuery {
		return p, cart.ErrInput
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return p, cart.ErrInput
	}
	for k, v := range q {
		if len(v) != 1 {
			return p, cart.ErrInput
		}
		switch k {
		case "limit":
			n, e := strconv.Atoi(v[0])
			if e != nil || n < 1 || n > 100 {
				return p, cart.ErrInput
			}
			p.Limit = n
		case "cursor":
			if !opaque(v[0]) {
				return p, cart.ErrInput
			}
			p.Cursor = v[0]
		default:
			return p, cart.ErrInput
		}
	}
	return p, nil
}
func noBody(r *http.Request) bool {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 || r.Header.Get("Content-Length") != "" || r.Header.Get("Transfer-Encoding") != "" || r.Header.Get("Trailer") != "" {
		return false
	}
	if r.Body != nil {
		b, e := io.ReadAll(io.LimitReader(r.Body, 1))
		return e == nil && len(b) == 0
	}
	return true
}
func actionAllowed(a cart.PickupStaffAction) bool {
	switch a {
	case cart.PickupAccept, cart.PickupReject, cart.PickupStartPreparing, cart.PickupMarkReady, cart.PickupMarkPickedUp, cart.PickupCancel, cart.PickupMarkPaid:
		return true
	}
	return false
}
func parseAction(raw []byte) (cart.PickupStaffActionRequest, error) {
	var a cart.PickupStaffActionRequest
	if !utf8.Valid(raw) {
		return a, cart.ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return a, cart.ErrInput
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		if e != nil {
			return a, cart.ErrInput
		}
		name, ok := tok.(string)
		if !ok || seen[name] {
			return a, cart.ErrInput
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || len(value) == 0 || value[0] != '"' {
			return a, cart.ErrInput
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return a, cart.ErrInput
		}
		switch name {
		case "action":
			a.Action = cart.PickupStaffAction(text)
			if !actionAllowed(a.Action) {
				return a, cart.ErrInput
			}
		case "expected_version":
			if len(text) > 19 || a.ExpectedVersion.UnmarshalText([]byte(text)) != nil {
				return a, cart.ErrInput
			}
		default:
			return a, cart.ErrInput
		}
	}
	if tok, e = d.Token(); e != nil || tok != json.Delim('}') || len(seen) != 2 {
		return a, cart.ErrInput
	}
	if _, e = d.Token(); e != io.EOF {
		return a, cart.ErrInput
	}
	return a, nil
}

// writeFailure never serializes a domain/provider error string.
func writeFailure(w http.ResponseWriter, id string, status int, code string) {
	messages := map[string]string{"invalid_input": "Invalid request.", "unauthenticated": "Authentication is required.", "forbidden": "Staff access is not available.", "not_found": "Order resource was not found.", "version_conflict": "Refresh this order before trying again.", "idempotency_conflict": "Idempotency key was already used for a different request.", "invalid_transition": "The order action is not available.", "policy_unresolved": "This order action requires an unresolved policy.", "temporarily_unavailable": "Orders are temporarily unavailable.", "internal_error": "Orders could not be completed.", "unsupported_media_type": "Content-Type must be application/json.", "payload_too_large": "Request body is too large.", "method_not_allowed": "The method is not allowed."}
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
	}{code, messages[code]}, id})
}
func domainError(err error) (int, string) {
	switch {
	case errors.Is(err, cart.ErrInput), errors.Is(err, staff.ErrInput):
		return 400, "invalid_input"
	case errors.Is(err, staff.ErrUnauthenticated):
		return 401, "unauthenticated"
	case errors.Is(err, cart.ErrPickupForbidden):
		return 403, "forbidden"
	case errors.Is(err, cart.ErrNotFound):
		return 404, "not_found"
	case errors.Is(err, cart.ErrConflict):
		return 409, "idempotency_conflict"
	case errors.Is(err, cart.ErrStale):
		return 409, "version_conflict"
	case errors.Is(err, cart.ErrPickupInvalidTransition):
		return 409, "invalid_transition"
	case errors.Is(err, cart.ErrPickupPolicyUnresolved):
		return 422, "policy_unresolved"
	case errors.Is(err, cart.ErrUnavailable), errors.Is(err, staff.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 503, "temporarily_unavailable"
	default:
		return 500, "internal_error"
	}
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeFailure(w, "", 500, "internal_error")
		return
	}
	id := hex.EncodeToString(random[:])
	fail := func(status int, code string) { writeFailure(w, id, status, code) }
	kind, orderText, summaryText := route(r.URL.Path)
	if kind == unknown {
		fail(404, "not_found")
		return
	}
	method := "GET"
	if kind == actionRoute {
		method = "POST"
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		fail(405, "method_not_allowed")
		return
	}
	var orderID cart.PickupOrderID
	var summaryID cart.PickupCandidateID
	if r.URL.RawPath != "" || kind != listRoute && (!validID(orderText) || orderID.UnmarshalText([]byte(orderText)) != nil) || kind == unitsRoute && (!validID(summaryText) || summaryID.UnmarshalText([]byte(summaryText)) != nil) {
		fail(400, "invalid_input")
		return
	}
	page := cart.PickupPageRequest{}
	var intent cart.PickupStaffActionRequest
	if kind == listRoute || kind == unitsRoute {
		var err error
		page, err = pagination(r)
		if err != nil {
			fail(400, "invalid_input")
			return
		}
	} else if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(400, "invalid_input")
		return
	}
	if kind != actionRoute {
		if !noBody(r) {
			fail(400, "invalid_input")
			return
		}
	} else {
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || len(keys[0]) < 1 || len(keys[0]) > 128 {
			fail(400, "invalid_input")
			return
		}
		for _, c := range []byte(keys[0]) {
			if c < 33 || c > 126 {
				fail(400, "invalid_input")
				return
			}
		}
		media := r.Header.Values("Content-Type")
		if len(media) != 1 {
			fail(415, "unsupported_media_type")
			return
		}
		mt, params, err := mime.ParseMediaType(media[0])
		if err != nil || mt != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
			fail(415, "unsupported_media_type")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(413, "payload_too_large")
			} else {
				fail(400, "invalid_input")
			}
			return
		}
		intent, err = parseAction(raw)
		if err != nil {
			fail(400, "invalid_input")
			return
		}
		intent.IdempotencyKey = keys[0]
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(auth[0], "Bearer "), " \t\r\n") {
		fail(401, "unauthenticated")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	subject, err := h.verifier.Verify(ctx, strings.TrimPrefix(auth[0], "Bearer "))
	if err != nil {
		if errors.Is(err, staff.ErrUnavailable) {
			fail(503, "temporarily_unavailable")
		} else {
			fail(401, "unauthenticated")
		}
		return
	}
	var data any
	switch kind {
	case listRoute:
		var p cart.PickupOrderPage
		p, err = h.port.ListOrders(ctx, cart.PickupStaffSubject(subject), page)
		if err == nil {
			data, err = projectList(p, page.Limit)
		}
	case detailRoute:
		var o cart.PickupOrder
		o, err = h.port.ReadOrder(ctx, cart.PickupStaffSubject(subject), orderID)
		if err == nil {
			data, err = projectDetail(o, orderID)
		}
	case unitsRoute:
		var p cart.PickupOrderUnitPage
		p, err = h.port.ReadUnits(ctx, cart.PickupStaffSubject(subject), orderID, summaryID, page)
		if err == nil {
			data, err = projectUnits(p, summaryID, page.Limit)
		}
	case actionRoute:
		var o cart.PickupOrder
		o, err = h.port.ApplyAction(ctx, cart.PickupStaffSubject(subject), orderID, intent)
		if err == nil {
			data, err = projectDetail(o, orderID)
		}
	}
	if err != nil {
		status, code := domainError(err)
		fail(status, code)
		return
	}
	body, err := json.Marshal(struct {
		Data      any    `json:"data"`
		RequestID string `json:"request_id"`
	}{data, id})
	if err != nil || len(body)+1 > responseLimit {
		fail(500, "internal_error")
		return
	}
	body = append(body, '\n')
	_, _ = w.Write(body)
}

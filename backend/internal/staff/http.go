package staff

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Membership interface {
	Access(context.Context, string) (bool, error)
}
type handler struct {
	members    Membership
	restaurant string
	verifier   *Verifier
}

func NewHandler(members Membership, restaurant string, verifier *Verifier) (http.Handler, error) {
	if _, e := parseRestaurant(restaurant); e != nil || members == nil || verifier == nil {
		return nil, ErrInput
	}
	return &handler{members: members, restaurant: restaurant, verifier: verifier}, nil
}
func failure(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}})
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/staff/access" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		failure(w, 405, "method_not_allowed", "The method is not allowed.")
		return
	}
	// One byte suffices to reject all bodies, including chunked bodies.
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if e != nil || len(body) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, 400, "invalid_input", "Invalid request.")
		return
	}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || strings.ContainsAny(strings.TrimPrefix(headers[0], "Bearer "), " \t\r\n") {
		failure(w, 401, "unauthenticated", "Authentication is required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	subject, e := h.verifier.Verify(ctx, strings.TrimPrefix(headers[0], "Bearer "))
	if errors.Is(e, ErrUnavailable) {
		failure(w, 503, "temporarily_unavailable", "Staff access is temporarily unavailable.")
		return
	}
	if e != nil {
		failure(w, 401, "unauthenticated", "Authentication is required.")
		return
	}
	active, e := h.members.Access(ctx, subject)
	if e != nil {
		failure(w, 503, "temporarily_unavailable", "Staff access is temporarily unavailable.")
		return
	}
	if !active {
		failure(w, 403, "forbidden", "Staff access is not available.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		RestaurantID string `json:"restaurant_id"`
	}{h.restaurant})
}

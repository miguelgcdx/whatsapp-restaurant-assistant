// Package api serves only the local synthetic callback protocol.
package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"restaurant-assistant/backend/internal/foundation"
)

const maxBody = 65536

// New constructs the synthetic protocol. It performs no setup or database probes.
func New(store foundation.Store, secret []byte, wake func(context.Context) error) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if len(secret) < 32 {
		return nil, errors.New("synthetic secret must contain at least 32 bytes")
	}
	key := bytes.Clone(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/synthetic/events" {
			respond(w, 404, "not_found")
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			respond(w, 405, "method_not_allowed")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				respond(w, 413, "body_too_large")
			} else {
				respond(w, 400, "invalid_event")
			}
			return
		}
		values := r.Header.Values("X-Synthetic-Signature")
		var digest []byte
		if len(values) == 1 && strings.HasPrefix(values[0], "sha256=") && len(values[0]) == 71 {
			digest, _ = hex.DecodeString(strings.TrimPrefix(values[0], "sha256="))
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(raw)
		if !hmac.Equal(digest, mac.Sum(nil)) {
			respond(w, 401, "unauthorized")
			return
		}
		if !utf8.Valid(raw) {
			respond(w, 400, "invalid_event")
			return
		}
		var event foundation.Event
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			respond(w, 400, "invalid_event")
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || event.Validate() != nil {
			respond(w, 400, "invalid_event")
			return
		}
		if _, err := store.Accept(r.Context(), event, time.Now()); err != nil {
			if errors.Is(err, foundation.ErrConflict) {
				respond(w, 409, "event_conflict")
			} else {
				respond(w, 503, "storage_unavailable")
			}
			return
		}
		if wake != nil {
			_ = wake(r.Context())
		}
		respond(w, 200, "")
	}), nil
}

func respond(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if code == "" {
		_, _ = io.WriteString(w, "{\"ok\":true}\n")
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{code})
}

// Notify sends an unauthoritative constant hint with at most 250ms of I/O time.
// Durable ingestion never relies on this socket being available.
func Notify(ctx context.Context, socketPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unixgram", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_, err = conn.Write([]byte("wake"))
	return err
}

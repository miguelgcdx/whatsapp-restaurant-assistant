// Package testpickupreceiver provides a fixture-owned fake LOCAL HTTP receiver.
package testpickupreceiver

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Message carries host-owned correlation and exact presentation text.
type Message struct {
	CorrelationID string `json:"correlation_id"`
	Text          string `json:"text"`
}

// Acknowledgement describes what the fake receiver actually received.
// ContentSHA256 is the lowercase hexadecimal SHA-256 of decoded Text bytes.
type Acknowledgement struct {
	CorrelationID     string `json:"correlation_id"`
	ReceiverReference string `json:"receiver_reference"`
	ContentSHA256     string `json:"content_sha256"`
}

// Receiver owns a loopback server whose lifetime is bounded by the test.
type Receiver struct {
	server *httptest.Server
	client *http.Client

	mu       sync.Mutex
	received []Message
}

// New starts a fake receiver and registers server cleanup with t.
func New(t testing.TB) *Receiver {
	t.Helper()
	receiver := &Receiver{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(receiver.receive))
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 5 * time.Second
	server.Config.WriteTimeout = 5 * time.Second
	server.Config.IdleTimeout = 5 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	receiver.server = server
	receiver.client = server.Client()
	receiver.client.Timeout = 5 * time.Second
	return receiver
}

// URL returns the loopback message endpoint.
func (r *Receiver) URL() string { return r.server.URL }

// Client returns the fixture's reusable, time-bounded HTTP client.
func (r *Receiver) Client() *http.Client { return r.client }

// Received returns a defensive snapshot of successfully decoded requests,
// including replay attempts. Concurrent requests have no predetermined order.
func (r *Receiver) Received() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.received...)
}

func (r *Receiver) receive(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound the whole body before decoding, preserving exact decoded Text.
	// Unknown fields (including client-supplied receipt fields) are ignored.
	request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	var message Message
	if err := json.Unmarshal(body, &message); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	digest := sha256.Sum256([]byte(message.Text))
	ack := Acknowledgement{
		CorrelationID: message.CorrelationID,
		// An injective encoding keeps distinct correlations distinct without
		// counters, global state, or dependence on receiver instance lifetime.
		ReceiverReference: "local-" + base64.RawURLEncoding.EncodeToString([]byte(message.CorrelationID)),
		ContentSHA256:     hex.EncodeToString(digest[:]),
	}
	r.mu.Lock()
	r.received = append(r.received, message)
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	// All fields are strings, so encoding cannot fail before writing. A failed
	// transport write does not undo the request actually received and recorded.
	if err := json.NewEncoder(w).Encode(ack); err != nil {
		return
	}
}

package testpickupeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/commandcode"
	"restaurant-assistant/backend/internal/pickupintent"
	"restaurant-assistant/backend/internal/testpickupreceiver"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func boundServer(server *httptest.Server) {
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 5 * time.Second
	server.Config.WriteTimeout = 5 * time.Second
	server.Config.IdleTimeout = 5 * time.Second
}

func receive(ctx context.Context, t testing.TB, receiver *testpickupreceiver.Receiver, correlation, text string) (Delivery, [32]byte) {
	t.Helper()
	before := len(receiver.Received())
	body, err := json.Marshal(testpickupreceiver.Message{CorrelationID: correlation, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.URL(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := receiver.Client().Do(request)
	if err != nil {
		t.Fatal("owned receiver HTTP request failed")
	}
	defer response.Body.Close()
	var ack testpickupreceiver.Acknowledgement
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK || decoder.Decode(&ack) != nil {
		t.Fatal("owned receiver did not return a valid HTTP 200 ACK")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		t.Fatal("owned receiver ACK had trailing or oversized data")
	}
	decoded, err := hex.DecodeString(ack.ContentSHA256)
	if err != nil || len(decoded) != sha256.Size || ack.CorrelationID != correlation || ack.ReceiverReference == "" {
		t.Fatal("actual receiver ACK lacked valid correlation/digest")
	}
	var digest [32]byte
	copy(digest[:], decoded)
	if digest != sha256.Sum256([]byte(text)) {
		t.Fatal("actual receiver ACK did not bind exact intent bytes")
	}
	received := receiver.Received()
	if len(received) != before+1 || received[before].CorrelationID != correlation || received[before].Text != text {
		t.Fatal("actual receiver observation did not match acknowledged intent")
	}
	return Delivery{Message: received[before], Acknowledgment: ack}, digest
}

func modelServer(t testing.TB, script ModelResponse, barrier *modelBarrier) (pickupintent.Assessor, func() []ModelRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []ModelRequest
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		// Capture only the bounded body, never authorization or provider headers.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		var wire ModelRequest
		var extra any
		if r.Method != http.MethodPost || decoder.Decode(&wire) != nil || decoder.Decode(&extra) != io.EOF {
			t.Error("invalid offline model HTTP request")
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, wire)
		mu.Unlock()
		if barrier != nil && !barrier.pause(r.Context()) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status := script.HTTPStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = io.WriteString(w, `{"error":"synthetic provider unavailable"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"index": 0, "finish_reason": "tool_calls", "message": map[string]any{
				"role": "assistant", "content": nil, "tool_calls": []commandcode.ToolCall{{
					ID: "offline-pickup-eval", Type: "function", Function: commandcode.FunctionCall{
						Name: "assess_pickup_completion", Arguments: script.ToolArguments,
					},
				}},
			},
		}}})
	}))
	boundServer(server)
	server.Start()
	t.Cleanup(server.Close)
	destination, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := commandcode.New(commandcode.Config{APIKey: "synthetic-eval-completion", Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != commandcode.Endpoint {
			return nil, errors.New("unexpected model endpoint")
		}
		local := r.Clone(r.Context())
		local.URL = destination
		local.Host = destination.Host
		local.Header.Del("Authorization")
		return server.Client().Transport.RoundTrip(local)
	})})
	if err != nil {
		t.Fatal(err)
	}
	assessor, err := pickupintent.New(pickupintent.Config{Client: client, MaxOutputTokens: 80})
	if err != nil {
		t.Fatal(err)
	}
	return assessor, func() []ModelRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]ModelRequest(nil), requests...)
	}
}

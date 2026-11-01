package testpickupreceiver_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/testpickupreceiver"
)

func TestHTTPAcknowledgementBindsExactReceivedText(t *testing.T) {
	receiver := testpickupreceiver.New(t)
	message := testpickupreceiver.Message{
		CorrelationID: "host-message-17",
		Text:          " \t¡Pedido listo! 🍲\nCafe\u0301 / café\n  ",
	}
	// Spoofed receipt fields are not part of Message and must not supply the ACK.
	body, err := json.Marshal(map[string]string{
		"correlation_id":     message.CorrelationID,
		"text":               message.Text,
		"content_sha256":     strings.Repeat("0", 64),
		"receiver_reference": "client-supplied-not-a-receipt",
	})
	if err != nil {
		t.Fatal(err)
	}
	ack := postAcknowledgement(t, receiver, string(body))
	assertAcknowledgement(t, ack, message)
	if ack.ReceiverReference == "client-supplied-not-a-receipt" {
		t.Error("receiver trusted the client-supplied reference")
	}
	assertReceived(t, receiver, []testpickupreceiver.Message{message})
}

func TestHTTPReplayHasStableCorrelatedAcknowledgement(t *testing.T) {
	receiver := testpickupreceiver.New(t)
	message := testpickupreceiver.Message{CorrelationID: "intent-23", Text: "Resumen\n¿Confirmas?"}
	first := postMessage(t, receiver, message)
	second := postMessage(t, receiver, message)
	assertAcknowledgement(t, first, message)
	assertAcknowledgement(t, second, message)
	if first != second {
		t.Errorf("replay ACK = %#v, first = %#v", second, first)
	}
	other := message
	other.CorrelationID = "intent-24"
	otherACK := postMessage(t, receiver, other)
	assertAcknowledgement(t, otherACK, other)
	if first.ReceiverReference == otherACK.ReceiverReference {
		t.Error("distinct correlations received the same reference")
	}
	assertReceived(t, receiver, []testpickupreceiver.Message{message, message, other})

	// Stability belongs to the correlation, not an instance-local request counter.
	fresh := testpickupreceiver.New(t)
	freshACK := postMessage(t, fresh, message)
	if freshACK != first {
		t.Errorf("fresh receiver ACK = %#v, want %#v", freshACK, first)
	}
	assertReceived(t, fresh, []testpickupreceiver.Message{message})
}

func TestHTTPReceivedSnapshotIsDefensive(t *testing.T) {
	receiver := testpickupreceiver.New(t)
	message := testpickupreceiver.Message{CorrelationID: "host-message-31", Text: " exact text\n"}
	assertAcknowledgement(t, postMessage(t, receiver, message), message)
	assertReceived(t, receiver, []testpickupreceiver.Message{message})
	snapshot := receiver.Received()
	snapshot[0] = testpickupreceiver.Message{CorrelationID: "changed", Text: "changed"}
	snapshot = append(snapshot, testpickupreceiver.Message{CorrelationID: "invented"})
	assertReceived(t, receiver, []testpickupreceiver.Message{message})
}

func TestHTTPConcurrentRequestsAreAllRecorded(t *testing.T) {
	receiver := testpickupreceiver.New(t)
	const requests = 8
	t.Run("parallel requests", func(t *testing.T) {
		for i := 0; i < requests; i++ {
			t.Run(fmt.Sprintf("host-%d", i), func(t *testing.T) {
				t.Parallel()
				message := testpickupreceiver.Message{CorrelationID: fmt.Sprintf("host-%d", i), Text: "Concurrent 🍲\n"}
				assertAcknowledgement(t, postMessage(t, receiver, message), message)
				// Read concurrently with other HTTP requests; callers own the snapshot.
				for j, recorded := range receiver.Received() {
					if recorded.Text != message.Text {
						t.Errorf("record %d text = %q, want %q", j, recorded.Text, message.Text)
					}
				}
			})
		}
	})
	received := receiver.Received()
	if len(received) != requests {
		t.Fatalf("received count = %d, want %d", len(received), requests)
	}
	seen := make(map[string]int)
	for _, message := range received {
		seen[message.CorrelationID]++
		if message.Text != "Concurrent 🍲\n" {
			t.Errorf("received text = %q", message.Text)
		}
	}
	for i := 0; i < requests; i++ {
		if count := seen[fmt.Sprintf("host-%d", i)]; count != 1 {
			t.Errorf("host-%d count = %d, want 1", i, count)
		}
	}
}

func TestHTTPRejectedRequestsAreNotRecorded(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, wantBody string
		status                       int
	}{
		{"invalid JSON", http.MethodPost, `{"correlation_id":`, "invalid JSON\n", http.StatusBadRequest},
		{"wrong method", http.MethodGet, `{"correlation_id":"host-1","text":"hello"}`, "method not allowed\n", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := testpickupreceiver.New(t)
			response, body := exchange(t, receiver, tc.method, tc.body)
			if response.StatusCode != tc.status {
				t.Errorf("HTTP status = %d, want %d; body = %q", response.StatusCode, tc.status, body)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
			if tc.status == http.StatusMethodNotAllowed && response.Header.Get("Allow") != http.MethodPost {
				t.Errorf("Allow = %q, want POST", response.Header.Get("Allow"))
			}
			if got := receiver.Received(); len(got) != 0 {
				t.Errorf("rejected request recorded: %#v", got)
			}
			// Rejection must not consume a reference or damage later requests.
			message := testpickupreceiver.Message{CorrelationID: "host-after-rejection", Text: "unchanged"}
			ack := postMessage(t, receiver, message)
			assertAcknowledgement(t, ack, message)
			fresh := testpickupreceiver.New(t)
			if want := postMessage(t, fresh, message); ack != want {
				t.Errorf("ACK after rejection = %#v, clean receiver = %#v", ack, want)
			}
			assertReceived(t, receiver, []testpickupreceiver.Message{message})
		})
	}
}

func postMessage(t *testing.T, receiver *testpickupreceiver.Receiver, message testpickupreceiver.Message) testpickupreceiver.Acknowledgement {
	t.Helper()
	body, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return postAcknowledgement(t, receiver, string(body))
}

func postAcknowledgement(t *testing.T, receiver *testpickupreceiver.Receiver, body string) testpickupreceiver.Acknowledgement {
	t.Helper()
	response, responseBody := exchange(t, receiver, http.MethodPost, body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200; body = %q", response.StatusCode, responseBody)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", response.Header.Get("Content-Type"))
	}
	var ack testpickupreceiver.Acknowledgement
	decoder := json.NewDecoder(strings.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		t.Fatalf("decode HTTP ACK: %v; body = %q", err, responseBody)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("trailing ACK response data: %v", err)
	}
	return ack
}

func exchange(t *testing.T, receiver *testpickupreceiver.Receiver, method, body string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(method, receiver.URL(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := receiver.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 64*1024 {
		t.Fatal("response exceeds fixture read bound")
	}
	return response, string(data)
}

func assertAcknowledgement(t *testing.T, ack testpickupreceiver.Acknowledgement, message testpickupreceiver.Message) {
	t.Helper()
	if ack.CorrelationID != message.CorrelationID {
		t.Errorf("ACK correlation = %q, want %q", ack.CorrelationID, message.CorrelationID)
	}
	if strings.TrimSpace(ack.ReceiverReference) == "" {
		t.Error("ACK receiver reference is blank")
	}
	digest := sha256.Sum256([]byte(message.Text))
	if want := hex.EncodeToString(digest[:]); ack.ContentSHA256 != want {
		t.Errorf("ACK SHA-256 = %q, want %q for exact decoded Text bytes", ack.ContentSHA256, want)
	}
}

func assertReceived(t *testing.T, receiver *testpickupreceiver.Receiver, want []testpickupreceiver.Message) {
	t.Helper()
	if got := receiver.Received(); !reflect.DeepEqual(got, want) {
		t.Fatalf("received = %#v, want %#v", got, want)
	}
}

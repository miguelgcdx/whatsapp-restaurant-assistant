package foundation

import "testing"

func TestEventValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		event Event
		valid bool
	}{
		{"valid exact text", Event{ID: "event-1", Sender: "customer-1", Text: "  one without onions\n"}, true},
		{"missing identity", Event{Sender: "customer-1", Text: "hello"}, false},
		{"missing sender", Event{ID: "event-1", Text: "hello"}, false},
		{"blank text", Event{ID: "event-1", Sender: "customer-1", Text: " \n"}, false},
		{"invalid UTF8", Event{ID: "event-1", Sender: "customer-1", Text: string([]byte{0xff})}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.event
			if err := tt.event.Validate(); (err == nil) != tt.valid {
				t.Fatalf("Validate() = %v, valid=%v", err, tt.valid)
			}
			if tt.event != original {
				t.Fatal("validation rewrote authored text")
			}
		})
	}
}

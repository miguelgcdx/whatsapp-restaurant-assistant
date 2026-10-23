// Package foundation defines the local synthetic-ingestion slice, not restaurant orders.
package foundation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrConflict   = errors.New("event identity already has different content")
	ErrStaleLease = errors.New("lease is no longer current")
	ErrNotFound   = errors.New("event not found")
)

type Event struct {
	ID     string `json:"event_id"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

func (e Event) Validate() error {
	for _, field := range []struct {
		name, value string
		max         int
	}{
		{"event_id", e.ID, 128}, {"sender", e.Sender, 128}, {"text", e.Text, 4000},
	} {
		if !utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "" || utf8.RuneCountInString(field.value) > field.max {
			return fmt.Errorf("invalid %s", field.name)
		}
	}
	return nil
}

type Job struct {
	ID         string
	Event      Event
	Generation int64
	Attempt    int32
}

type Delivery struct {
	ID         string
	EventID    string
	Text       string
	Generation int64
	Attempt    int32
}

type SendState string

const (
	SendAccepted SendState = "accepted"
	SendUnknown  SendState = "unknown"
	SendFailed   SendState = "failed"
)

type SendResult struct {
	State      SendState
	ProviderID string
}

// Snapshot exposes durable processing outcomes for local diagnostics and black-box tests.
// It deliberately excludes provider credentials and database row representations.
type Snapshot struct {
	Event       Event
	JobState    string
	JobAttempt  int32
	OutboxState string
	ProviderID  string
	JobCount    int64
	OutboxCount int64
}

// Store owns durable atomic operations. All timestamps are supplied by the service's
// clock; callers must never pass customer/model-supplied deadlines or lease identities.
type Store interface {
	Accept(context.Context, Event, time.Time) (bool, error)
	ClaimJob(context.Context, time.Time, time.Duration) (Job, bool, error)
	CompleteJob(context.Context, Job, string, time.Time) error
	RetryJob(context.Context, Job, time.Time, time.Time) error
	FailJob(context.Context, Job, time.Time) error
	ClaimDelivery(context.Context, time.Time, time.Duration) (Delivery, bool, error)
	FinishDelivery(context.Context, Delivery, SendResult, time.Time) error
	NextDue(context.Context) (time.Time, bool, error)
	Inspect(context.Context, string) (Snapshot, error)
	Close()
}

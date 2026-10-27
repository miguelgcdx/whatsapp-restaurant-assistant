package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres/db"
)

// EventIntake prepares the existing canonical digest and identities, not route
// authority. Callers validate input and own the transaction and job disposition.
type EventIntake struct {
	EventID, JobID pgtype.UUID
	Digest         [32]byte
	event          foundation.Event
}

func PrepareEventIntake(event foundation.Event) (EventIntake, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return EventIntake{}, err
	}
	eventID, err := newID()
	if err != nil {
		return EventIntake{}, err
	}
	jobID, err := newID()
	if err != nil {
		return EventIntake{}, err
	}
	return EventIntake{eventID, jobID, sha256.Sum256(encoded), event}, nil
}

// Allocate uses the same per-sender sequence and global event namespace for
// both routes. A duplicate attempt MUST be rolled back, including its sequence;
// the caller must check the original digest, route and scope before replay.
func (a EventIntake) Allocate(ctx context.Context, q *db.Queries, now time.Time) (bool, error) {
	sequence, err := q.NextSequence(ctx, a.event.Sender)
	if err != nil {
		return false, err
	}
	n, err := q.InsertEvent(ctx, db.InsertEventParams{ID: a.EventID, ProviderEventID: a.event.ID, Sender: a.event.Sender, Sequence: sequence, Content: a.event.Text, Digest: a.Digest[:], CreatedAt: stamp(now)})
	return n != 0, err
}

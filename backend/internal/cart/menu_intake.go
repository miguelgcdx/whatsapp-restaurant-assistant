package cart

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/postgres/db"
)

// Admission is a historical observation, never current model permission.
// Mode and Version describe the immutable admission epoch, not current control.
type Admission struct {
	Source        Source
	JobID         string
	Mode          string
	Version       int64
	State         string
	NoReplyReason string
	OutboxID      string
	Reply         string
}

// AcceptMenu admits an operator-pinned local event. The boolean identifies only
// a newly committed admission; callers must not emit hints on replay or error.
// An error at commit may leave the outcome unknown, not necessarily rolled back.
func (s *Store) AcceptMenu(ctx context.Context, local LocalContext, event foundation.Event, now time.Time) (Admission, bool, error) {
	if event.Validate() != nil || !text(event.ID, 128, true) || !text(event.Sender, 128, true) || !text(event.Text, 4000, true) {
		return Admission{}, false, ErrInput
	}
	return s.menuIntake(ctx, local, event.ID, &event, now)
}

// ReadAdmission resolves durable admission under ownership/source/control/job
// locks. Neither an absent receipt nor queued state grants model permission.
func (s *Store) ReadAdmission(ctx context.Context, local LocalContext, eventID string) (Admission, error) {
	out, _, err := s.menuIntake(ctx, local, eventID, nil, time.Time{})
	return out, err
}

func (s *Store) menuIntake(parent context.Context, local LocalContext, eventID string, event *foundation.Event, now time.Time) (out Admission, created bool, err error) {
	if parent == nil || s == nil || s.pool == nil || !local.valid() || local.restaurant != s.restaurant || !text(eventID, 128, true) {
		return Admission{}, false, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out, created = Admission{}, false
		}
	}()
	if err = sourceContextError(ctx); err != nil {
		return Admission{}, false, err
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return Admission{}, false, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	var allocation postgres.EventIntake
	if event != nil {
		if event.Sender != local.sender {
			return Admission{}, false, ErrNotFound
		}
		allocation, err = postgres.PrepareEventIntake(*event)
		if err != nil {
			return Admission{}, false, ErrUnavailable
		}
		created, err = allocation.Allocate(ctx, db.New(tx), now)
		if err != nil {
			return Admission{}, false, ErrUnavailable
		}
	}
	// Reuse the existing binding invariant under the normal ownership prefix;
	// any failed admission or read rolls back provisional binding/allocation.
	out.Source, err = s.bindSourceTx(ctx, tx, local, eventID)
	if err != nil {
		return Admission{}, false, err
	}
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&out.Mode, &out.Version); err != nil {
		return Admission{}, false, unavailable(err)
	}
	if (out.Mode != "ai" && out.Mode != "staff") || out.Version < 0 {
		return Admission{}, false, ErrStoredData
	}
	if !created {
		var digest []byte
		if event != nil {
			if err = tx.QueryRow(ctx, `SELECT digest FROM app.inbound_events WHERE id=$1`, out.Source.eventUUID).Scan(&digest); err != nil {
				return Admission{}, false, ErrUnavailable
			}
			if !bytes.Equal(digest, allocation.Digest[:]) {
				return Admission{}, false, ErrConflict
			}
		}
		out, err = readMenuAdmissionTx(ctx, tx, out.Source)
		if event != nil && err == ErrNotFound {
			err = ErrConflict // An existing legacy identity is never menu replay.
		}
		// Deliberate rollback, not commit: Allocate's duplicate sequence and any
		// provisional source must not become durable, including after reopen.
		return out, false, err
	}
	out.JobID, out.State = allocation.JobID.String(), "queued"
	if out.Mode == "staff" {
		out.State, out.NoReplyReason = "completed", "staff_at_admission"
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.jobs(id,event_id,state,due_at,route,restaurant_id,conversation_id,admission_mode,admission_version,no_reply_reason) VALUES($1,$2,$3,$4,'menu_message',$5,$6,$7,$8,NULLIF($9,''))`, allocation.JobID, allocation.EventID, out.State, now, s.restaurant, local.conversation.value, out.Mode, out.Version, out.NoReplyReason)
	if err != nil {
		return Admission{}, false, ErrUnavailable
	}
	if err = sourceContextError(ctx); err != nil {
		return Admission{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Admission{}, false, ErrUnavailable // Outcome may be unknown.
	}
	return out, true, nil
}

func readMenuAdmissionTx(ctx context.Context, tx pgx.Tx, source Source) (Admission, error) {
	out := Admission{Source: source}
	err := tx.QueryRow(ctx, `SELECT id::text,admission_mode,admission_version,state,COALESCE(no_reply_reason,'') FROM app.jobs WHERE event_id=$1 AND route='menu_message' AND restaurant_id=$2 AND conversation_id=$3 FOR UPDATE`, source.eventUUID, source.restaurant, source.conversation.value).Scan(&out.JobID, &out.Mode, &out.Version, &out.State, &out.NoReplyReason)
	if err != nil {
		return Admission{}, unavailable(err)
	}
	// Migration 11 enforces complete typed route/state/counter/source shapes.
	// Decode the persisted variants, never caller-provided eligibility flags.
	if (out.Mode != "ai" && out.Mode != "staff") || out.Version < 0 || (out.State != "queued" && out.State != "leased" && out.State != "completed") {
		return Admission{}, ErrStoredData
	}
	if out.State == "completed" && out.NoReplyReason == "" {
		// Resolve the exact original effect after the job lock. Current control
		// may differ; only the immutable admission/creation tuple identifies it.
		err = tx.QueryRow(ctx, `SELECT o.id::text,o.content FROM app.outbox o JOIN app.jobs j ON j.event_id=o.event_id WHERE j.id=$1 AND o.kind='ai_reply' AND o.restaurant_id=$2 AND o.conversation_id=$3 AND o.control_version=$4 AND o.creation_generation=j.generation AND o.creation_attempt=j.attempt FOR SHARE OF o`, out.JobID, source.restaurant, source.conversation.value, out.Version).Scan(&out.OutboxID, &out.Reply)
		if errors.Is(err, pgx.ErrNoRows) {
			return Admission{}, ErrStoredData
		}
		if err != nil {
			return Admission{}, ErrUnavailable
		}
	}
	return out, nil
}

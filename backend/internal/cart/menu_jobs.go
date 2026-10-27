package cart

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
)

// MenuClaim is an opaque persisted execution claim. Its zero value is invalid;
// metadata getters do not grant current permission to invoke a model.
type MenuClaim struct {
	source  Source
	job     foundation.Job
	mode    string
	version int64
}

func (c MenuClaim) Source() Source      { return c.source }
func (c MenuClaim) Job() foundation.Job { return c.job }

// ClaimMenu claims scoped durable menu work with a service-owned lease duration.
// Issuance is not current model permission; execution must revalidate authority.
// Old AI admissions remain claimable for explicit no-reply resolution later.
// Commit errors can leave an uncertain outcome; only known commits return true.
func (s *Store) ClaimMenu(parent context.Context, local LocalContext, lease time.Duration) (out MenuClaim, claimed bool, err error) {
	if parent == nil || s == nil || s.pool == nil || !local.valid() || local.restaurant != s.restaurant || lease <= 0 {
		return MenuClaim{}, false, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out, claimed = MenuClaim{}, false
		}
	}()
	if err = sourceContextError(ctx); err != nil {
		return MenuClaim{}, false, err
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return MenuClaim{}, false, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	// Discovery grants no authority. Ownership is already locked by begin;
	// source, control, then job locks below precede all eligibility decisions.
	// The earliest active scoped event also orders any later scoped candidate.
	var provider string
	err = tx.QueryRow(ctx, `SELECT e.provider_event_id FROM app.jobs j JOIN app.inbound_events e ON e.id=j.event_id WHERE j.route='menu_message' AND j.restaurant_id=$1 AND j.conversation_id=$2 AND j.admission_mode='ai' AND j.state IN ('queued','leased') ORDER BY e.sequence,j.id LIMIT 1`, s.restaurant, local.conversation.value).Scan(&provider)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MenuClaim{}, false, ErrUnavailable
	}
	if provider != "" {
		// The menu FK guarantees a persisted binding. Reuse its complete
		// ownership/sender invariant rather than allocate or rebind a source.
		out.source, err = s.bindSourceTx(ctx, tx, local, provider)
		if err != nil {
			return MenuClaim{}, false, err
		}
	}
	var mode string
	var version int64
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&mode, &version); err != nil {
		return MenuClaim{}, false, unavailable(err)
	}
	if (mode != "ai" && mode != "staff") || version < 0 {
		return MenuClaim{}, false, ErrStoredData
	}
	if provider == "" {
		return MenuClaim{}, false, nil
	}
	admission, err := readMenuAdmissionTx(ctx, tx, out.source)
	if err != nil {
		return MenuClaim{}, false, err
	}
	if admission.Mode != "ai" || (admission.State != "queued" && admission.State != "leased") {
		return MenuClaim{}, false, nil
	}
	out.job.ID = admission.JobID
	out.mode, out.version = admission.Mode, admission.Version
	if err = tx.QueryRow(ctx, `SELECT provider_event_id,sender,content FROM app.inbound_events WHERE id=$1 FOR SHARE`, out.source.eventUUID).Scan(&out.job.Event.ID, &out.job.Event.Sender, &out.job.Event.Text); err != nil {
		return MenuClaim{}, false, ErrUnavailable
	}
	// Legacy jobs and other conversations still precede this scoped candidate.
	// Recheck after the complete lock prefix, not at discovery or before waits.
	var blocked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.jobs previous JOIN app.inbound_events pe ON pe.id=previous.event_id WHERE pe.sender=$1 AND pe.sequence<(SELECT sequence FROM app.inbound_events WHERE id=$2) AND previous.state IN ('queued','leased'))`, local.sender, out.source.eventUUID).Scan(&blocked); err != nil {
		return MenuClaim{}, false, ErrUnavailable
	}
	if blocked {
		return MenuClaim{}, false, nil
	}
	// Separate statement AFTER all blocking locks. Neither transaction-start
	// now(), discovery time nor a caller clock starts this service-owned lease.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return MenuClaim{}, false, ErrUnavailable
	}
	err = tx.QueryRow(ctx, `UPDATE app.jobs SET state='leased',generation=generation+1,attempt=attempt+1,lease_until=$2 WHERE id=$1 AND route='menu_message' AND restaurant_id=$3 AND conversation_id=$4 AND event_id=$5 AND admission_mode='ai' AND admission_version=$6 AND ((state='queued' AND due_at<=$7) OR (state='leased' AND lease_until<=$7)) RETURNING generation,attempt`, out.job.ID, now.Add(lease), s.restaurant, local.conversation.value, out.source.eventUUID, out.version, now).Scan(&out.job.Generation, &out.job.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MenuClaim{}, false, nil
	}
	if err != nil {
		return MenuClaim{}, false, ErrUnavailable
	}
	if err = sourceContextError(ctx); err != nil {
		return MenuClaim{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MenuClaim{}, false, ErrUnavailable
	}
	return out, true, nil
}

// FinishMenuWithoutReply retires a live claim only after its original AI epoch
// changes. True means a known committed no-reply outcome; errors may be uncertain.
func (s *Store) FinishMenuWithoutReply(parent context.Context, local LocalContext, claim MenuClaim) (finished bool, err error) {
	if parent == nil || s == nil || s.pool == nil || !local.valid() || local.restaurant != s.restaurant {
		return false, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			finished = false
		}
	}()
	if err = sourceContextError(ctx); err != nil {
		return false, err
	}
	if claim == (MenuClaim{}) || claim.source.restaurant != local.restaurant || claim.source.conversation != local.conversation || claim.source.business != local.business || claim.source.sender != local.sender || claim.mode != "ai" || claim.version < 0 {
		return false, foundation.ErrStaleLease
	}
	tx, err := s.begin(ctx, local)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	source, err := s.bindSourceTx(ctx, tx, local, claim.source.eventProviderID)
	if err != nil {
		if err == ErrConflict || err == ErrNotFound {
			err = foundation.ErrStaleLease
		}
		return false, err
	}
	if source != claim.source {
		return false, foundation.ErrStaleLease
	}
	var mode string
	var version int64
	if err = tx.QueryRow(ctx, `SELECT mode,version FROM app.conversation_controls WHERE restaurant_id=$1 AND conversation_id=$2 FOR UPDATE`, s.restaurant, local.conversation.value).Scan(&mode, &version); err != nil {
		return false, unavailable(err)
	}
	if (mode != "ai" && mode != "staff") || version < 0 {
		return false, ErrStoredData
	}
	binding := postgres.MenuExecutionBinding{EventUUID: source.eventUUID, RestaurantID: source.restaurant, ConversationID: source.conversation.value, Business: source.business, Sender: source.sender, AdmissionMode: claim.mode, AdmissionVersion: claim.version}
	clock, err := postgres.LockMenuExecutionJob(ctx, tx, binding, claim.job)
	check := func(err error) error {
		if errors.Is(err, postgres.ErrClaimUnavailable) {
			return ErrUnavailable
		}
		return err
	}
	if err != nil {
		return false, check(err)
	}
	if err = clock(); err != nil {
		return false, check(err)
	}
	if err = sourceContextError(ctx); err != nil {
		return false, err
	}
	if mode == "ai" && version == claim.version {
		return false, nil
	}
	result, err := tx.Exec(ctx, `UPDATE app.jobs SET state='completed',no_reply_reason='control_changed',lease_until=NULL WHERE id=$1 AND route='menu_message' AND state='leased' AND generation=$2 AND attempt=$3`, claim.job.ID, claim.job.Generation, claim.job.Attempt)
	if err != nil {
		return false, ErrUnavailable
	}
	if result.RowsAffected() != 1 {
		return false, foundation.ErrStaleLease
	}
	if err = clock(); err != nil {
		return false, check(err)
	}
	if err = sourceContextError(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, ErrUnavailable
	}
	return true, nil
}

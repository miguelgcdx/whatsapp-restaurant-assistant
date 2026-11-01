package cart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"restaurant-assistant/backend/internal/foundation"
)

// NativeActionClaim is an opaque, persisted business lease, not reply permission.
// Its deadline is the actual PostgreSQL microsecond value returned at acquisition.
type NativeActionClaim struct {
	ref                 NativeActionRef
	generation, attempt int64
	deadline            time.Time
}

func (c NativeActionClaim) Ref() NativeActionRef { return c.ref }

func validNativeRef(ref NativeActionRef) bool {
	source := ref.source
	return validID(source.restaurant) && validID(source.conversation.value) && validID(source.eventUUID) && validID(ref.operation.value) && ref.slot >= 0 && text(source.eventProviderID, 128, true) && text(source.business, 256, true) && text(source.sender, 256, true)
}

func (s *Store) nativeContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil || s == nil || s.pool == nil {
		return nil, nil, ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	if err := sourceContextError(ctx); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

func rollbackNative(tx pgx.Tx) {
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = tx.Rollback(cleanup)
}

// RecoverNativeEdit leases an existing reference, including completed history.
// false,nil is only a temporary no-work observation, never model permission.
func (s *Store) RecoverNativeEdit(parent context.Context, ref NativeActionRef, lease time.Duration) (out NativeActionClaim, claimed bool, err error) {
	if !validNativeRef(ref) || lease < time.Millisecond || lease > 5*time.Minute {
		return NativeActionClaim{}, false, ErrInput
	}
	ctx, cancel, err := s.nativeContext(parent)
	if err != nil {
		return NativeActionClaim{}, false, err
	}
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out, claimed = NativeActionClaim{}, false
		}
	}()
	if ref.source.restaurant != s.restaurant {
		return NativeActionClaim{}, false, ErrNotFound
	}
	return s.acquireNative(ctx, ref, lease, false)
}

// ClaimNextNativeEdit discovers only unfinished admitted work, without model input
// or a remembered reference. A discovery race is retried within the same 5s bound.
func (s *Store) ClaimNextNativeEdit(parent context.Context, lease time.Duration) (out NativeActionClaim, claimed bool, err error) {
	if lease < time.Millisecond || lease > 5*time.Minute {
		return NativeActionClaim{}, false, ErrInput
	}
	ctx, cancel, err := s.nativeContext(parent)
	if err != nil {
		return NativeActionClaim{}, false, err
	}
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out, claimed = NativeActionClaim{}, false
		}
	}()
	for {
		if err = sourceContextError(ctx); err != nil {
			return NativeActionClaim{}, false, err
		}
		var ref NativeActionRef
		err = s.pool.QueryRow(ctx, `SELECT n.event_id::text,n.runtime_slot,n.operation_id::text,n.restaurant_id::text,n.conversation_id::text,e.provider_event_id,c.business,c.sender FROM app.native_cart_actions n JOIN app.cart_execution_sources s ON s.event_id=n.event_id AND s.restaurant_id=n.restaurant_id AND s.conversation_id=n.conversation_id JOIN app.cart_conversations c ON c.restaurant_id=s.restaurant_id AND c.id=s.conversation_id JOIN app.inbound_events e ON e.id=n.event_id WHERE n.restaurant_id=$1 AND n.committed_at IS NULL AND n.due_at<=clock_timestamp() AND (n.lease_until IS NULL OR n.lease_until<=clock_timestamp()) ORDER BY n.due_at,n.event_id,n.runtime_slot LIMIT 1`, s.restaurant).Scan(&ref.source.eventUUID, &ref.slot, &ref.operation.value, &ref.source.restaurant, &ref.source.conversation.value, &ref.source.eventProviderID, &ref.source.business, &ref.source.sender)
		if errors.Is(err, pgx.ErrNoRows) {
			return NativeActionClaim{}, false, nil
		}
		if err != nil {
			return NativeActionClaim{}, false, ErrUnavailable
		}
		if !validNativeRef(ref) {
			return NativeActionClaim{}, false, ErrStoredData
		}
		out, claimed, err = s.acquireNative(ctx, ref, lease, true)
		if err != nil || claimed {
			return out, claimed, err
		}
	}
}

type nativeWork struct {
	call                Call
	generation, attempt int64
	lease, committed    pgtype.Timestamptz
	due                 time.Time
}

// loadNative owns the usual restaurant/conversation/source/call prefix. The reply
// job is checked only as immutable provenance, never as current business authority.
func (s *Store) loadNative(ctx context.Context, ref NativeActionRef) (tx pgx.Tx, local LocalContext, work nativeWork, err error) {
	local = LocalContext{ref.source.restaurant, ref.source.conversation, ref.source.business, ref.source.sender}
	tx, err = s.begin(ctx, local)
	if err != nil {
		return nil, local, work, err
	}
	defer func() {
		if err != nil {
			rollbackNative(tx)
		}
	}()
	source, err := s.bindSourceTx(ctx, tx, local, ref.source.eventProviderID)
	if err != nil {
		if err == ErrConflict {
			err = ErrNotFound
		}
		return tx, local, work, err
	}
	if source != ref.source {
		return tx, local, work, ErrNotFound
	}
	var action, body, operation string
	var hash []byte
	if err = tx.QueryRow(ctx, `SELECT action,canonical_intent,fingerprint,operation_id::text FROM app.cart_execution_calls WHERE event_id=$1 AND runtime_slot=$2 AND restaurant_id=$3 AND conversation_id=$4 FOR UPDATE`, source.eventUUID, ref.slot, s.restaurant, local.conversation.value).Scan(&action, &body, &hash, &operation); err != nil {
		return tx, local, work, unavailable(err)
	}
	if operation != ref.operation.value {
		return tx, local, work, ErrNotFound
	}
	work.call, err = decodeCall(body)
	if err != nil {
		return tx, local, work, err
	}
	expected := sha256.Sum256([]byte(body))
	if action != work.call.req.Action || !bytes.Equal(hash, expected[:]) || (action != "add" && action != "remove" && action != "set_note") || work.call.draft != nil || work.call.preview != nil {
		return tx, local, work, ErrStoredData
	}
	// Keep the historical menu-job lock before the independently leased work row.
	var jobEvent, jobRestaurant, jobConversation, mode string
	var jobVersion, admittedVersion, admittedGeneration, admittedAttempt int64
	var jobGeneration int64
	var jobAttempt int32
	err = tx.QueryRow(ctx, `SELECT j.event_id::text,j.restaurant_id::text,j.conversation_id::text,j.admission_mode,j.admission_version,j.generation,j.attempt FROM app.jobs j JOIN app.native_cart_actions n ON n.admission_job_id=j.id WHERE n.event_id=$1 AND n.runtime_slot=$2 AND j.route='menu_message' FOR SHARE OF j`, source.eventUUID, ref.slot).Scan(&jobEvent, &jobRestaurant, &jobConversation, &mode, &jobVersion, &jobGeneration, &jobAttempt)
	if err != nil {
		return tx, local, work, unavailable(err)
	}
	var storedOperation, restaurant, conversation string
	err = tx.QueryRow(ctx, `SELECT operation_id::text,restaurant_id::text,conversation_id::text,admission_version,admission_generation,admission_attempt,generation,attempt,lease_until,committed_at,due_at FROM app.native_cart_actions WHERE event_id=$1 AND runtime_slot=$2 FOR UPDATE`, source.eventUUID, ref.slot).Scan(&storedOperation, &restaurant, &conversation, &admittedVersion, &admittedGeneration, &admittedAttempt, &work.generation, &work.attempt, &work.lease, &work.committed, &work.due)
	if err != nil {
		return tx, local, work, unavailable(err)
	}
	if storedOperation != operation || restaurant != s.restaurant || conversation != local.conversation.value || jobEvent != source.eventUUID || jobRestaurant != restaurant || jobConversation != conversation || mode != "ai" || jobVersion != admittedVersion || admittedVersion < 0 || admittedGeneration <= 0 || admittedAttempt <= 0 || jobGeneration < admittedGeneration || int64(jobAttempt) < admittedAttempt || work.generation < 0 || work.attempt < 0 || (work.generation == 0) != (work.attempt == 0) || (work.lease.Valid && (work.generation == 0 || work.lease.InfinityModifier != pgtype.Finite)) || (work.committed.Valid && work.committed.InfinityModifier != pgtype.Finite) {
		return tx, local, work, ErrStoredData
	}
	return tx, local, work, nil
}

func nativeClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, ErrUnavailable
	}
	return now, nil
}

func (s *Store) acquireNative(ctx context.Context, ref NativeActionRef, lease time.Duration, unfinishedOnly bool) (NativeActionClaim, bool, error) {
	tx, _, work, err := s.loadNative(ctx, ref)
	if err != nil {
		return NativeActionClaim{}, false, err
	}
	defer rollbackNative(tx)
	now, err := nativeClock(ctx, tx)
	if err != nil {
		return NativeActionClaim{}, false, err
	}
	if (unfinishedOnly && work.committed.Valid) || work.due.After(now) || (work.lease.Valid && work.lease.Time.After(now)) {
		return NativeActionClaim{}, false, nil
	}
	if work.generation == math.MaxInt64 || work.attempt == math.MaxInt64 {
		return NativeActionClaim{}, false, ErrLimit
	}
	claim := NativeActionClaim{ref: ref}
	err = tx.QueryRow(ctx, `UPDATE app.native_cart_actions SET generation=generation+1,attempt=attempt+1,lease_until=clock_timestamp()+($3::bigint*interval '1 microsecond') WHERE event_id=$1 AND runtime_slot=$2 RETURNING generation,attempt,lease_until`, ref.source.eventUUID, ref.slot, int64(lease/time.Microsecond)).Scan(&claim.generation, &claim.attempt, &claim.deadline)
	if err != nil {
		return NativeActionClaim{}, false, ErrUnavailable
	}
	if err = sourceContextError(ctx); err != nil {
		return NativeActionClaim{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return NativeActionClaim{}, false, ErrUnavailable
	}
	return claim, true, nil
}

// ExecuteNativeEdit requires a fresh business lease even for historical replay.
// Current AI mode/epoch and reply-job lease/state are deliberately not permission.
func (s *Store) ExecuteNativeEdit(parent context.Context, claim NativeActionClaim) (out Result, err error) {
	ctx, cancel, err := s.nativeContext(parent)
	if err != nil {
		return Result{}, err
	}
	defer cancel()
	defer func() {
		if expired := sourceContextError(ctx); expired != nil {
			err = expired
		}
		if err != nil {
			out = Result{}
		}
	}()
	if !validNativeRef(claim.ref) || claim.ref.source.restaurant != s.restaurant || claim.generation <= 0 || claim.attempt <= 0 || claim.deadline.IsZero() {
		return Result{}, foundation.ErrStaleLease
	}
	tx, local, work, err := s.loadNative(ctx, claim.ref)
	if err != nil {
		if err == ErrNotFound {
			err = foundation.ErrStaleLease
		}
		return Result{}, err
	}
	defer rollbackNative(tx)
	if work.generation != claim.generation || work.attempt != claim.attempt || !work.lease.Valid || !work.lease.Time.Equal(claim.deadline) {
		return Result{}, foundation.ErrStaleLease
	}
	// Capture the locked deadline BEFORE retirement. All later checks use this
	// immutable value, including the existing post-operation-insert barrier.
	deadline := work.lease.Time
	guard := func() error {
		now, err := nativeClock(ctx, tx)
		if err != nil {
			return err
		}
		if !deadline.After(now) {
			return foundation.ErrStaleLease
		}
		return sourceContextError(ctx)
	}
	if err = guard(); err != nil {
		return Result{}, err
	}
	history, found, err := s.replay(ctx, tx, local, claim.ref.operation, work.call.req)
	if err != nil {
		if err == ErrConflict {
			err = ErrStoredData
		}
		return Result{}, err
	}
	if work.committed.Valid != found {
		return Result{}, ErrStoredData
	}
	if err = s.stageResolvedNativeEdit(ctx, tx, local, claim.ref, work.call, found); err != nil {
		return Result{}, err
	}
	// Staging is not externally visible until the SAME transaction commits.
	// Existing apply owns that commit for a new result; domain failures roll
	// back this marker/retirement along with every provisional cart write.
	updated, err := tx.Exec(ctx, `UPDATE app.native_cart_actions SET committed_at=COALESCE(committed_at,clock_timestamp()),lease_until=NULL WHERE event_id=$1 AND runtime_slot=$2 AND generation=$3 AND attempt=$4`, claim.ref.source.eventUUID, claim.ref.slot, claim.generation, claim.attempt)
	if err != nil {
		return Result{}, ErrUnavailable
	}
	if updated.RowsAffected() != 1 {
		return Result{}, foundation.ErrStaleLease
	}
	if !found {
		return s.apply(ctx, tx, local, claim.ref.operation, CartID{work.call.req.Cart}, Version{work.call.req.Expected}, work.call.action, work.call.req, guard)
	}
	if err = guard(); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, ErrUnavailable
	}
	return history, nil
}

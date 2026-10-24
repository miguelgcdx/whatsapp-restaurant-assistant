// Package worker runs the bounded, local synthetic acknowledgement worker.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"restaurant-assistant/backend/internal/foundation"
)

type Sender interface {
	Send(context.Context, foundation.Delivery) foundation.SendResult
}
type Options struct {
	SocketPath       string
	RecoveryInterval time.Duration
	Lease            time.Duration
	SendTimeout      time.Duration
}

// Run owns its socket and timers, but not the supplied store. Store errors stop
// the process rather than hiding failure in an immediate retry loop. Durable
// leases and send intent remain the store's recovery authority.
func Run(ctx context.Context, store foundation.Store, sender Sender, opts Options) error {
	if ctx == nil || store == nil || sender == nil {
		return errors.New("worker requires context, store and sender")
	}
	if opts.RecoveryInterval == 0 {
		opts.RecoveryInterval = 30 * time.Minute
	}
	if opts.Lease == 0 {
		opts.Lease = 30 * time.Second
	}
	if opts.SendTimeout == 0 {
		opts.SendTimeout = 5 * time.Second
	}
	if opts.RecoveryInterval < 0 || opts.Lease < 0 || opts.SendTimeout < 0 || opts.SendTimeout >= opts.Lease {
		return errors.New("worker durations must be positive and send timeout shorter than lease")
	}
	if ctx.Err() != nil {
		return nil
	}
	socket, err := ownSocket(opts.SocketPath)
	if err != nil {
		return err
	}
	defer socket.close()
	hints := make(chan struct{}, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var b [1]byte
		for {
			if _, _, err := socket.conn.ReadFromUnix(b[:]); err != nil {
				return
			}
			select {
			case hints <- struct{}{}:
			default:
			}
		}
	}()
	defer func() { socket.conn.Close(); <-readerDone }()
	recovery := time.NewTicker(opts.RecoveryInterval)
	defer recovery.Stop()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var due <-chan time.Time
	drain := func() error {
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			job, found, err := store.ClaimJob(ctx, time.Now(), opts.Lease)
			if err != nil {
				return err
			}
			if found {
				if err := store.CompleteJob(ctx, job, "Local test acknowledgement.", time.Now()); err != nil {
					return err
				}
			}
			delivery, send, err := store.ClaimDelivery(ctx, time.Now(), opts.Lease)
			if err != nil {
				return err
			}
			if send {
				sendCtx, cancel := context.WithTimeout(ctx, opts.SendTimeout)
				result := sender.Send(sendCtx, delivery)
				cancel()
				if err := store.FinishDelivery(ctx, delivery, result, time.Now()); err != nil {
					return err
				}
			}
			if !found && !send {
				return nil
			}
		}
	}
	for {
		if err := drain(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("worker drain: %w", err)
		}
		when, exists, err := store.NextDue(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("worker next due: %w", err)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		due = nil
		if exists {
			delay := time.Until(when)
			// A deadline that raced another process must not become a hot DB loop.
			if delay < 10*time.Millisecond {
				delay = 10 * time.Millisecond
			}
			timer.Reset(delay)
			due = timer.C
		}
		select {
		case <-ctx.Done():
			return nil
		case <-hints:
		case <-due:
		case <-recovery.C:
		}
	}
}

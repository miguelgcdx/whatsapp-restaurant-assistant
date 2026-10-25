// Package preparation calculates provisional preparation-only estimates from
// explicit local workload snapshots. It does not update fulfillment statuses.
package preparation

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// State is a boundary value, validated by Calculate before modeling workload.
type State string

const (
	StateSubmitted      State = "submitted"
	StateAccepted       State = "accepted"
	StatePreparing      State = "preparing"
	StateReady          State = "ready"
	StateOutForDelivery State = "out_for_delivery"
	StateRejected       State = "rejected"
	StateCancelled      State = "cancelled"
	StatePickedUp       State = "picked_up"
	StateDelivered      State = "delivered"
)

// Entry is a boundary DTO, not a validated order or an identity. Sequence must
// be positive and unique among non-held accepted/preparing entries. StartedAt
// must be zero for accepted work and nonzero and no later than asOf for preparing
// work. Held entries and other known states do not contribute workload.
type Entry struct {
	State     State
	Sequence  int64
	StartedAt time.Time
	Held      bool
}

// Estimate describes modeled readiness of a prospective new order, not just
// its queue wait. AsOf is UTC; Provisional is always true.
type Estimate struct {
	Minutes     int64
	AsOf        time.Time
	Provisional bool
}

var (
	ErrInput    = errors.New("invalid preparation snapshot")
	ErrOverflow = errors.New("preparation estimate overflow")
)

const effort = 10 * time.Minute

// Calculate uses three parallel slots with ten minutes of uniform effort per
// order. Invalid snapshots return ErrInput (possibly wrapped). A valid snapshot
// containing any active preparation at or beyond ten minutes returns nil, nil:
// the estimate is unavailable until staff update the workload, not zero/ready.
// No input entry is changed, and the clock is exclusively the supplied asOf.
func Calculate(asOf time.Time, workload []Entry) (*Estimate, error) {
	model, err := parseSnapshot(asOf, workload)
	if err != nil {
		return nil, err
	}
	if model.overdue {
		return nil, nil
	}
	for i := 0; i < model.waiting; i++ {
		if _, err := assign(&model.slots); err != nil {
			return nil, err
		}
	}
	completion, err := assign(&model.slots)
	if err != nil {
		return nil, err
	}
	minutes := int64(completion / time.Minute)
	if completion%time.Minute != 0 {
		minutes++ // Division above leaves ample headroom for this rounding step.
	}
	return &Estimate{Minutes: minutes, AsOf: asOf.UTC(), Provisional: true}, nil
}

// snapshot contains only validated model inputs, with idle slots left at zero.
type snapshot struct {
	slots   [3]time.Duration
	waiting int
	overdue bool
}

func parseSnapshot(asOf time.Time, workload []Entry) (snapshot, error) {
	var model snapshot
	if asOf.IsZero() {
		return model, fmt.Errorf("%w: zero asOf", ErrInput)
	}
	sequences := make(map[int64]struct{})
	preparing := 0
	for i, entry := range workload {
		switch entry.State {
		case StateSubmitted, StateReady, StateOutForDelivery, StateRejected, StateCancelled, StatePickedUp, StateDelivered:
			continue
		case StateAccepted, StatePreparing:
			if entry.Held {
				continue
			}
		default:
			return model, fmt.Errorf("%w: entry %d has unknown state", ErrInput, i)
		}
		if entry.Sequence <= 0 {
			return model, fmt.Errorf("%w: entry %d has nonpositive active sequence", ErrInput, i)
		}
		if _, exists := sequences[entry.Sequence]; exists {
			return model, fmt.Errorf("%w: duplicate active sequence", ErrInput)
		}
		sequences[entry.Sequence] = struct{}{}
		if entry.State == StateAccepted {
			if !entry.StartedAt.IsZero() {
				return model, fmt.Errorf("%w: accepted entry %d has start time", ErrInput, i)
			}
			model.waiting++
			continue
		}
		if entry.StartedAt.IsZero() || entry.StartedAt.After(asOf) {
			return model, fmt.Errorf("%w: preparing entry %d has missing or future start", ErrInput, i)
		}
		if preparing == len(model.slots) {
			return model, fmt.Errorf("%w: more than three active preparing entries", ErrInput)
		}
		// Sub saturates for very old starts; such work is still overdue. Never
		// subtract saturated elapsed time from effort or release its occupied slot.
		elapsed := asOf.Sub(entry.StartedAt)
		if elapsed >= effort {
			model.overdue = true
		} else {
			model.slots[preparing] = effort - elapsed
		}
		preparing++
	}
	return model, nil
}

func assign(slots *[3]time.Duration) (time.Duration, error) {
	earliest := 0
	for i := 1; i < len(slots); i++ {
		if slots[i] < slots[earliest] {
			earliest = i
		}
	}
	if slots[earliest] > time.Duration(math.MaxInt64)-effort {
		return 0, ErrOverflow
	}
	slots[earliest] += effort
	return slots[earliest], nil
}

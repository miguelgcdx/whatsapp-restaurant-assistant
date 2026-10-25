// Package conversation persists staff-authenticated local control. A State is a
// sampled staff-visible value, not a model, job, or dispatch authorization permit.
package conversation

import (
	"errors"

	"restaurant-assistant/backend/internal/cart"
)

type Mode string

const (
	ModeAI    Mode = "ai"
	ModeStaff Mode = "staff"
)

func (m Mode) valid() bool { return m == ModeAI || m == ModeStaff }

type State struct {
	ConversationID cart.ConversationID
	Mode           Mode
	Version        int64
}

var (
	ErrInput       = errors.New("invalid conversation control input")
	ErrNotFound    = errors.New("conversation control not found")
	ErrForbidden   = errors.New("staff access is forbidden")
	ErrConflict    = errors.New("conversation control operation conflict")
	ErrStale       = errors.New("conversation control version conflict")
	ErrStoredData  = errors.New("invalid stored conversation control data")
	ErrUnavailable = errors.New("conversation control temporarily unavailable")
)

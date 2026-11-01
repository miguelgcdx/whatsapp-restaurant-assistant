package carteditflow_test

import (
	"errors"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/carteditflow"
)

func TestAIAmbiguityHostRejectsMissingConfiguration(t *testing.T) {
	for _, c := range []carteditflow.Config{{}, {NativeLease: time.Minute}} {
		h, err := carteditflow.New(c)
		if h != nil || !errors.Is(err, carteditflow.ErrInput) {
			t.Fatalf("host=%v error=%v", h, err)
		}
	}
}

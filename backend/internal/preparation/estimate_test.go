package preparation_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/preparation"
)

func TestCalculate(t *testing.T) {
	asOf := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	waiting := func(n int) []preparation.Entry {
		entries := make([]preparation.Entry, n)
		for i := range entries {
			entries[i] = preparation.Entry{State: preparation.StateAccepted, Sequence: int64(i + 1)}
		}
		return entries
	}
	preparing := func(sequence int64, elapsed time.Duration) preparation.Entry {
		return preparation.Entry{State: preparation.StatePreparing, Sequence: sequence, StartedAt: asOf.Add(-elapsed)}
	}
	type testCase struct {
		name        string
		asOf        time.Time
		workload    []preparation.Entry
		minutes     int64
		unavailable bool
		err         error
	}
	cases := []testCase{
		{name: "empty", asOf: asOf, minutes: 10},
		{name: "explicit non UTC as of", asOf: asOf.In(time.FixedZone("fixture", 2*60*60)), minutes: 10},
		{name: "six minutes preparing and three waiting", asOf: asOf, workload: append([]preparation.Entry{preparing(1, 6*time.Minute), preparing(2, 6*time.Minute), preparing(3, 6*time.Minute)}, preparation.Entry{State: preparation.StateAccepted, Sequence: 4}, preparation.Entry{State: preparation.StateAccepted, Sequence: 5}, preparation.Entry{State: preparation.StateAccepted, Sequence: 6}), minutes: 24},
		{name: "unequal starts use earliest slot", asOf: asOf, workload: []preparation.Entry{preparing(1, 9*time.Minute), preparing(2, 5*time.Minute), preparing(3, time.Minute), {State: preparation.StateAccepted, Sequence: 4}, {State: preparation.StateAccepted, Sequence: 5}}, minutes: 19},
		{name: "idle slots remain usable", asOf: asOf, workload: []preparation.Entry{preparing(1, time.Minute)}, minutes: 10},
		{name: "exact overdue", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute)}, unavailable: true},
		{name: "later overdue", asOf: asOf, workload: []preparation.Entry{preparing(1, 11*time.Minute)}, unavailable: true},
		{name: "ancient start is overdue", asOf: asOf, workload: []preparation.Entry{{State: preparation.StatePreparing, Sequence: 1, StartedAt: time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)}}, unavailable: true},
		{name: "just before overdue rounds up", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute-time.Nanosecond), preparing(2, 10*time.Minute-time.Nanosecond), preparing(3, 10*time.Minute-time.Nanosecond)}, minutes: 11},
		{name: "fractional minutes round up", asOf: asOf, workload: []preparation.Entry{preparing(1, 6*time.Minute+30*time.Second), preparing(2, 6*time.Minute+30*time.Second), preparing(3, 6*time.Minute+30*time.Second)}, minutes: 14},
		{name: "start at as of", asOf: asOf, workload: []preparation.Entry{preparing(1, 0), preparing(2, 0), preparing(3, 0)}, minutes: 20},
		{name: "zero as of", err: preparation.ErrInput},
		{name: "unknown state", asOf: asOf, workload: []preparation.Entry{{State: "unknown"}}, err: preparation.ErrInput},
		{name: "empty state", asOf: asOf, workload: []preparation.Entry{{}}, err: preparation.ErrInput},
		{name: "held unknown state", asOf: asOf, workload: []preparation.Entry{{State: "unknown", Held: true}}, err: preparation.ErrInput},
		{name: "accepted zero sequence", asOf: asOf, workload: []preparation.Entry{{State: preparation.StateAccepted}}, err: preparation.ErrInput},
		{name: "accepted negative sequence", asOf: asOf, workload: []preparation.Entry{{State: preparation.StateAccepted, Sequence: -1}}, err: preparation.ErrInput},
		{name: "preparing zero sequence", asOf: asOf, workload: []preparation.Entry{preparing(0, time.Minute)}, err: preparation.ErrInput},
		{name: "preparing negative sequence", asOf: asOf, workload: []preparation.Entry{preparing(-1, time.Minute)}, err: preparation.ErrInput},
		{name: "duplicate accepted sequences", asOf: asOf, workload: []preparation.Entry{{State: preparation.StateAccepted, Sequence: 1}, {State: preparation.StateAccepted, Sequence: 1}}, err: preparation.ErrInput},
		{name: "duplicate across active states", asOf: asOf, workload: []preparation.Entry{preparing(1, time.Minute), {State: preparation.StateAccepted, Sequence: 1}}, err: preparation.ErrInput},
		{name: "duplicate preparing sequences", asOf: asOf, workload: []preparation.Entry{preparing(1, time.Minute), preparing(1, 2*time.Minute)}, err: preparation.ErrInput},
		{name: "accepted unexpected start", asOf: asOf, workload: []preparation.Entry{{State: preparation.StateAccepted, Sequence: 1, StartedAt: asOf}}, err: preparation.ErrInput},
		{name: "preparing missing start", asOf: asOf, workload: []preparation.Entry{{State: preparation.StatePreparing, Sequence: 1}}, err: preparation.ErrInput},
		{name: "preparing future start", asOf: asOf, workload: []preparation.Entry{preparing(1, -time.Nanosecond)}, err: preparation.ErrInput},
		{name: "more than three preparing", asOf: asOf, workload: []preparation.Entry{preparing(1, time.Minute), preparing(2, time.Minute), preparing(3, time.Minute), preparing(4, time.Minute)}, err: preparation.ErrInput},
		{name: "invalid after overdue", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute), {State: "unknown"}}, err: preparation.ErrInput},
		{name: "invalid before overdue", asOf: asOf, workload: []preparation.Entry{{State: "unknown"}, preparing(1, 10*time.Minute)}, err: preparation.ErrInput},
		{name: "overdue still validates sequences", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute), {State: preparation.StateAccepted, Sequence: 1}}, err: preparation.ErrInput},
		{name: "overdue still validates times", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute), preparing(2, -time.Second)}, err: preparation.ErrInput},
		{name: "overdue still validates cardinality", asOf: asOf, workload: []preparation.Entry{preparing(1, 10*time.Minute), preparing(2, time.Minute), preparing(3, time.Minute), preparing(4, time.Minute)}, err: preparation.ErrInput},
		{name: "sequence maximum is not a quantity cap", asOf: asOf, workload: []preparation.Entry{{State: preparation.StateAccepted, Sequence: 1<<63 - 1}}, minutes: 10},
	}
	for position := 1; position <= 12; position++ {
		cases = append(cases, testCase{name: fmt.Sprintf("baseline position %d", position), asOf: asOf, workload: waiting(position - 1), minutes: int64((position + 2) / 3 * 10)})
	}
	for _, state := range []preparation.State{preparation.StateSubmitted, preparation.StateReady, preparation.StateOutForDelivery, preparation.StateRejected, preparation.StateCancelled, preparation.StatePickedUp, preparation.StateDelivered} {
		cases = append(cases, testCase{name: "excluded " + string(state), asOf: asOf, workload: []preparation.Entry{{State: state, Sequence: -1, StartedAt: asOf.Add(time.Hour)}, {State: state, Sequence: -1}, {State: preparation.StateAccepted, Sequence: 1}, {State: preparation.StateAccepted, Sequence: 2}, {State: preparation.StateAccepted, Sequence: 3}}, minutes: 20})
	}
	for _, state := range []preparation.State{preparation.StateAccepted, preparation.StatePreparing} {
		cases = append(cases, testCase{name: "held " + string(state), asOf: asOf, workload: []preparation.Entry{{State: state, Held: true, Sequence: -1, StartedAt: asOf.Add(time.Hour)}, {State: state, Held: true}, {State: state, Held: true, StartedAt: asOf.Add(-time.Hour)}, {State: preparation.StateAccepted, Sequence: 1}, {State: preparation.StateAccepted, Sequence: 2}, {State: preparation.StateAccepted, Sequence: 3}}, minutes: 20})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := clone(tc.workload)
			var want *preparation.Estimate
			if tc.err == nil && !tc.unavailable {
				want = &preparation.Estimate{Minutes: tc.minutes, AsOf: tc.asOf.UTC(), Provisional: true}
			}
			// Repeat the same public call, then reverse a copy to check ordering independence.
			reversed := clone(tc.workload)
			for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
				reversed[i], reversed[j] = reversed[j], reversed[i]
			}
			for _, input := range [][]preparation.Entry{tc.workload, tc.workload, reversed} {
				inputBefore := clone(input)
				got, err := preparation.Calculate(tc.asOf, input)
				if !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want identity %v", err, tc.err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("estimate = %#v, want %#v", got, want)
				}
				if !reflect.DeepEqual(input, inputBefore) {
					t.Fatal("Calculate mutated input")
				}
			}
			if !reflect.DeepEqual(tc.workload, before) {
				t.Fatal("original workload changed")
			}
		})
	}
}

func clone(entries []preparation.Entry) []preparation.Entry {
	if entries == nil {
		return nil
	}
	return append(make([]preparation.Entry, 0, len(entries)), entries...)
}

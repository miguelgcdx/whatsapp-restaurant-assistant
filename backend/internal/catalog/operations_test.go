package catalog_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

func TestAtomicAvailability(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	cfg, items := fixture()
	if err := s.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatal(err)
	}
	members, err := staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	for _, subject := range []string{"alex", "sam"} {
		if _, err := members.EnsureMember(t.Context(), subject); err != nil {
			t.Fatal(err)
		}
	}
	intent := catalog.AvailabilityIntent{Subject: "alex", Key: "one", RequestID: "original-request", ItemID: items[0].ID, ExpectedVersion: 0, Value: false}
	first, err := s.ApplyAvailability(t.Context(), intent, members.Access)
	if err != nil || !bytes.Contains(first, []byte(`"version":"1"`)) {
		t.Fatalf("first: %s %v", first, err)
	}
	changed := intent
	changed.Key = "two"
	changed.ExpectedVersion = 1
	changed.Value = true
	if _, err := s.ApplyAvailability(t.Context(), changed, members.Access); err != nil {
		t.Fatal(err)
	}
	replay := intent
	replay.RequestID = "new-request"
	got, err := s.ApplyAvailability(t.Context(), replay, members.Access)
	if err != nil || !bytes.Equal(first, got) {
		t.Fatalf("replay: %s %v", got, err)
	}
	changed = intent
	changed.Value = true
	if got, err := s.ApplyAvailability(t.Context(), changed, members.Access); len(got) != 0 || err != catalog.ErrIdempotencyConflict {
		t.Fatalf("changed body: %s %v", got, err)
	}
	changed.Key = "stale"
	if _, err := s.ApplyAvailability(t.Context(), changed, members.Access); err != catalog.ErrStale {
		t.Fatal(err)
	}
	// Rejected intent never reserves the key: corrected version can use it.
	changed.ExpectedVersion = 2
	if _, err := s.ApplyAvailability(t.Context(), changed, members.Access); err != nil {
		t.Fatal(err)
	}
	// Actor and resource are independent key scopes.
	changed.Subject = "sam"
	changed.Key = "one"
	changed.ExpectedVersion = 3
	if _, err := s.ApplyAvailability(t.Context(), changed, members.Access); err != nil {
		t.Fatal(err)
	}
	settingsIntent := catalog.AvailabilityIntent{Subject: "alex", Key: "one", RequestID: "settings-request", Value: false}
	if _, err := s.ApplyAvailability(t.Context(), settingsIntent, members.Access); err != nil {
		t.Fatal(err)
	}
	if err := s.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatalf("create-only retry after closure: %v", err)
	}
	// Same body/key remains independently scoped to another configured restaurant.
	foreign := open(t, dsn, other)
	if err := foreign.Initialize(t.Context(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.ApplyAvailability(t.Context(), settingsIntent, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	// Settings replay also ignores later state and stale expected versions.
	originalSettings, err := s.ApplyAvailability(t.Context(), settingsIntent, members.Access)
	if err != nil {
		t.Fatal(err)
	}
	newerSettings := settingsIntent
	newerSettings.Key = "settings-next"
	newerSettings.ExpectedVersion = 1
	newerSettings.Value = true
	if _, err := s.ApplyAvailability(t.Context(), newerSettings, members.Access); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.ApplyAvailability(t.Context(), settingsIntent, members.Access); err != nil || !bytes.Equal(originalSettings, replay) {
		t.Fatalf("settings replay %s %v", replay, err)
	}
	different := settingsIntent
	different.Value = true
	if _, err := s.ApplyAvailability(t.Context(), different, members.Access); err != catalog.ErrIdempotencyConflict {
		t.Fatal(err)
	}
	newerSettings.Key = "settings-close"
	newerSettings.ExpectedVersion = 2
	newerSettings.Value = false
	if _, err := s.ApplyAvailability(t.Context(), newerSettings, members.Access); err != nil {
		t.Fatal(err)
	}
	stored, menu := state(t, s)
	if stored.Open || stored.Version != 3 || menu[0].Version != 4 || !menu[0].Available {
		t.Fatalf("state: %+v %+v", stored, menu)
	}
	if err := members.RevokeMember(t.Context(), "alex"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ApplyAvailability(t.Context(), intent, members.Access); len(got) != 0 || err != catalog.ErrForbidden {
		t.Fatalf("revoked replay: %s %v", got, err)
	}
	equalState(t, s, stored, menu)
	if active, err := members.EnsureMember(t.Context(), "alex"); err != nil || active {
		t.Fatalf("reactivation: %t %v", active, err)
	}
	if err := s.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatal(err)
	}
	equalState(t, s, stored, menu)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ApplyAvailability(ctx, intent, members.Access); err != catalog.ErrUnavailable {
		t.Fatalf("canceled: %v", err)
	}
}

func TestConcurrentAvailabilityOperations(t *testing.T) {
	dsn := testpg.Start(t)
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	s := open(t, dsn, restaurant)
	cfg, items := fixture()
	if err := s.Initialize(t.Context(), cfg, items); err != nil {
		t.Fatal(err)
	}
	authorize := func(context.Context, string) (bool, error) { return true, nil }
	run := func(sameKey bool, version int64) {
		var wg sync.WaitGroup
		bodies := make([][]byte, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range 2 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				key := "duplicate"
				if !sameKey {
					key = []string{"a", "b"}[i]
				}
				bodies[i], errs[i] = s.ApplyAvailability(t.Context(), catalog.AvailabilityIntent{Subject: "alex", Key: key, RequestID: []string{"first", "second"}[i], ItemID: items[0].ID, ExpectedVersion: version, Value: false}, authorize)
			}(i)
		}
		close(start)
		wg.Wait()
		if sameKey {
			if errs[0] != nil || errs[1] != nil || !bytes.Equal(bodies[0], bodies[1]) {
				t.Fatalf("duplicates: %s %s %v", bodies[0], bodies[1], errs)
			}
		} else {
			wins := 0
			for _, err := range errs {
				if err == nil {
					wins++
				} else if !errors.Is(err, catalog.ErrStale) {
					t.Fatal(err)
				}
			}
			if wins != 1 {
				t.Fatalf("winners: %v", errs)
			}
		}
	}
	run(true, 0)
	run(false, 1)
	_, menu := state(t, s)
	if menu[0].Version != 2 {
		t.Fatal(menu)
	}
}

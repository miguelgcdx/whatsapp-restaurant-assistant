package staff_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const restaurant = "12345678-1234-4234-8234-123456789abc"
const otherRestaurant = "22345678-1234-4234-8234-123456789abc"

func TestMembershipLifecycleAndNoRuntimeMigration(t *testing.T) {
	dsn := testpg.Start(t)
	s, err := staff.OpenStore(t.Context(), dsn, restaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Access(t.Context(), "alex"); err == nil {
		t.Fatal("runtime created schema")
	}
	if _, err := s.EnsureMember(t.Context(), "alex"); err == nil {
		t.Fatal("operator implicitly migrated")
	}
	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	f, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	now := time.Now()
	event := foundation.Event{ID: "preserved", Sender: "fictional", Text: "local"}
	if _, err := f.Accept(t.Context(), event, now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := f.CompleteJob(t.Context(), job, "local reply", now); err != nil {
		t.Fatal(err)
	}
	before, err := f.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := staff.OpenStore(t.Context(), dsn, otherRestaurant)
	if err != nil {
		t.Fatal(err)
	}
	defer cross.Close()
	for range 2 {
		if active, err := s.EnsureMember(t.Context(), "alex"); err != nil || !active {
			t.Fatalf("ensure %v %v", active, err)
		}
	}
	if active, err := cross.Access(t.Context(), "alex"); err != nil || active {
		t.Fatalf("cross %v %v", active, err)
	}
	if active, err := s.Access(t.Context(), "missing"); err != nil || active {
		t.Fatalf("missing %v %v", active, err)
	}
	for range 2 {
		if err := s.RevokeMember(t.Context(), "alex"); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeMember(t.Context(), "missing"); err != nil {
			t.Fatal(err)
		}
	}
	if active, err := s.EnsureMember(t.Context(), "alex"); err != nil || active {
		t.Fatalf("reactivation %v %v", active, err)
	}
	for _, subject := range []string{"", " ", strings.Repeat("x", 257)} {
		if _, err := s.EnsureMember(t.Context(), subject); err == nil {
			t.Fatal("invalid subject accepted")
		}
	}
	after, err := f.Inspect(t.Context(), event.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("foundation changed: %+v %v", after, err)
	}
}
func TestStoreBoundary(t *testing.T) {
	for _, id := range []string{"bad", strings.ToUpper(restaurant), "12345678123442348234123456789abc"} {
		if s, err := staff.OpenStore(t.Context(), "postgres://test@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", id); err == nil {
			s.Close()
			t.Fatal("invalid UUID accepted")
		}
	}
	if _, err := staff.OpenStore(t.Context(), "postgres://secret-sentinel@192.0.2.1/postgres", restaurant); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("unsafe/redaction %v", err)
	}
	s, err := staff.OpenStore(t.Context(), "postgres://test@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", restaurant)
	if err != nil {
		t.Fatal("not lazy", err)
	}
	s.Close()
}

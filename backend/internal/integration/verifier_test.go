package integration_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

// S1/T4: exercise real CLI exits, exact streams and non-destructive reapplication.
func TestVerifierMigrationBinaryContract(t *testing.T) {
	bin := build(t, t.Context(), "local-migrate")
	invoke := func(dsn string, code int, stdout, stderr string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = append(os.Environ(), "LOCAL_DATABASE_URL="+dsn)
		var out, diagnostic bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diagnostic
		err := cmd.Run()
		actual := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			actual = exit.ExitCode()
		}
		if actual != code || out.String() != stdout || diagnostic.String() != stderr {
			t.Fatalf("exit=%d stdout=%q stderr=%q", actual, out.String(), diagnostic.String())
		}
	}
	invoke("", 0, "", "Usage: local-migrate\nApply embedded foundation migrations to a dedicated numeric-loopback test database.\nRequired environment: LOCAL_DATABASE_URL\nNo database creation or destructive rollback.\n", "-help")
	invoke("", 1, "", "LOCAL_DATABASE_URL is required\n")
	invoke("postgres://user:private@192.0.2.1/restaurant_foundation_test", 1, "", "local foundation migration failed; check dedicated local database configuration\n")
	invoke("", 1, "", "unexpected positional arguments\n", "extra")
	dsn := testpg.Start(t)
	invoke(dsn, 0, "local foundation migrations applied\n", "")
	s, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := foundation.Event{ID: "migrate-completed", Sender: "same", Text: "exact"}
	if ok, err := s.Accept(t.Context(), event, now); err != nil || !ok {
		t.Fatalf("accept=%v %v", ok, err)
	}
	job, ok, err := s.ClaimJob(t.Context(), now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	if err := s.CompleteJob(t.Context(), job, "reply", now); err != nil {
		t.Fatal(err)
	}
	before, err := s.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	invoke(dsn, 0, "local foundation migrations applied\n", "")
	after, err := s.Inspect(t.Context(), event.ID)
	if err != nil || after != before || after.OutboxCount != 1 {
		t.Fatalf("migration changed completed record: %+v -> %+v %v", before, after, err)
	}
}

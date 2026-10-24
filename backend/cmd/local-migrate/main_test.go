package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/foundation"
	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/testpg"
)

func TestConfigurationAndHelp(t *testing.T) {
	for _, tt := range []struct {
		name      string
		args      []string
		dsn, want string
	}{
		{name: "missing database", want: "LOCAL_DATABASE_URL is required"},
		{name: "unsafe database redacted", dsn: "postgres://user:private@192.0.2.1/unsafe", want: "local foundation migration failed; check dedicated local database configuration"},
		{name: "positional argument", args: []string{"extra"}, want: "unexpected positional arguments"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := run(t.Context(), tt.args, func(string) string { return tt.dsn }, &out, &diagnostic)
			if err == nil || err.Error() != tt.want || out.Len() != 0 || diagnostic.Len() != 0 {
				t.Fatalf("run = %v; stdout=%q stderr=%q", err, out.String(), diagnostic.String())
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("credential disclosed")
			}
		})
	}
	var out, diagnostic bytes.Buffer
	err := run(t.Context(), []string{"-help"}, func(string) string { t.Fatal("help read environment"); return "" }, &out, &diagnostic)
	want := "Usage: local-migrate\nApply embedded foundation migrations to a dedicated numeric-loopback test database.\nRequired environment: LOCAL_DATABASE_URL\nNo database creation or destructive rollback.\n"
	if err != nil || out.Len() != 0 || diagnostic.String() != want {
		t.Fatalf("help = %v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
	}
}

func TestExplicitMigrationPreservesExistingEvents(t *testing.T) {
	dsn := testpg.Start(t)
	store, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := foundation.Event{ID: "setup-event", Sender: "local", Text: "exact test text"}
	if _, err := store.Accept(t.Context(), event, time.Now()); err == nil {
		t.Fatal("Open implicitly migrated database")
	}
	migrate := func() {
		t.Helper()
		var out, diagnostic bytes.Buffer
		if err := run(context.Background(), nil, func(string) string { return dsn }, &out, &diagnostic); err != nil || out.String() != "local foundation migrations applied\n" || diagnostic.Len() != 0 {
			t.Fatalf("migration=%v stdout=%q stderr=%q", err, out.String(), diagnostic.String())
		}
	}
	migrate()
	if created, err := store.Accept(t.Context(), event, time.Now()); err != nil || !created {
		t.Fatalf("accept=%v %v", created, err)
	}
	before, err := store.Inspect(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	migrate()
	after, err := store.Inspect(t.Context(), event.ID)
	if err != nil || after != before || after.JobCount != 1 || after.OutboxCount != 0 {
		t.Fatalf("migration changed event: %+v -> %+v %v", before, after, err)
	}
}

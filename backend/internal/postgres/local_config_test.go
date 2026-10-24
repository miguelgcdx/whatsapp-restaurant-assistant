package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"restaurant-assistant/backend/internal/postgres"
)

func TestLocalPoolConfigPreservesFoundationGuard(t *testing.T) {
	for _, dsn := range []string{
		"", " ", "not a valid DSN",
		"postgres://test:private@localhost/restaurant_foundation_staff",
		"postgres://test:private@127.0.0.1/restaurant_foundation_",
		"postgres://test:private@127.0.0.1/other_database",
		"postgres://test:private@127.0.0.1,192.0.2.1/restaurant_foundation_staff",
	} {
		cfg, err := postgres.LocalPoolConfig(dsn)
		if cfg != nil || !errors.Is(err, postgres.ErrUnsafeDSN) {
			t.Fatal("shared pool configuration did not reject unsafe input")
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatal("guard error disclosed credentials")
		}
		// Existing constructor must retain the same rejection behavior.
		store, err := postgres.Open(t.Context(), dsn)
		if store != nil || !errors.Is(err, postgres.ErrUnsafeDSN) {
			t.Fatal("existing Open guard changed")
		}
	}

	const dsn = "postgres://test:private@127.0.0.1:1/restaurant_foundation_staff?sslmode=disable"
	cfg, err := postgres.LocalPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 4 || cfg.MinConns != 0 || cfg.MinIdleConns != 0 || cfg.MaxConnIdleTime != time.Second || cfg.HealthCheckPeriod != time.Second || cfg.ConnConfig.ConnectTimeout != 5*time.Second || cfg.ConnConfig.RuntimeParams["statement_timeout"] != "5000" {
		t.Fatal("shared configuration changed bounded idle/connection/statement settings")
	}
	// No server listens on port 1. Both paths remain lazy and never migrate.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal("existing lazy Open unexpectedly connected")
	}
	store.Close()
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"missing database", nil, map[string]string{"SYNTHETIC_WEBHOOK_SECRET": strings.Repeat("s", 32)}},
		{"short secret", nil, map[string]string{"LOCAL_DATABASE_URL": "postgres://127.0.0.1/restaurant_foundation_test", "SYNTHETIC_WEBHOOK_SECRET": "private"}},
		{"hostname", []string{"-listen", "localhost:8080"}, nil},
		{"wildcard", []string{"-listen", ":8080"}, nil},
		{"remote", []string{"-listen", "192.0.2.1:8080"}, nil},
		{"invalid port", []string{"-listen", "127.0.0.1:65536"}, nil},
		{"arguments", []string{"unexpected"}, nil},
		{"empty socket", []string{"-wake-socket", ""}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := configuration(tt.args, func(k string) string { return tt.env[k] }, &out)
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("secret disclosed")
			}
		})
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:8080"} {
		cfg, err := configuration([]string{"-listen", addr}, func(k string) string {
			if k == "LOCAL_DATABASE_URL" {
				return "postgres://127.0.0.1/restaurant_foundation_test"
			}
			return strings.Repeat("s", 32)
		}, &bytes.Buffer{})
		if err != nil || cfg.listen != addr || cfg.socket != ".cache/worker.sock" {
			t.Fatalf("valid config: %+v %v", cfg, err)
		}
	}
	var help bytes.Buffer
	_, _ = configuration([]string{"-help"}, func(string) string { return "" }, &help)
	for _, want := range []string{"-listen", "-wake-socket", "loopback", "Unix"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("help missing %s: %s", want, help.String())
		}
	}
}

func TestRunCancellationAndUnsafeDatabase(t *testing.T) {
	getenv := func(k string) string {
		if k == "LOCAL_DATABASE_URL" {
			return "postgres://localtest@127.0.0.1:1/restaurant_foundation_test?sslmode=disable"
		}
		return strings.Repeat("s", 32)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Opening is lazy: a canceled process can stop cleanly without any database
	// server, implicit migrations or readiness queries.
	if err := run(ctx, []string{"-listen", "127.0.0.1:0"}, getenv, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), nil, func(k string) string {
		if k == "LOCAL_DATABASE_URL" {
			return "postgres://user:private@192.0.2.1/unsafe"
		}
		return strings.Repeat("s", 32)
	}, &bytes.Buffer{})
	if err == nil || err.Error() != "invalid local database configuration" {
		t.Fatalf("unsafe database: %v", err)
	}
}

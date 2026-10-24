package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{{"missing", nil, nil}, {"unsafe provider", nil, map[string]string{"LOCAL_DATABASE_URL": "redacted", "FAKE_PROVIDER_URL": "https://example.com"}}, {"negative interval", []string{"-recovery-interval=-1s"}, nil}, {"unknown flag", []string{"-bogus"}, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), tc.args, func(k string) string { return tc.env[k] }, &out)
			if err == nil {
				t.Fatal("accepted invalid config")
			}
			if strings.Contains(err.Error(), "redacted") {
				t.Fatal("disclosed configuration")
			}
		})
	}
}
func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, func(string) string { t.Fatal("help read environment"); return "" }, &out); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"wake-socket", "recovery-interval"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatal("missing help", flag)
		}
	}
}

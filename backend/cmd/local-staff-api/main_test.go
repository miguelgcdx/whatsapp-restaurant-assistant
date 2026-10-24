package main

import (
	"bytes"
	"context"
	"testing"
)

func TestAPIBoundariesAndHelp(t *testing.T) {
	valid := map[string]string{"LOCAL_DATABASE_URL": "postgres://fictional@127.0.0.1:1/restaurant_foundation_test?sslmode=disable", "LOCAL_RESTAURANT_ID": "12345678-1234-4234-8234-123456789abc", "STAFF_JWT_ISSUER": "http://127.0.0.1:3000", "STAFF_JWKS_URL": "http://127.0.0.1:3000/api/auth/jwks"}
	for _, args := range [][]string{{"-listen", "0.0.0.0:0"}, {"-listen", "localhost:8081"}, {"-listen", "127.0.0.1:65536"}, {"-listen", "127.0.0.1:-1"}, {"-unknown=secret-sentinel"}, {"secret-sentinel"}} {
		var out, errout bytes.Buffer
		code := execute(t.Context(), args, func(k string) string { return valid[k] }, &out, &errout)
		if code != 1 || out.Len() != 0 || errout.String() != "Staff API could not be completed.\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
		}
	}
	for _, key := range []string{"LOCAL_DATABASE_URL", "LOCAL_RESTAURANT_ID", "STAFF_JWT_ISSUER", "STAFF_JWKS_URL"} {
		t.Run(key, func(t *testing.T) {
			var out, errout bytes.Buffer
			code := execute(t.Context(), nil, func(k string) string {
				if k == key {
					return "secret-sentinel"
				}
				return valid[k]
			}, &out, &errout)
			if code != 1 || out.Len() != 0 || errout.String() != "Staff API could not be completed.\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
			}
		})
	}
	var out, errout bytes.Buffer
	code := execute(t.Context(), []string{"-help"}, func(string) string { t.Fatal("help read environment"); return "" }, &out, &errout)
	if code != 0 || out.String() != apiHelp || errout.Len() != 0 {
		t.Fatalf("help %d %q %q", code, out.String(), errout.String())
	}
	// No DB/JWKS is listening: canceled runtime must bind/shut down without probing,
	// provisioning or migrating dependencies.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out.Reset()
	errout.Reset()
	code = execute(ctx, []string{"-listen", "127.0.0.1:0"}, func(k string) string { return valid[k] }, &out, &errout)
	if code != 0 || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("lazy/shutdown %d %q %q", code, out.String(), errout.String())
	}
}

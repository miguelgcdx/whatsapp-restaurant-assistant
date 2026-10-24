package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/testpg"
)

const rid = "12345678-1234-4234-8234-123456789abc"

func TestOperatorCLI(t *testing.T) {
	dsn := testpg.Start(t)
	env := func(k string) string {
		if k == "LOCAL_DATABASE_URL" {
			return dsn
		}
		return ""
	}
	invoke := func(args []string, wantCode int, wantOut, wantErr string) {
		t.Helper()
		var out, errout bytes.Buffer
		code := execute(t.Context(), args, env, &out, &errout)
		if code != wantCode || out.String() != wantOut || errout.String() != wantErr {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
		}
	}
	args := []string{"-restaurant-id", rid, "-auth-user-id", "fictional-subject", "-action", "ensure"}
	invoke(args, 1, "", "Staff membership could not be completed.\n")
	if e := postgres.Migrate(t.Context(), dsn); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		invoke(args, 0, fmt.Sprintf("{\"restaurant_id\":%q,\"auth_user_id\":\"fictional-subject\",\"active\":true}\n", rid), "")
	}
	args[len(args)-1] = "revoke"
	for range 2 {
		invoke(args, 0, fmt.Sprintf("{\"restaurant_id\":%q,\"auth_user_id\":\"fictional-subject\",\"active\":false}\n", rid), "")
	}
	args[len(args)-1] = "ensure"
	invoke(args, 0, fmt.Sprintf("{\"restaurant_id\":%q,\"auth_user_id\":\"fictional-subject\",\"active\":false}\n", rid), "")
	s, e := staff.OpenStore(t.Context(), dsn, rid)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if active, e := s.Access(t.Context(), "fictional-subject"); e != nil || active {
		t.Fatal("CLI reactivated", e)
	}
}
func TestOperatorHelpAndRedaction(t *testing.T) {
	for _, args := range [][]string{{}, {"-action", "secret-sentinel"}, {"-unknown=secret-sentinel"}, {"-restaurant-id", rid, "-auth-user-id", "x", "-action", "ensure"}, {"secret-sentinel"}} {
		var out, errout bytes.Buffer
		code := execute(context.Background(), args, func(string) string { return "postgres://secret-sentinel@192.0.2.1/restaurant_foundation_test" }, &out, &errout)
		if code != 1 || out.Len() != 0 || errout.String() != "Staff membership could not be completed.\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	code := execute(t.Context(), []string{"-help"}, func(string) string { t.Fatal("help read environment"); return "" }, &out, &errout)
	if code != 0 || errout.Len() != 0 || out.String() != membershipHelp {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
	}
	for _, word := range []string{"-restaurant-id", "-auth-user-id", "ensure", "revoke", "reactivate"} {
		if !strings.Contains(out.String(), word) {
			t.Fatal("help missing", word)
		}
	}
}

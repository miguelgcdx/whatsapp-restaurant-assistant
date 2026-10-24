// local-staff-membership explicitly ensures or revokes an opaque staff subject.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"restaurant-assistant/backend/internal/staff"
)

const membershipHelp = `Usage: local-staff-membership -restaurant-id UUID -auth-user-id SUBJECT -action ensure|revoke
Explicit local fixture membership operation; LOCAL_DATABASE_URL is required.
  -restaurant-id UUID   canonical configured restaurant identity
  -auth-user-id SUBJECT opaque auth subject (1–256 bytes)
  -action ensure|revoke ensure absent membership or revoke idempotently
Ensure never reactivates revoked membership (does not reactivate).
No migrations, passwords, auth-private reads or runtime seeding.
`

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("local-staff-membership", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var restaurant, subject, action string
	fs.StringVar(&restaurant, "restaurant-id", "", "")
	fs.StringVar(&subject, "auth-user-id", "", "")
	fs.StringVar(&action, "action", "", "")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || (action != "ensure" && action != "revoke") {
		return staff.ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	store, e := staff.OpenStore(ctx, getenv("LOCAL_DATABASE_URL"), restaurant)
	if e != nil {
		return e
	}
	defer store.Close()
	var active bool
	if action == "ensure" {
		active, e = store.EnsureMember(ctx, subject)
	} else {
		e = store.RevokeMember(ctx, subject)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(struct {
		RestaurantID string `json:"restaurant_id"`
		AuthUserID   string `json:"auth_user_id"`
		Active       bool   `json:"active"`
	}{restaurant, subject, active})
}
func execute(ctx context.Context, args []string, getenv func(string) string, out, errout io.Writer) int {
	e := run(ctx, args, getenv, out)
	if errors.Is(e, flag.ErrHelp) {
		_, _ = io.WriteString(out, membershipHelp)
		return 0
	}
	if e != nil {
		_, _ = fmt.Fprintln(errout, "Staff membership could not be completed.")
		return 1
	}
	return 0
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := execute(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

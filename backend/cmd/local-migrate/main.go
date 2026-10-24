// local-migrate explicitly applies embedded migrations only to a dedicated local database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"restaurant-assistant/backend/internal/postgres"
)

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("local-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, "Usage: local-migrate\nApply embedded foundation migrations to a dedicated numeric-loopback test database.\nRequired environment: LOCAL_DATABASE_URL\nNo database creation or destructive rollback.\n")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid migration flags")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	dsn := getenv("LOCAL_DATABASE_URL")
	if strings.TrimSpace(dsn) == "" {
		return errors.New("LOCAL_DATABASE_URL is required")
	}
	if err := postgres.Migrate(ctx, dsn); err != nil {
		return errors.New("local foundation migration failed; check dedicated local database configuration")
	}
	_, err := fmt.Fprintln(stdout, "local foundation migrations applied")
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

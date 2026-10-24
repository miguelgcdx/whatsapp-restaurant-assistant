// local-worker runs only the local synthetic acknowledgement slice.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"restaurant-assistant/backend/internal/postgres"
	"restaurant-assistant/backend/internal/worker"
)

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	flags := flag.NewFlagSet("local-worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("wake-socket", ".cache/worker.sock", "Unix datagram wake socket in a private same-user directory")
	recovery := flags.Duration("recovery-interval", 30*time.Minute, "positive interval for durable recovery scans (no idle polling)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid worker flags")
	}
	if flags.NArg() != 0 || *recovery <= 0 || *socket == "" {
		return errors.New("invalid worker configuration")
	}
	dsn, endpoint := getenv("LOCAL_DATABASE_URL"), getenv("FAKE_PROVIDER_URL")
	if dsn == "" || endpoint == "" {
		return errors.New("LOCAL_DATABASE_URL and FAKE_PROVIDER_URL are required")
	}
	sender, err := worker.NewFakeSender(endpoint)
	if err != nil {
		return errors.New("invalid FAKE_PROVIDER_URL: numeric-loopback HTTP URL required")
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		return errors.New("invalid local database configuration")
	}
	defer store.Close()
	if err := worker.Run(ctx, store, sender, worker.Options{SocketPath: *socket, RecoveryInterval: *recovery}); err != nil {
		return errors.New("local worker stopped: check local configuration and persistence availability")
	}
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

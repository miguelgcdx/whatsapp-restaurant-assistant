// local-api runs the loopback-only synthetic callback service, without migrations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"restaurant-assistant/backend/internal/api"
	"restaurant-assistant/backend/internal/postgres"
)

type config struct {
	listen, socket, dsn string
	secret              []byte
}

func configuration(args []string, getenv func(string) string, out io.Writer) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("local-api", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&cfg.listen, "listen", "127.0.0.1:8080", "numeric-loopback HTTP address (host:port)")
	fs.StringVar(&cfg.socket, "wake-socket", ".cache/worker.sock", "worker Unix datagram socket for best-effort wake hints")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, errors.New("unexpected positional arguments")
	}
	host, port, err := net.SplitHostPort(cfg.listen)
	ip := net.ParseIP(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || n < 0 || n > 65535 {
		return config{}, errors.New("listen must be a numeric loopback host and port (0–65535)")
	}
	if strings.TrimSpace(cfg.socket) == "" {
		return config{}, errors.New("wake socket path is required")
	}
	cfg.dsn = getenv("LOCAL_DATABASE_URL")
	cfg.secret = []byte(getenv("SYNTHETIC_WEBHOOK_SECRET"))
	if strings.TrimSpace(cfg.dsn) == "" {
		return config{}, errors.New("LOCAL_DATABASE_URL is required")
	}
	if len(cfg.secret) < 32 {
		return config{}, errors.New("SYNTHETIC_WEBHOOK_SECRET must contain at least 32 bytes")
	}
	return cfg, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	cfg, err := configuration(args, getenv, out)
	if err != nil {
		return err
	}
	store, err := postgres.Open(ctx, cfg.dsn)
	if err != nil {
		return errors.New("invalid local database configuration")
	}
	defer store.Close()
	handler, err := api.New(store, cfg.secret, func(ctx context.Context) error { return api.Notify(ctx, cfg.socket) })
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return errors.New("cannot listen on configured loopback address")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		<-served
		if err != nil {
			return errors.New("HTTP shutdown deadline exceeded")
		}
		return nil
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

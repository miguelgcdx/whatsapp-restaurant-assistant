// local-staff-api serves the loopback-only staff access boundary, never migrations.
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
	"syscall"
	"time"

	"restaurant-assistant/backend/internal/catalog"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/staffavailability"
	"restaurant-assistant/backend/internal/staffcatalog"
)

const apiHelp = `Usage: local-staff-api [-listen 127.0.0.1:8081]
  -listen ADDRESS numeric-loopback HTTP host:port (port 0–65535)
Required environment: LOCAL_DATABASE_URL, LOCAL_RESTAURANT_ID,
STAFF_JWT_ISSUER, STAFF_JWKS_URL (fixed /api/auth/jwks).
GET /v1/staff/access requires an EdDSA bearer token and current membership.
GET /v1/staff/catalog uses the same authority; optional limit=1..100 and cursor.
Optional STAFF_CATALOG_CURSOR_KEY: 64 lowercase hex characters (32 random bytes).
Without this separate application-purpose key, authenticated catalog reads return 503.
PATCH /v1/staff/catalog/items/{id}/availability: {expected_version:STRING,available:BOOL}.
PATCH /v1/staff/restaurant/availability: {expected_version:STRING,open:BOOL}.
Writes require the same JWT/current membership, application/json (64 KiB maximum),
and one Idempotency-Key (1-128 printable ASCII, no whitespace); no cursor key needed.
Versions are canonical nonnegative int64 decimal strings. No query/extra fields.
409 version_conflict requires refresh; idempotency_conflict means changed key/body.
Same-key/body retries replay the original result after current authorization.
No automatic mutation retry; preserve the exact key/body for an uncertain outcome.
No migrations or runtime fixture provisioning. Errors are redacted.
`

func run(ctx context.Context, args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("local-staff-api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "127.0.0.1:8081", "")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return staff.ErrInput
	}
	host, port, e := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	n, pe := strconv.Atoi(port)
	if e != nil || ip == nil || !ip.IsLoopback() || pe != nil || n < 0 || n > 65535 {
		return staff.ErrInput
	}
	restaurant := getenv("LOCAL_RESTAURANT_ID")
	v, e := staff.NewVerifier(getenv("STAFF_JWT_ISSUER"), getenv("STAFF_JWKS_URL"))
	if e != nil {
		return e
	}
	store, e := staff.OpenStore(ctx, getenv("LOCAL_DATABASE_URL"), restaurant)
	if e != nil {
		return e
	}
	defer store.Close()
	h, e := staff.NewHandler(store, restaurant, v)
	if e != nil {
		return e
	}
	catalogStore, e := catalog.OpenStore(ctx, getenv("LOCAL_DATABASE_URL"), restaurant)
	if e != nil {
		return e
	}
	defer catalogStore.Close()
	catalogHandler, e := staffcatalog.NewHandler(catalogStore, store, restaurant, v, getenv("STAFF_CATALOG_CURSOR_KEY"))
	if e != nil {
		return e
	}
	availabilityHandler, e := staffavailability.NewHandler(catalogStore, store, v)
	if e != nil {
		return e
	}
	combined := dispatch(h, catalogHandler, availabilityHandler)
	listener, e := net.Listen("tcp", *listen)
	if e != nil {
		return staff.ErrUnavailable
	}
	server := &http.Server{Handler: combined, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case e := <-served:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return staff.ErrUnavailable
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e := server.Shutdown(shutdown)
		if e != nil {
			_ = server.Close()
		}
		<-served
		if e != nil {
			return staff.ErrUnavailable
		}
		return nil
	}
}

// dispatch deliberately avoids ServeMux cleaning/redirects. The original
// access handler still owns every legacy access and unknown-path response.
func dispatch(access, catalog, availability http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if staffavailability.Matches(r.URL.Path) {
			availability.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/staff/catalog" {
			catalog.ServeHTTP(w, r)
			return
		}
		access.ServeHTTP(w, r)
	})
}

func execute(ctx context.Context, args []string, getenv func(string) string, out, errout io.Writer) int {
	e := run(ctx, args, getenv)
	if errors.Is(e, flag.ErrHelp) {
		_, _ = io.WriteString(out, apiHelp)
		return 0
	}
	if e != nil {
		_, _ = fmt.Fprintln(errout, "Staff API could not be completed.")
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

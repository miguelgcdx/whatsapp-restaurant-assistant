// Package postgres implements the durable local fake-provider store.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

var ErrUnsafeDSN = errors.New("database must be an explicitly named local foundation database")

//go:embed migrations/*.sql
var migrationFiles embed.FS

func localConfig(dsn string) (*pgxpool.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrUnsafeDSN
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid connection configuration", ErrUnsafeDSN)
	}
	local := func(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }
	if !local(cfg.ConnConfig.Host) || !strings.HasPrefix(cfg.ConnConfig.Database, "restaurant_foundation_") || cfg.ConnConfig.Database == "restaurant_foundation_" {
		return nil, ErrUnsafeDSN
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if !local(fallback.Host) {
			return nil, ErrUnsafeDSN
		}
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.MaxConnIdleTime = time.Second
	// A zero HealthCheckPeriod panics in pgx. With both minima zero, this
	// positive ticker only reaps local idle resources; it does not ping idle DBs.
	cfg.HealthCheckPeriod = time.Second
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	return cfg, nil
}

// LocalPoolConfig shares the same guarded, bounded local pool settings with
// other app-owned stores. It performs no database I/O or migrations.
func LocalPoolConfig(dsn string) (*pgxpool.Config, error) {
	return localConfig(dsn)
}

// Open is lazy and never applies migrations or creates a database.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := localConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open local database pool: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Migrate is an explicit setup operation restricted to named loopback databases.
// API/worker startup must not call it implicitly.
func Migrate(ctx context.Context, dsn string) (err error) {
	cfg, err := localConfig(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*cfg.ConnConfig)
	db.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, db.Close()) }()
	if _, err = db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS app"); err != nil {
		return fmt.Errorf("create business namespace: %w", err)
	}
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithTableName("app.goose_db_version"))
	if err != nil {
		return fmt.Errorf("configure business migrations: %w", err)
	}
	if _, err = provider.Up(ctx); err != nil {
		return fmt.Errorf("apply business migrations: %w", err)
	}
	return nil
}

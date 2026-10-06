// Package store is the Postgres bootstrap every Byz service copies: open a
// pool with the fleet's settings, run a function in a transaction, answer
// readiness, and recognise the Postgres error codes handlers care about.
//
// Frozen verbs: Open, WithTx, Health, IsUniqueViolation.
//
// No ORM and no generated SQL live here. Services keep raw SQL over
// database/sql (or sqlc later, which generates against the same DBTX
// interface). Nothing in this package knows a table name.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/Syluxso/byz-platform/httpx"
)

// DBTX is satisfied by *sql.DB and *sql.Tx, so a store method can run inside
// or outside a transaction without two code paths. sqlc generates against
// the same shape.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Options tunes the pool. Zero values use the fleet defaults byz-kan runs
// with: 8 open, 2 idle, one hour lifetime, 5 second startup ping.
type Options struct {
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	PingTimeout     time.Duration
}

// Open connects with the pgx stdlib driver, applies pool settings and pings
// once so a bad DSN fails at startup rather than on the first request.
func Open(ctx context.Context, dsn string, opts ...Options) (*sql.DB, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.MaxOpen <= 0 {
		o.MaxOpen = 8
	}
	if o.MaxIdle <= 0 {
		o.MaxIdle = 2
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = time.Hour
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = 5 * time.Second
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(o.MaxOpen)
	db.SetMaxIdleConns(o.MaxIdle)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)

	pctx, cancel := context.WithTimeout(ctx, o.PingTimeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return db, nil
}

// WithTx runs fn in a transaction. It commits when fn returns nil, rolls
// back on error or panic, and re-panics after rolling back.
func WithTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// Health returns a readiness probe for httpx.Health that pings the pool.
func Health(db *sql.DB) httpx.ReadyFunc {
	return func(ctx context.Context) error {
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("postgres: %w", err)
		}
		return nil
	}
}

// Closer adapts db.Close to the shutdown.Closer shape.
func Closer(db *sql.DB) func(context.Context) error {
	return func(context.Context) error { return db.Close() }
}

// Sentinel errors a service store returns so handlers can map them without
// importing pgconn. Wrap them with fmt.Errorf("%w: ...") for detail.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

// Postgres SQLSTATE codes.
const (
	CodeUniqueViolation     = "23505"
	CodeForeignKeyViolation = "23503"
	CodeCheckViolation      = "23514"
	CodeNotNullViolation    = "23502"
)

// PgCode returns the SQLSTATE of a pgx error, or "".
func PgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// IsUniqueViolation reports a unique index or constraint violation.
func IsUniqueViolation(err error) bool { return PgCode(err) == CodeUniqueViolation }

// IsForeignKeyViolation reports a foreign key violation.
func IsForeignKeyViolation(err error) bool { return PgCode(err) == CodeForeignKeyViolation }

// IsCheckViolation reports a CHECK or NOT NULL violation.
func IsCheckViolation(err error) bool {
	c := PgCode(err)
	return c == CodeCheckViolation || c == CodeNotNullViolation
}

// Problem maps a store error to problem JSON. Sentinels and Postgres codes
// get precise statuses; anything else is a 500 carrying only fallback, so
// driver messages never reach clients.
func Problem(err error, fallback string) httpx.Problem {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return httpx.NewProblem(http.StatusNotFound, "store.not_found", fallback)
	case errors.Is(err, ErrConflict), IsUniqueViolation(err):
		return httpx.NewProblem(http.StatusConflict, "store.conflict", fallback)
	case errors.Is(err, ErrInvalid), IsCheckViolation(err), IsForeignKeyViolation(err):
		return httpx.NewProblem(http.StatusBadRequest, "store.invalid", fallback)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return httpx.NewProblem(http.StatusGatewayTimeout, "store.timeout", fallback)
	default:
		return httpx.NewProblem(http.StatusInternalServerError, "store.internal", fallback)
	}
}

// WriteError writes Problem(err, fallback) to w.
func WriteError(w http.ResponseWriter, err error, fallback string) {
	httpx.WriteProblemValue(w, Problem(err, fallback))
}

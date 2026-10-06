// Package migrate applies named SQL migrations to Postgres using the
// name-keyed schema_migrations table byz-kan already runs in production, so
// adopting it needs no baseline step.
//
// Frozen verb: Run.
//
// Shape of the table, per schema:
//
//	CREATE TABLE IF NOT EXISTS <schema>.schema_migrations (
//	    name        TEXT PRIMARY KEY,
//	    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
//	)
//
// Rules, unchanged from kan:
//   - Append only. Never edit or reorder a shipped migration.
//   - One concern per entry, named for what it does.
//   - Prefer idempotent SQL so a half-recorded state is recoverable.
//   - Each migration runs and is recorded in one transaction.
//
// SQL files stay in the service. This package only orders and applies them.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Migration is one named SQL step.
type Migration struct {
	Name string
	SQL  string
}

// Options configures Run.
type Options struct {
	// Schema qualifies the schema_migrations table, e.g. "kan". Required.
	Schema string
	// Bootstrap is idempotent CREATE ... IF NOT EXISTS SQL run before the
	// migrations on every start (kan's docs/SCHEMA.sql). Optional.
	Bootstrap string
	// Migrations to apply in order after Bootstrap.
	Migrations []Migration
	// Logger for applied/skipped lines. Default slog.Default().
	Logger *slog.Logger
}

var schemaRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Run creates the schema and its schema_migrations table if missing, runs
// Bootstrap, then applies every migration not yet recorded. A failing
// migration returns an error naming it; the service should treat that as
// fatal rather than serve against a schema it does not match.
func Run(ctx context.Context, db *sql.DB, opts Options) error {
	if !schemaRe.MatchString(opts.Schema) {
		return fmt.Errorf("migrate: invalid schema name %q", opts.Schema)
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	table := opts.Schema + ".schema_migrations"

	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+opts.Schema); err != nil {
		return fmt.Errorf("migrate: create schema: %w", err)
	}
	if strings.TrimSpace(opts.Bootstrap) != "" {
		if _, err := db.ExecContext(ctx, opts.Bootstrap); err != nil {
			return fmt.Errorf("migrate: bootstrap: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
    name        TEXT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("migrate: create %s: %w", table, err)
	}

	applied, err := Applied(ctx, db, opts.Schema)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range applied {
		seen[n] = true
	}

	names := map[string]bool{}
	ran := 0
	for _, m := range opts.Migrations {
		if strings.TrimSpace(m.Name) == "" {
			return errors.New("migrate: migration with empty name")
		}
		if names[m.Name] {
			return fmt.Errorf("migrate: duplicate migration name %q", m.Name)
		}
		names[m.Name] = true
		if seen[m.Name] {
			continue
		}
		if err := applyOne(ctx, db, table, m); err != nil {
			return fmt.Errorf("migrate: migration %q: %w", m.Name, err)
		}
		log.Info("migration applied", "schema", opts.Schema, "name", m.Name)
		ran++
	}
	if ran == 0 {
		log.Info("migrations: nothing to apply", "schema", opts.Schema, "known", len(opts.Migrations))
	}
	return nil
}

// Applied lists recorded migration names in application order.
func Applied(ctx context.Context, db *sql.DB, schema string) ([]string, error) {
	if !schemaRe.MatchString(schema) {
		return nil, fmt.Errorf("migrate: invalid schema name %q", schema)
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM `+schema+`.schema_migrations ORDER BY applied_at, name`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func applyOne(ctx context.Context, db *sql.DB, table string, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+table+` (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, m.Name); err != nil {
		return err
	}
	return tx.Commit()
}

// FromFS loads *.sql files from dir in fsys, sorted by filename, using the
// filename without extension as the migration name. Prefix files with a
// sortable date or sequence: 2026-09-27-add-column.sql.
//
//	//go:embed db/migrations/*.sql
//	var migrationsFS embed.FS
//	ms, err := migrate.FromFS(migrationsFS, "db/migrations")
func FromFS(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir %q: %w", dir, err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Name: strings.TrimSuffix(e.Name(), ".sql"), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

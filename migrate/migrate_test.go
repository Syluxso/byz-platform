package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Syluxso/byz-platform/store"
)

func TestFromFSOrdersAndNames(t *testing.T) {
	fsys := fstest.MapFS{
		"db/migrations/2026-09-02-second.sql": {Data: []byte("SELECT 2")},
		"db/migrations/2026-09-01-first.sql":  {Data: []byte("SELECT 1")},
		"db/migrations/README.md":             {Data: []byte("ignored")},
	}
	ms, err := FromFS(fsys, "db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Name != "2026-09-01-first" || ms[1].Name != "2026-09-02-second" || ms[1].SQL != "SELECT 2" {
		t.Fatalf("migrations = %+v", ms)
	}
}

func TestRunRejectsBadSchema(t *testing.T) {
	err := Run(context.Background(), nil, Options{Schema: "kan; DROP"})
	if err == nil || !strings.Contains(err.Error(), "invalid schema") {
		t.Fatalf("err = %v", err)
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BYZ_TEST_DB_URL")
	if dsn == "" {
		t.Skip("BYZ_TEST_DB_URL unset; skipping Postgres test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRunIntegration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	schema := fmt.Sprintf("byzplat_%d", time.Now().UnixNano()%1_000_000_000)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	boot := `CREATE TABLE IF NOT EXISTS ` + schema + `.things (id INT PRIMARY KEY)`
	m1 := Migration{Name: "2026-09-27-add-name", SQL: `ALTER TABLE ` + schema + `.things ADD COLUMN IF NOT EXISTS name TEXT`}
	bad := Migration{Name: "2026-09-28-broken", SQL: `ALTER TABLE ` + schema + `.missing ADD COLUMN x INT`}

	if err := Run(ctx, db, Options{Schema: schema, Bootstrap: boot, Migrations: []Migration{m1}}); err != nil {
		t.Fatal(err)
	}
	applied, err := Applied(ctx, db, schema)
	if err != nil || len(applied) != 1 || applied[0] != m1.Name {
		t.Fatalf("applied = %v (%v)", applied, err)
	}
	// Re-run is a no-op and idempotent.
	if err := Run(ctx, db, Options{Schema: schema, Bootstrap: boot, Migrations: []Migration{m1}}); err != nil {
		t.Fatal(err)
	}
	// A failing migration is reported by name and not recorded.
	err = Run(ctx, db, Options{Schema: schema, Migrations: []Migration{m1, bad}})
	if err == nil || !strings.Contains(err.Error(), bad.Name) {
		t.Fatalf("expected failure naming %s, got %v", bad.Name, err)
	}
	applied, _ = Applied(ctx, db, schema)
	if len(applied) != 1 {
		t.Fatalf("failed migration was recorded: %v", applied)
	}
	// Table shape matches kan's live table byte-for-byte in the columns that matter.
	var cols int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='schema_migrations' AND column_name IN ('name','applied_at')`, schema).Scan(&cols); err != nil || cols != 2 {
		t.Fatalf("schema_migrations columns = %d (%v)", cols, err)
	}
}

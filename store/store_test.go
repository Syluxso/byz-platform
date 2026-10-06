package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestDSNEnv names the Postgres used by integration tests across the module.
// Unset or unreachable means those tests skip, matching byz-kan's harness.
const TestDSNEnv = "BYZ_TEST_DB_URL"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(TestDSNEnv)
	if dsn == "" {
		t.Skipf("%s unset; skipping Postgres test", TestDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProblemMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ErrNotFound, 404, "store.not_found"},
		{fmt.Errorf("board: %w", ErrNotFound), 404, "store.not_found"},
		{sql.ErrNoRows, 404, "store.not_found"},
		{ErrConflict, 409, "store.conflict"},
		{&pgconn.PgError{Code: CodeUniqueViolation}, 409, "store.conflict"},
		{&pgconn.PgError{Code: CodeCheckViolation}, 400, "store.invalid"},
		{&pgconn.PgError{Code: CodeForeignKeyViolation}, 400, "store.invalid"},
		{ErrInvalid, 400, "store.invalid"},
		{context.DeadlineExceeded, 504, "store.timeout"},
		{errors.New("pq: something secret"), 500, "store.internal"},
	}
	for _, c := range cases {
		p := Problem(c.err, "fallback")
		if p.Status != c.status || p.Code != c.code || p.Detail != "fallback" {
			t.Errorf("%v -> %+v, want %d %s", c.err, p, c.status, c.code)
		}
	}
	if !IsUniqueViolation(fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "23505"})) {
		t.Fatal("wrapped unique violation not detected")
	}
	if IsUniqueViolation(errors.New("x")) {
		t.Fatal("plain error flagged as unique violation")
	}
}

func TestNullHelpers(t *testing.T) {
	if PtrString(sql.NullString{}) != nil || PtrString(sql.NullString{String: "", Valid: true}) != nil {
		t.Fatal("empty should be nil")
	}
	if s := PtrString(sql.NullString{String: "a", Valid: true}); s == nil || *s != "a" {
		t.Fatal("PtrString")
	}
	now := time.Now()
	if PtrTime(sql.NullTime{}) != nil || !PtrTime(sql.NullTime{Time: now, Valid: true}).Equal(now) {
		t.Fatal("PtrTime")
	}
	if PtrInt(sql.NullInt64{}) != nil || *PtrInt(sql.NullInt64{Int64: 3, Valid: true}) != 3 {
		t.Fatal("PtrInt")
	}
	empty := ""
	if NullString(nil).Valid || NullString(&empty).Valid {
		t.Fatal("NullString nil/empty should be invalid")
	}
	v := 7
	if n := NullInt(&v); !n.Valid || n.Int64 != 7 {
		t.Fatal("NullInt")
	}
}

func TestOpenBadDSNFailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Open(ctx, "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable", Options{PingTimeout: time.Second})
	if err == nil {
		t.Fatal("expected ping failure")
	}
}

func TestWithTxIntegration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	table := fmt.Sprintf("byzplat_tx_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+table+` (id INT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table) })

	// Commit path.
	if err := WithTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO `+table+` VALUES (1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Rollback path: error from fn.
	boom := errors.New("boom")
	if err := WithTx(ctx, db, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(ctx, `INSERT INTO `+table+` VALUES (2)`)
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	// Unique violation surfaces through the sentinel helpers.
	err := WithTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO `+table+` VALUES (1)`)
		return err
	})
	if !IsUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d (%v); rollback did not hold", n, err)
	}
	if err := Health(db)(ctx); err != nil {
		t.Fatal(err)
	}
}

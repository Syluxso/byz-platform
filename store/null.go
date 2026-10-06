package store

import (
	"database/sql"
	"time"
)

// Null-to-pointer helpers. Every service store repeats these when scanning
// nullable columns into view structs with pointer fields.

// PtrString returns nil for NULL or empty string.
func PtrString(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	s := ns.String
	return &s
}

// PtrTime returns nil for NULL.
func PtrTime(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// PtrInt returns nil for NULL.
func PtrInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// PtrInt64 returns nil for NULL.
func PtrInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// PtrBool returns nil for NULL.
func PtrBool(b sql.NullBool) *bool {
	if !b.Valid {
		return nil
	}
	v := b.Bool
	return &v
}

// Pointer-to-null helpers for binding optional request fields.

// NullString binds nil or "" as NULL.
func NullString(p *string) sql.NullString {
	if p == nil || *p == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

// NullTime binds nil as NULL.
func NullTime(p *time.Time) sql.NullTime {
	if p == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}

// NullInt64 binds nil as NULL.
func NullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// NullInt binds nil as NULL.
func NullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

// NullBool binds nil as NULL.
func NullBool(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}

package httpx

import (
	"net/http"
	"strconv"
)

// Page is the list envelope for Byz Go services. It is deliberately generic:
// items, a total count, and the 1-based page and size that produced it.
//
//	{"items":[...],"total":123,"page":1,"size":50}
//
// Total is the count across all pages, or -1 when the store did not compute
// it. Spring services on the platform still return their own Page shape; a
// front-end that talks to both must handle both until those services change.
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
}

// NewPage builds a Page, normalising a nil slice to an empty array so the
// JSON is always "items": [] rather than null.
func NewPage[T any](items []T, total int64, page, size int) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Page: page, Size: size}
}

// PageParams reads ?page= and ?size= from the request. page is 1-based and
// defaults to 1. size defaults to defSize and is clamped to [1, maxSize].
func PageParams(r *http.Request, defSize, maxSize int) (page, size int) {
	if defSize <= 0 {
		defSize = 50
	}
	if maxSize <= 0 {
		maxSize = 500
	}
	page = 1
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	size = defSize
	if v, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && v > 0 {
		size = v
	}
	if size > maxSize {
		size = maxSize
	}
	return page, size
}

// Offset converts 1-based page and size into a SQL OFFSET.
func Offset(page, size int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * size
}

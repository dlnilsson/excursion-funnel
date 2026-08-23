package report

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// queryRows runs a query and scans every row into a T. label names the result
// set for error messages ("summary", "recent requests"), producing the same
// "query/scan/iterate <label>" wrapping every reporting query used to spell out
// by hand.
//
// It is a package-level function rather than a Reporter method because Go does
// not allow generic methods.
func queryRows[T any](ctx context.Context, db *sql.DB, label, query string, args []any, scan func(*sql.Rows, *T) error) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var row T
		if err := scan(rows, &row); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return out, nil
}

// queryRowsInto is queryRows for callers that discard some scanned columns
// instead of storing them, such as a filter applied during the scan. Returning
// false from scan drops the row.
func queryRowsInto[T any](ctx context.Context, db *sql.DB, label, query string, args []any, scan func(*sql.Rows, *T) (bool, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var row T
		keep, err := scan(rows, &row)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		if keep {
			out = append(out, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return out, nil
}

// placeholders returns "?, ?, …" for an IN clause of n values.
func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.Repeat("?, ", n-1) + "?"
}

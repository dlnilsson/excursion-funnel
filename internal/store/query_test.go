package store

import (
	"path/filepath"
	"testing"
)

func TestQueryContextLocalKeepsBoundParameters(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// []byte is supported by the database driver, but deliberately unsupported
	// by sqlparams.Bind. This proves local queries still use driver binding.
	rows, err := st.QueryContext(t.Context(), "SELECT ?::BLOB", []byte("bound value"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("missing row: %v", rows.Err())
	}
	var value []byte
	if err := rows.Scan(&value); err != nil || string(value) != "bound value" {
		t.Fatalf("bound value = %q, %v", value, err)
	}
}

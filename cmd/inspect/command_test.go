package inspect

import (
	"bytes"
	"path/filepath"
	"testing"
)

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	cmd := New()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestRejectsNonPositiveLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	if _, err := execute(t, "--db", dbPath, "--limit", "0", "req-1"); err == nil {
		t.Fatal("non-positive limit accepted")
	}
}

func TestRequiresExactlyOneID(t *testing.T) {
	if _, err := execute(t); err == nil {
		t.Fatal("missing ID accepted")
	}
	if _, err := execute(t, "one", "two"); err == nil {
		t.Fatal("multiple IDs accepted")
	}
}

package tools

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func seedToolCall(t *testing.T, dbPath string) {
	t.Helper()
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "req-tool-today", StartedAt: now, CompletedAt: now.Add(time.Second), Method: "POST", Path: "/v1/messages", UpstreamURL: "https://api.anthropic.com/v1/messages",
		ToolCalls: []queue.ToolCall{{Name: "Bash", Description: "Run tests", Command: "go test ./..."}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func runToday(t *testing.T, dbPath string, jsonOutput bool) (string, error) {
	t.Helper()
	var output bytes.Buffer
	err := Run(t.Context(), &output, Options{
		Connection: reporting.Connection{DBPath: dbPath, DefaultDBPath: dbPath},
		Limit:      report.DefaultRecentToolCallLimit,
		JSON:       jsonOutput,
	})
	return output.String(), err
}

func TestToday(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	seedToolCall(t, dbPath)
	output, err := runToday(t, dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STARTED", "Bash", "Run tests", "go test ./..."} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestTodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	seedToolCall(t, dbPath)
	output, err := runToday(t, dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	var rows []report.ToolCallRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	if len(rows) != 1 || rows[0].Name != "Bash" || rows[0].Description != "Run tests" || rows[0].Command != "go test ./..." {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRequiresPositiveLimit(t *testing.T) {
	if err := Run(t.Context(), &bytes.Buffer{}, Options{Limit: 0}); err == nil {
		t.Fatal("non-positive limit accepted")
	}
}

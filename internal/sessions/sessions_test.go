package sessions

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

func TestRunTodayJSONDistinguishesStartedFromUsed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	today := reporting.BeginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "yesterday", SessionID: "continued", StartedAt: today.AddDate(0, 0, -1), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "continued", SessionID: "continued", StartedAt: today, Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "new", SessionID: "new", StartedAt: today.Add(time.Minute), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{Connection: testConnection(t, dbPath), Shortcut: "today", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var rows []report.SessionRow
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Provider != "openai" || rows[0].Started != 1 || rows[0].Used != 2 {
		t.Fatalf("today rows = %+v, want openai started=1 used=2", rows)
	}
}

func TestRunRangeRendersWeeklyTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 3, 12, 0, 0, 0, time.Local)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "weekly", SessionID: "weekly", StartedAt: started,
		Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{
		Connection: testConnection(t, dbPath), Since: "2026-08-03", Until: "2026-08-09", GroupBy: "week",
	}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"PERIOD", "PROVIDER", "STARTED", "USED", "2026-08-03", "anthropic"} {
		if !strings.Contains(output.String(), marker) {
			t.Fatalf("output missing %q:\n%s", marker, output.String())
		}
	}
}

func TestRunRejectsInvalidDateAndShortcut(t *testing.T) {
	if err := Run(t.Context(), &bytes.Buffer{}, Options{Since: "not-a-date"}); err == nil {
		t.Fatal("invalid date succeeded")
	}
	if err := Run(t.Context(), &bytes.Buffer{}, Options{Shortcut: "month"}); err == nil {
		t.Fatal("invalid shortcut succeeded")
	}
}

func testConnection(t *testing.T, dbPath string) reporting.Connection {
	t.Helper()
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	return reporting.Connection{DBPath: dbPath, DefaultDBPath: dbPath}
}

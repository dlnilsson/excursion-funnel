package requests

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestTodayWithExplicitRangeIsRejected(t *testing.T) {
	for _, opts := range []Options{{Today: true, Limit: 50, Since: "2026-08-01"}, {Today: true, Limit: 50, Until: "2026-08-01"}} {
		if err := Run(t.Context(), io.Discard, opts); !errors.Is(err, reporting.ErrShortcutWithRange) {
			t.Fatalf("Run() error = %v, want ErrShortcutWithRange", err)
		}
	}
}

func TestHistoricalDateRangeIsInclusive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events := []queue.UsageEvent{
		webEvent("before", time.Date(2026, 7, 31, 23, 59, 0, 0, time.Local), "before"),
		webEvent("first", time.Date(2026, 8, 1, 12, 0, 0, 0, time.Local), "first"),
		webEvent("last", time.Date(2026, 8, 7, 23, 59, 0, 0, time.Local), "last"),
		webEvent("after", time.Date(2026, 8, 8, 0, 0, 0, 0, time.Local), "after"),
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{
		Connection: reporting.Connection{DBPath: dbPath, DefaultDBPath: dbPath},
		Limit:      50, Since: "2026-08-01", Until: "2026-08-07", JSON: true,
	}); err != nil {
		t.Fatal(err)
	}
	var rows []report.WebRequestRow
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Query != "last" || rows[1].Query != "first" {
		t.Fatalf("rows = %+v, want inclusive range newest first", rows)
	}
}

func TestInvalidDateIsRejected(t *testing.T) {
	if err := Run(t.Context(), io.Discard, Options{Limit: 50, Since: "08/01/2026"}); err == nil || !strings.Contains(err.Error(), "--since: expected YYYY-MM-DD") {
		t.Fatalf("Run() error = %v", err)
	}
}

func webEvent(id string, started time.Time, query string) queue.UsageEvent {
	return queue.UsageEvent{
		RequestID: id, StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "https://api.openai.com/v1/responses",
		WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: query}},
	}
}

func seedWebRequest(t *testing.T, dbPath string) {
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
		RequestID: "req-web-today", StartedAt: now, CompletedAt: now.Add(time.Second), Method: "POST", Path: "/v1/responses", UpstreamURL: "https://api.openai.com/v1/responses",
		ClientName: "Codex", ModelReported: "gpt-5.6-sol", WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: "Go release", Domain: "go.dev"}},
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
		Today:      true,
	})
	return output.String(), err
}

func TestToday(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	seedWebRequest(t, dbPath)
	output, err := runToday(t, dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STARTED", "Codex", "gpt-5.6-sol", "web_search_call", "Go release", "go.dev"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %s", want, output)
		}
	}
}

func TestTodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	seedWebRequest(t, dbPath)
	output, err := runToday(t, dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	var rows []report.WebRequestRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	if len(rows) != 1 || rows[0].Name != "web_search_call" || rows[0].Query != "Go release" || rows[0].Domain != "go.dev" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRequiresPositiveLimit(t *testing.T) {
	if err := Run(t.Context(), &bytes.Buffer{}, Options{Limit: 0}); err == nil {
		t.Fatal("non-positive limit accepted")
	}
}

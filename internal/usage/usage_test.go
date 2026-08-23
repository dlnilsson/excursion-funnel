package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func connection(t *testing.T, dbPath string) reporting.Connection {
	t.Helper()
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	return reporting.Connection{DBPath: dbPath, DefaultDBPath: dbPath}
}

func TestTodayWithExplicitRangeIsRejected(t *testing.T) {
	for _, opts := range []Options{{Today: true, Since: "2026-07-01"}, {Today: true, Until: "2026-07-01"}} {
		if err := Run(t.Context(), io.Discard, opts); !errors.Is(err, reporting.ErrShortcutWithRange) {
			t.Fatalf("Run() error = %v, want ErrShortcutWithRange", err)
		}
	}
}

func TestMissingDatabaseIsAnError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "absent.duckdb")
	if err := Run(t.Context(), io.Discard, Options{Today: true, GroupBy: "model", Connection: connection(t, dbPath)}); err == nil {
		t.Fatal("missing database returned nil")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run created %s", dbPath)
	}
}

func TestTodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	input, outputTokens, total := int64(10), int64(4), int64(14)
	started := reporting.BeginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "req-today-json", StartedAt: started, CompletedAt: started.Add(time.Second),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "https://api.openai.com/v1/responses",
		ModelReported: "gpt-5.3-codex", HTTPStatus: 200,
		Usage: queue.Usage{InputTokens: &input, OutputTokens: &outputTokens, TotalTokens: &total},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{Today: true, JSON: true, GroupBy: "model", Connection: connection(t, dbPath)}); err != nil {
		t.Fatal(err)
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output.String())
	}
	if len(rows) != 1 || rows[0].Provider != "openai" || rows[0].Model != "gpt-5.3-codex" || rows[0].Total != total {
		t.Fatalf("rows = %+v", rows)
	}
	if want := reporting.BeginningOfDay(time.Now()).Format("2006-01-02"); rows[0].Day != want {
		t.Fatalf("Day = %q, want %q", rows[0].Day, want)
	}
}

func TestNoRangeIsAllTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	input, total := int64(10), int64(10)
	today := reporting.BeginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "today", StartedAt: today, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ModelReported: "gpt-test", Usage: queue.Usage{InputTokens: &input, TotalTokens: &total}},
		{RequestID: "yesterday", StartedAt: today.AddDate(0, 0, -1), Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ModelReported: "gpt-test", Usage: queue.Usage{InputTokens: &input, TotalTokens: &total}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{JSON: true, GroupBy: "model", Connection: connection(t, dbPath)}); err != nil {
		t.Fatal(err)
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output.String())
	}
	if len(rows) != 1 || rows[0].Requests != 2 || rows[0].Total != 20 {
		t.Fatalf("rows = %+v, want both today's and yesterday's usage", rows)
	}

	output.Reset()
	if err := Run(t.Context(), &output, Options{Today: true, JSON: true, GroupBy: "model", Connection: connection(t, dbPath)}); err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output.String())
	}
	if len(rows) != 1 || rows[0].Requests != 1 || rows[0].Total != 10 {
		t.Fatalf("rows = %+v, want only today's usage", rows)
	}
}

func TestProjectContextGroupingAndFilters(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(14)
	started := reporting.BeginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "matching", Directory: "/work/api", GitBranch: "feature-x", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
		{RequestID: "other", Directory: "/work/web", GitBranch: "main", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Run(t.Context(), &output, Options{
		Today: true, GroupBy: "git_branch", Directory: "/work/api", Branch: "feature-x", Connection: connection(t, dbPath),
	}); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "GIT_BRANCH") || !strings.Contains(text, "feature-x") || strings.Contains(text, "main") {
		t.Fatalf("project context output = %s", text)
	}
}

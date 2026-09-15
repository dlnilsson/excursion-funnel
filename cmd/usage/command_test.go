package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
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

func TestUsageFiltersToCurrentSourceUnlessAll(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(14)
	started := reporting.BeginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "mine", Source: "daniel", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
		{RequestID: "theirs", Source: "teammate", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EF_SOURCE", "daniel")

	output, err := execute(t, "--db", dbPath, "--group-by", "source", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	if len(rows) != 1 || rows[0].Source != "daniel" || rows[0].Requests != 1 {
		t.Fatalf("default rows = %+v", rows)
	}

	output, err = execute(t, "--db", dbPath, "--group-by", "source", "--json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	if len(rows) != 2 {
		t.Fatalf("all-source rows = %+v", rows)
	}
}

func TestHeatmapRejectsExplicitGroupBy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	if _, err := execute(t, "--db", dbPath, "--heatmap", "--group-by", "day"); !errors.Is(err, ErrHeatmapWithGroupBy) {
		t.Fatalf("error = %v, want ErrHeatmapWithGroupBy", err)
	}
	// The default grouping was never asked for, so it must not block the flag.
	if _, err := execute(t, "--db", dbPath, "--heatmap", "--json"); err == nil || errors.Is(err, ErrHeatmapWithGroupBy) {
		t.Fatalf("error = %v, want a database error rather than the conflict", err)
	}
}

func TestHeatmapJSONEmitsActivityRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(4_096)
	started := reporting.BeginningOfDay(time.Now()).Add(13 * time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "mine", Source: "daniel", StartedAt: started, Method: "POST", Path: "/v1/messages",
			UpstreamURL: "https://api.anthropic.com/v1/messages", Usage: queue.Usage{TotalTokens: &total}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EF_SOURCE", "daniel")

	output, err := execute(t, "--db", dbPath, "--heatmap", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []report.TokenActivityRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	// The default window is a zero-filled calendar year of days.
	if len(rows) < 360 {
		t.Fatalf("returned %d rows, want a zero-filled calendar", len(rows))
	}
	var sum int64
	for _, row := range rows {
		sum += row.Total
	}
	if sum != total {
		t.Fatalf("activity total = %d, want %d", sum, total)
	}
}

func TestHeatmapTodayUsesHourlyBuckets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := execute(t, "--db", dbPath, "today", "--heatmap", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []report.TokenActivityRow
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output)
	}
	if len(rows) != 24 {
		t.Fatalf("returned %d rows, want 24 hourly buckets", len(rows))
	}
}

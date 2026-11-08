package ui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestToolsFailedFilterFindsOlderFailures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// More than the dashboard's limit of newer, successful calls must not
	// hide an older failure when the server applies the filter.
	calls := make([]queue.ToolCall, 0, recentToolCallsLimit+1)
	results := make([]queue.ToolResult, 0, recentToolCallsLimit+1)
	for i := range recentToolCallsLimit {
		id := time.Duration(i).String()
		calls = append(calls, queue.ToolCall{ID: id, Name: "shell", Command: "true"})
		results = append(results, queue.ToolResult{ID: id, ExitCode: new(int64(0))})
	}
	calls = append(calls, queue.ToolCall{ID: "failed", Name: "shell", Command: "false"})
	results = append(results, queue.ToolResult{ID: "failed", ExitCode: new(int64(1)), Failed: true})
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "tools", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses", UpstreamURL: "/",
		ToolCalls: calls, ToolResults: results,
	}}); err != nil {
		t.Fatal(err)
	}
	handler := New(report.New(st), slog.New(slog.DiscardHandler))
	for _, failed := range []bool{false, true} {
		url := "/ui/api/tools"
		if failed {
			url += "?failed=true"
		}
		rec := doRequest(t, handler, http.MethodGet, url)
		var rows []report.ToolCallRow
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &rows) != nil {
			t.Fatalf("tools response: %d, %s", rec.Code, rec.Body.String())
		}
		if failed {
			if len(rows) != 1 || rows[0].ID != "failed" || rows[0].Failed == nil || !*rows[0].Failed {
				t.Fatalf("failed rows = %+v", rows)
			}
		} else if len(rows) != recentToolCallsLimit {
			t.Fatalf("unfiltered rows = %d, want %d", len(rows), recentToolCallsLimit)
		}
	}
}

package ui

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestRowsHandler(t *testing.T) {
	t.Run("writes an empty JSON array for nil rows", func(t *testing.T) {
		h := rowsHandler(slog.New(slog.DiscardHandler), "test", func(*http.Request) ([]int, error) {
			return nil, nil
		})

		rec := doRequest(t, h, http.MethodGet, "/")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); got != "[]\n" {
			t.Fatalf("body = %q, want empty JSON array", got)
		}
	})

	t.Run("returns an internal error when the query fails", func(t *testing.T) {
		h := rowsHandler(slog.New(slog.DiscardHandler), "test", func(*http.Request) ([]int, error) {
			return nil, errors.New("query failed")
		})

		rec := doRequest(t, h, http.MethodGet, "/")

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := rec.Body.String(); got != "internal error\n" {
			t.Fatalf("body = %q, want generic error", got)
		}
	})
}

func TestNew_RedirectsUIPath(t *testing.T) {
	h := New(nil, slog.New(slog.DiscardHandler))
	rec := doRequest(t, h, http.MethodGet, "/ui")

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMovedPermanently)
	}
	if got := rec.Header().Get("Location"); got != "/ui/" {
		t.Fatalf("Location = %q, want %q", got, "/ui/")
	}
}

func TestHandleIndex_ServesDashboardHTML(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "excursion-funnel") {
		t.Fatalf("body missing expected marker: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "history-table") {
		t.Fatalf("body contains history table, want chart canvases")
	}
	if strings.Contains(rec.Body.String(), "cdn.jsdelivr.net") {
		t.Fatalf("body contains CDN dependency: %s", rec.Body.String())
	}
	for _, marker := range []string{
		`/ui/assets/vendor/chart.umd.min.js`,
		`/ui/assets/vendor/highlight.min.js`,
		`/ui/assets/vendor/github-dark.min.css`,
		`/ui/assets/vendor/github.min.css`,
		`id="token-heatmap"`,
		`id="token-heatmap-legend"`,
		`const heatmapWeeks = 53`,
		`const heatmapWeekStart = 1`,
		`renderTokenHeatmap(historyRows)`,
		`id="hourly-token-chart"`,
		`id="token-activity-last-24-toggle"`,
		`id="token-activity-all-time-toggle"`,
		`aria-label="Token activity range"`,
		`let selectedTokenActivityRange = "24h"`,
		`setTokenActivityRange("all")`,
		`aggregateDailyTokenRows(latestHistoryRows)`,
		`aggregate.FreshInput += Number(row.FreshInput) || 0`,
		`aggregate.Output += Number(row.Output) || 0`,
		`fetch("/ui/api/history/hourly")`,
		`type: "line"`,
		`label: "Fresh input tokens"`,
		`label: "Output tokens"`,
		`Fresh input excludes cached input tokens and Anthropic cache-write tokens.`,
		`id="history-stacked-chart"`,
		`id="history-chart-source-filter"`,
		`id="model-activity-table"`,
		`id="model-activity-source-filter"`,
		`<th>Input tokens</th>`,
		`<th>Output tokens</th>`,
		`aggregateModelActivityRows(rows)`,
		`aggregate.Input += Number(row.Input) || 0`,
		`aggregate.Output += Number(row.Output) || 0`,
		`fetch("/ui/api/kpis")`,
		`fetch("/ui/api/sessions")`,
		`Sessions used today`,
		`Sessions started this week`,
		`p50 latency`,
		`Error rate today`,
		`Cache-hit rate`,
		`Peak concurrency`,
		`Anthropic thinking`,
		`Anthropic cache writes`,
		`anthropic?.Requests`,
		`data-table-key="summary"`,
		`id="summary-source-filter"`,
		`initSourceFilters();`,
		`data-table-key="directories"`,
		`data-table-key="model-activity"`,
		`data-table-key="errors"`,
		`data-table-key="tools"`,
		`excursion-funnel-table-state`,
		`data-section-key="kpis" open`,
		`data-section-key="token-activity" open`,
		`data-section-key="model-activity-chart" open`,
		`data-section-key="token-heatmap" open`,
		`excursion-funnel-section-state`,
		`initDashboardSectionCollapse();`,
		`hourlyTokenChart.resize();`,
		`historyStackedChart.resize();`,
		`fetch("/ui/api/history/models")`,
		`fetch("/ui/api/directories")`,
		`id="tools-table"`,
		`fetch("/ui/api/tools")`,
		`data-table-key="web-requests"`,
		`id="web-requests-table"`,
		`fetch("/ui/api/web-requests")`,
		`hljs.highlightElement`,
		`data-command-index`,
		`navigator.clipboard`,
		`id="agent-config"`,
		`id="claude-config-tab"`,
		`id="codex-config-tab"`,
		`ANTHROPIC_BASE_URL`,
		`base_url = "http://127.0.0.1:8787/v1"`,
	} {
		if !strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("body missing chart marker %q: %s", marker, rec.Body.String())
		}
	}
	for _, removed := range []string{
		"Requests by git branch",
		`data-table-key="branches"`,
		`fetch("/ui/api/branches")`,
		`Session activity overall`,
		`id="session-history-chart"`,
		`id="session-day-toggle"`,
		`id="session-week-toggle"`,
		`renderSessionHistory`,
		`sessionChartOptions`,
	} {
		if strings.Contains(rec.Body.String(), removed) {
			t.Fatalf("body contains removed dashboard marker %q", removed)
		}
	}
	body := rec.Body.String()
	modelChartStart := strings.Index(body, `data-section-key="model-activity-chart"`)
	modelChartEnd := modelChartStart + strings.Index(body[modelChartStart:], "</details>")
	if modelTable := strings.Index(body, `data-table-key="model-activity"`); modelTable < modelChartEnd {
		t.Fatal("model activity table appears inside collapsible chart, want it independently visible")
	}
	if strings.Index(body, `id="hourly-token-chart"`) > strings.Index(body, `id="history-stacked-chart"`) {
		t.Fatal("hourly token chart appears after model history, want it above")
	}
	if strings.Index(body, `id="token-heatmap"`) < strings.Index(body, `id="agent-config"`) {
		t.Fatal("token heatmap appears before agent configuration, want it below")
	}
}

func TestHandleKPIs_ReturnsTodaysOperationalMetrics(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/kpis")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var stats report.KPIStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if stats.Requests != 2 || stats.Errors != 1 {
		t.Fatalf("request/error counts = %d/%d, want 2/1: %+v", stats.Requests, stats.Errors, stats)
	}
	if stats.LatencyP50MS == nil || *stats.LatencyP50MS != 1000 || stats.LatencyP95MS == nil || *stats.LatencyP95MS != 1000 {
		t.Fatalf("latencies = %v/%v, want 1000/1000", stats.LatencyP50MS, stats.LatencyP95MS)
	}
	if stats.CacheEligibleRequests != 1 || stats.CacheHitRequests != 0 || stats.PeakConcurrency != 1 {
		t.Fatalf("cache/concurrency stats = %+v, want eligible=1 hits=0 peak=1", stats)
	}
	if stats.Anthropic.Requests != 1 || stats.Anthropic.ThinkingReportedRequests != 0 || stats.Anthropic.CacheWriteReportedRequests != 0 {
		t.Fatalf("Anthropic coverage = %+v, want one request with no detailed usage", stats.Anthropic)
	}
}

func TestHandleSessions_ReturnsCurrentCountsAndHistory(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var stats sessionDashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if stats.Today.Started != 2 || stats.Today.Used != 2 || stats.ThisWeek.Started != 2 || stats.ThisWeek.Used != 2 {
		t.Fatalf("current session counts = %+v/%+v, want 2/2 for both periods", stats.Today, stats.ThisWeek)
	}
	if len(stats.Daily) != 2 || len(stats.Weekly) != 2 {
		t.Fatalf("session history lengths = %d/%d, want provider rows for both providers", len(stats.Daily), len(stats.Weekly))
	}
}

func TestHandleKPIs_ReturnsAnthropicUsageDetails(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var (
		today      = reporting.BeginningOfDay(time.Now())
		output     = int64(100)
		cacheWrite = int64(1000)
	)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "anthropic-details", StartedAt: today.Add(time.Hour), CompletedAt: today.Add(time.Hour + time.Second),
		Method: "POST", Path: "/v1/messages", UpstreamURL: "/", HTTPStatus: 200,
		Usage:     queue.Usage{OutputTokens: &output, CacheWriteTokens: &cacheWrite},
		UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":40},"cache_creation":{"ephemeral_5m_input_tokens":300,"ephemeral_1h_input_tokens":600}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	h := New(report.New(st), slog.New(slog.DiscardHandler))

	rec := doRequest(t, h, http.MethodGet, "/ui/api/kpis")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var stats report.KPIStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	got := stats.Anthropic
	if got.Requests != 1 || got.OutputReportedRequests != 1 || got.ThinkingReportedRequests != 1 ||
		got.ThinkingTokens != 40 || got.OutputTokens != 100 {
		t.Fatalf("Anthropic thinking response = %+v, want one reported request with thinking=40 output=100", got)
	}
	if got.CacheWriteTokens != 1000 || got.CacheWrite5MTokens != 300 ||
		got.CacheWrite1HTokens != 600 || got.CacheWriteUnclassifiedTokens != 100 {
		t.Fatalf("Anthropic cache response = %+v, want total=1000 5m=300 1h=600 unclassified=100", got)
	}
}

func TestHandleKPIs_EmptyLedgerReturnsNullLatenciesAndZeroCounts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := New(report.New(st), slog.New(slog.DiscardHandler))

	rec := doRequest(t, h, http.MethodGet, "/ui/api/kpis")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var stats report.KPIStats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if stats.LatencyP50MS != nil || stats.LatencyP95MS != nil || stats.Requests != 0 || stats.PeakConcurrency != 0 {
		t.Fatalf("empty KPI response = %+v, want nil latencies and zero counts", stats)
	}
	if !strings.Contains(rec.Body.String(), `"LatencyP50MS":null`) || !strings.Contains(rec.Body.String(), `"LatencyP95MS":null`) {
		t.Fatalf("empty KPI JSON does not preserve null latencies: %s", rec.Body.String())
	}
}

func TestHandleAssets_ServesEmbeddedDependencies(t *testing.T) {
	h := newTestHandler(t)

	tests := []struct {
		path        string
		contentType string
		marker      string
	}{
		{"/ui/assets/vendor/chart.umd.min.js", "javascript", "Chart"},
		{"/ui/assets/vendor/highlight.min.js", "javascript", "hljs"},
		{"/ui/assets/vendor/github-dark.min.css", "text/css", ".hljs"},
		{"/ui/assets/vendor/github.min.css", "text/css", ".hljs"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodGet, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Fatalf("content-type = %q, want it to contain %q", ct, tt.contentType)
			}
			if !strings.Contains(rec.Body.String(), tt.marker) {
				t.Fatalf("body missing marker %q", tt.marker)
			}
		})
	}
}

func TestHandleDirectories_ReturnsTodaysGroups(t *testing.T) {
	h := newTestHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/ui/api/directories")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"/work/api": true, "/work/web": true}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for _, row := range rows {
		if !want[row.Directory] {
			t.Fatalf("unexpected row = %+v", row)
		}
	}
}

func TestBranchesEndpointNoLongerServesAPI(t *testing.T) {
	h := newTestHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/ui/api/branches")
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, branch endpoint still serves JSON", ct)
	}
}

func TestSourcesEndpointNoLongerServesAPI(t *testing.T) {
	h := newTestHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/ui/api/sources")
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, sources endpoint still serves JSON", ct)
	}
}

func TestHandleToolCalls_ReturnsTodaysCalls(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/tools")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.ToolCallRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.RequestID != "req-ok-1" || got.Client != "Zed" || got.Model != "gpt-5.3-codex" || got.Name != "Bash" || got.Command != "go test ./..." || got.Description != "Run tests" {
		t.Fatalf("tool row = %+v, want the persisted Bash call", got)
	}
}

func TestHandleWebRequests_ReturnsTodaysRequests(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	today := reporting.BeginningOfDay(time.Now())
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "req-web-today", StartedAt: today.Add(time.Hour), CompletedAt: today.Add(time.Hour + time.Second),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "https://api.openai.com/v1/responses",
		ModelReported: "gpt-5.5-codex", ClientName: "Codex", WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: "Go release"}},
	}}); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}
	rec := doRequest(t, New(report.New(st), slog.New(slog.DiscardHandler)), http.MethodGet, "/ui/api/web-requests")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.WebRequestRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0].Name != "web_search_call" || rows[0].Query != "Go release" {
		t.Fatalf("rows = %+v, want today's web search", rows)
	}
}

func TestHandleToolCalls_ExcludesEarlierCalls(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	today := reporting.BeginningOfDay(time.Now())
	events := []queue.UsageEvent{
		{
			RequestID:   "req-yesterday",
			StartedAt:   today.Add(-time.Minute),
			CompletedAt: today.Add(-time.Minute + time.Second),
			Method:      "POST",
			Path:        "/v1/responses",
			UpstreamURL: "https://api.openai.com/v1/responses",
			ToolCalls: []queue.ToolCall{{
				ID:      "tool-old",
				Name:    "Bash",
				Command: "old command",
			}},
		},
		{
			RequestID:   "req-today",
			StartedAt:   today.Add(time.Hour),
			CompletedAt: today.Add(time.Hour + time.Second),
			Method:      "POST",
			Path:        "/v1/responses",
			UpstreamURL: "https://api.openai.com/v1/responses",
			ToolCalls: []queue.ToolCall{{
				ID:      "tool-today",
				Name:    "Bash",
				Command: "today command",
			}},
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}

	rec := doRequest(t, New(report.New(st), slog.New(slog.DiscardHandler)), http.MethodGet, "/ui/api/tools")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.ToolCallRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0].RequestID != "req-today" {
		t.Fatalf("rows = %+v, want only today's tool call", rows)
	}
}

func TestHandleSummary_ReturnsTodaysUsage(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/summary")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2: %+v", len(rows), rows)
	}
	var found bool
	for _, row := range rows {
		if row.Provider == "openai" && row.Model == "gpt-5.3-codex" {
			found = true
			if row.Client != "Zed" || row.Source != "alice" || row.Requests != 1 ||
				row.Input != 10 || row.Output != 4 || row.Total != 14 {
				t.Fatalf("openai row = %+v, want client=Zed source=alice requests=1 input=10 output=4 total=14", row)
			}
		}
	}
	if !found {
		t.Fatalf("no openai/gpt-5.3-codex row in %+v", rows)
	}
	// Rows must carry a source so the dashboard can filter them client-side.
	for _, row := range rows {
		if row.Source == "" {
			t.Fatalf("row without source = %+v", row)
		}
	}
}

func TestHandleErrors_ReturnsOnlyErrorRows(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/errors")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.InspectRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].ID != "req-error-1" || rows[0].Client != "Claude Code" || rows[0].ErrorType != "parse_error" {
		t.Fatalf("row = %+v, want req-error-1/Claude Code/parse_error", rows[0])
	}
}

func TestHandleHistory_ReturnsLiveAggregates(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/history")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Day == "" || row.Source == "" {
			t.Fatalf("history row = %+v, want a day and a source", row)
		}
	}
	var found bool
	for _, row := range rows {
		if row.Provider == "openai" && row.Model == "gpt-5.3-codex" {
			found = true
			if row.Client != "Zed" || row.Requests != 1 || row.Input != 10 || row.FreshInput != 10 || row.Output != 4 {
				t.Fatalf("openai row = %+v, want client=Zed requests=1 input=10 fresh-input=10 output=4", row)
			}
		}
	}
	if !found {
		t.Fatalf("no openai/gpt-5.3-codex row in %+v", rows)
	}
}

func TestHandleModelHistory_ReturnsLiveAggregates(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/history/models")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Day != "" || row.Source == "" {
			t.Fatalf("model history row = %+v, want empty day and a source", row)
		}
	}
	var found bool
	for _, row := range rows {
		if row.Provider == "openai" && row.Model == "gpt-5.3-codex" {
			found = true
			if row.Client != "Zed" || row.Requests != 1 || row.Input != 10 || row.Output != 4 || row.Total != 14 {
				t.Fatalf("openai row = %+v, want client=Zed requests=1 input=10 output=4 total=14", row)
			}
		}
	}
	if !found {
		t.Fatalf("no openai/gpt-5.3-codex row in %+v", rows)
	}
}

func TestHandleHourlyHistory_ReturnsTwentyFourTokenBuckets(t *testing.T) {
	h := newTestHandler(t)

	rec := doRequest(t, h, http.MethodGet, "/ui/api/history/hourly")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.HourlyTokenRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if len(rows) != hourlyHistoryBuckets {
		t.Fatalf("rows len = %d, want %d: %+v", len(rows), hourlyHistoryBuckets, rows)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i-1].Hour.Before(rows[i].Hour) {
			t.Fatalf("rows are not chronologically ordered at %d: %+v", i, rows)
		}
	}
	latest := rows[len(rows)-1]
	if latest.FreshInput != 10 || latest.Output != 4 {
		t.Fatalf("latest row = %+v, want fresh input 10 and output 4", latest)
	}
}

func TestSummaryAndErrors_RejectNonGET(t *testing.T) {
	h := newTestHandler(t)

	for _, path := range []string{"/ui/api/kpis", "/ui/api/summary", "/ui/api/history", "/ui/api/history/models", "/ui/api/history/hourly", "/ui/api/errors", "/ui/api/tools"} {
		rec := doRequest(t, h, http.MethodPost, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, rec.Code)
		}
	}
}

func doRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var (
		now    = time.Now().UTC()
		input  = int64(10)
		output = int64(4)
		total  = int64(14)
	)
	events := []queue.UsageEvent{
		{
			RequestID:     "req-ok-1",
			SessionID:     "openai-session",
			Source:        "alice",
			Directory:     "/work/api",
			GitBranch:     "main",
			StartedAt:     now,
			CompletedAt:   now.Add(time.Second),
			Method:        "POST",
			Path:          "/v1/responses",
			UpstreamURL:   "https://api.openai.com/v1/responses",
			ModelReported: "gpt-5.3-codex",
			HTTPStatus:    200,
			UserAgent:     "codex-tui/0.146.0",
			Originator:    "zed",
			ClientName:    "Zed",
			Usage:         queue.Usage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total},
			ToolCalls: []queue.ToolCall{{
				ID:            "toolu-bash",
				Name:          "Bash",
				Command:       "go test ./...",
				Description:   "Run tests",
				ArgumentsJSON: `{"command":"go test ./...","description":"Run tests"}`,
			}},
		},
		{
			RequestID:    "req-error-1",
			SessionID:    "anthropic-session",
			Source:       "bob",
			Directory:    "/work/web",
			GitBranch:    "feature-x",
			StartedAt:    now.Add(time.Minute),
			CompletedAt:  now.Add(time.Minute + time.Second),
			Method:       "POST",
			Path:         "/v1/messages",
			UpstreamURL:  "https://api.anthropic.com/v1/messages",
			HTTPStatus:   500,
			UserAgent:    "claude-cli/2.1.218",
			ClientName:   "Claude Code",
			ErrorType:    "parse_error",
			ErrorMessage: "boom",
		},
		{
			RequestID:   "req-health-check",
			StartedAt:   now.Add(2 * time.Minute),
			CompletedAt: now.Add(2*time.Minute + time.Second),
			Method:      "GET",
			Path:        "/health",
			UpstreamURL: "http://localhost:8787/health",
			UserAgent:   "curl/8.21.0",
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}
	rep := report.New(st)
	return New(rep, slog.New(slog.DiscardHandler))
}

package ui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

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
		`id="history-stacked-chart"`,
		`id="model-activity-table"`,
		`data-table-key="summary"`,
		`data-table-key="sources"`,
		`data-table-key="directories"`,
		`data-table-key="model-activity"`,
		`data-table-key="errors"`,
		`data-table-key="tools"`,
		`excursion-funnel-table-state`,
		`fetch("/ui/api/history/models")`,
		`fetch("/ui/api/sources")`,
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
	} {
		if strings.Contains(rec.Body.String(), removed) {
			t.Fatalf("body contains removed branch UI marker %q", removed)
		}
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

func TestHandleSources_ReturnsTodaysSources(t *testing.T) {
	h := newTestHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/ui/api/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []report.SummaryRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Source != "alice" || rows[1].Source != "bob" {
		t.Fatalf("source rows = %+v", rows)
	}
	if rows[0].Requests != 1 || rows[1].Errors != 1 {
		t.Fatalf("source totals = %+v", rows)
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
	today := beginningOfDay(time.Now())
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

	today := beginningOfDay(time.Now())
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
			if row.Client != "Zed" || row.Requests != 1 || row.Input != 10 {
				t.Fatalf("openai row = %+v, want client=Zed requests=1 input=10", row)
			}
		}
	}
	if !found {
		t.Fatalf("no openai/gpt-5.3-codex row in %+v", rows)
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
		if row.Day == "" {
			t.Fatalf("history row missing day: %+v", row)
		}
	}
	var found bool
	for _, row := range rows {
		if row.Provider == "openai" && row.Model == "gpt-5.3-codex" {
			found = true
			if row.Client != "Zed" || row.Requests != 1 || row.Input != 10 {
				t.Fatalf("openai row = %+v, want client=Zed requests=1 input=10", row)
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
		if row.Day != "" {
			t.Fatalf("model history row has day: %+v", row)
		}
	}
	var found bool
	for _, row := range rows {
		if row.Provider == "openai" && row.Model == "gpt-5.3-codex" {
			found = true
			if row.Client != "Zed" || row.Requests != 1 || row.Input != 10 {
				t.Fatalf("openai row = %+v, want client=Zed requests=1 input=10", row)
			}
		}
	}
	if !found {
		t.Fatalf("no openai/gpt-5.3-codex row in %+v", rows)
	}
}

func TestSummaryAndErrors_RejectNonGET(t *testing.T) {
	h := newTestHandler(t)

	for _, path := range []string{"/ui/api/summary", "/ui/api/history", "/ui/api/history/models", "/ui/api/errors", "/ui/api/tools"} {
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

	now := time.Now().UTC()
	input := int64(10)
	events := []queue.UsageEvent{
		{
			RequestID:     "req-ok-1",
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
			Usage:         queue.Usage{InputTokens: &input},
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

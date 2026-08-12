package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestSummary_GroupsByProviderAndModel(t *testing.T) {
	dbPath := seedReportDB(t)

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.Summary(t.Context(), SummaryOptions{
		Since:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Until:   time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		GroupBy: "model",
	})
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2: %+v", len(rows), rows)
	}

	openai := rows[1]
	if openai.Provider != "openai" || openai.Client != "Codex CLI" || openai.Model != "gpt-5.3-codex" {
		t.Fatalf("openai row identity = %+v, want openai/Codex CLI/gpt-5.3-codex", openai)
	}
	if openai.Requests != 2 || openai.Input != 30 || openai.Cached != 5 || openai.Output != 15 || openai.Total != 45 {
		t.Fatalf("openai totals = %+v, want requests=2 input=30 cached=5 output=15 total=45", openai)
	}

	anthropic := rows[0]
	if anthropic.Provider != "anthropic" || anthropic.Client != "Claude Code" || anthropic.Model != "claude-opus-5" {
		t.Fatalf("anthropic row identity = %+v, want anthropic/Claude Code/claude-opus-5", anthropic)
	}
	if anthropic.Requests != 1 || anthropic.Input != 7 || anthropic.CacheWrite != 3 || anthropic.Output != 11 {
		t.Fatalf("anthropic totals = %+v, want requests=1 input=7 cache_write=3 output=11", anthropic)
	}
}

func TestHistorical_ReadsMaterializedAggregates(t *testing.T) {
	dbPath := seedReportDB(t)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.RefreshUsageAggregates(t.Context()); err != nil {
		t.Fatalf("RefreshUsageAggregates() error = %v", err)
	}

	r := New(st)

	byModel, err := r.HistoricalByModel(t.Context())
	if err != nil {
		t.Fatalf("HistoricalByModel() error = %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("byModel len = %d, want 2: %+v", len(byModel), byModel)
	}
	// Ordered by total_tokens DESC: openai (45) before anthropic (0).
	openai := byModel[0]
	if openai.Provider != "openai" || openai.Client != "Codex CLI" || openai.Model != "gpt-5.3-codex" {
		t.Fatalf("openai identity = %+v, want openai/Codex CLI/gpt-5.3-codex", openai)
	}
	if openai.Day != "" {
		t.Fatalf("openai day = %q, want empty", openai.Day)
	}
	if openai.Requests != 2 || openai.Input != 30 || openai.Cached != 5 || openai.Output != 15 || openai.Total != 45 {
		t.Fatalf("openai totals = %+v, want requests=2 input=30 cached=5 output=15 total=45", openai)
	}

	byDay, err := r.HistoricalByDay(t.Context())
	if err != nil {
		t.Fatalf("HistoricalByDay() error = %v", err)
	}
	if len(byDay) != 2 {
		t.Fatalf("byDay len = %d, want 2: %+v", len(byDay), byDay)
	}
	if byDay[0].Day != "2026-08-01" || byDay[0].Model != "gpt-5.3-codex" || byDay[0].Requests != 2 {
		t.Fatalf("byDay[0] = %+v, want day=2026-08-01 model=gpt-5.3-codex requests=2", byDay[0])
	}
}

func TestInspect_FindsByResponseID(t *testing.T) {
	dbPath := seedReportDB(t)

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.Inspect(t.Context(), "resp-openai-1", 0)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ID != "req-openai-1" || got.Provider != "openai" || got.Client != "Codex CLI" || got.ModelReported != "gpt-5.3-codex" {
		t.Fatalf("inspect row = %+v, want req-openai-1/openai/Codex CLI/gpt-5.3-codex", got)
	}
	if !got.Total.Valid || got.Total.Int64 != 15 {
		t.Fatalf("total = %+v, want valid 15", got.Total)
	}
}

func TestInspect_IncludesToolCalls(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	err = st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID:   "req-tool",
		ResponseID:  "resp-tool",
		StartedAt:   started,
		CompletedAt: started.Add(time.Second),
		Method:      "POST",
		Path:        "/v1/messages",
		UpstreamURL: "https://api.anthropic.com/v1/messages",
		ToolCalls: []queue.ToolCall{{
			ID:            "toolu_bash",
			Name:          "Bash",
			Command:       "go test ./...",
			Description:   "Run tests",
			ArgumentsJSON: `{"command":"go test ./...","description":"Run tests"}`,
		}},
	}})
	if err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	rows, err := r.Inspect(t.Context(), "resp-tool", 1)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(rows) != 1 || len(rows[0].ToolCalls) != 1 {
		t.Fatalf("Inspect() = %+v, want one request with one tool call", rows)
	}
	call := rows[0].ToolCalls[0]
	if call.Ordinal != 0 || call.ID != "toolu_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want persisted Bash call", call)
	}
}

func TestInspect_IncludesWebRequests(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "req-web", ResponseID: "resp-web", StartedAt: started,
		CompletedAt: started.Add(time.Second), Method: "POST", Path: "/v1/responses",
		UpstreamURL: "https://api.openai.com/v1/responses",
		WebRequests: []queue.WebRequest{{ID: "web-1", Name: "web_search_call", Query: "Go release", Domain: "go.dev"}},
	}}); err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	rows, err := r.Inspect(t.Context(), "resp-web", 1)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(rows) != 1 || len(rows[0].WebRequests) != 1 {
		t.Fatalf("Inspect() = %+v, want one web request", rows)
	}
	request := rows[0].WebRequests[0]
	if request.ID != "web-1" || request.Name != "web_search_call" || request.Query != "Go release" || request.Domain != "go.dev" {
		t.Fatalf("web request = %+v, want persisted web search", request)
	}
}

// WebRequests must surface web-tool activity from both providers — OpenAI's
// web_search_call and Claude Code's client-side WebSearch/WebFetch — while
// leaving stray raw-HTTP rows out of the ledger.
func TestWebRequests_IncludesBothProviders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{
			RequestID: "req-openai", ResponseID: "resp-openai", StartedAt: started,
			CompletedAt: started.Add(time.Second), Method: "POST", Path: "/v1/responses",
			UpstreamURL: "https://api.openai.com/v1/responses",
			WebRequests: []queue.WebRequest{{ID: "ws-1", Name: "web_search_call", Query: "Go release"}},
		},
		{
			RequestID: "req-claude", ResponseID: "resp-claude", StartedAt: started.Add(time.Minute), Method: "POST", Path: "/v1/messages",
			CompletedAt: started.Add(time.Minute + time.Second),
			UpstreamURL: "https://api.anthropic.com/v1/messages",
			WebRequests: []queue.WebRequest{
				{ID: "toolu_ws", Name: "WebSearch", Query: "nord theme"},
				{ID: "toolu_wf", Name: "WebFetch", URL: "https://example.com/docs"},
			},
		},
		{
			RequestID: "req-raw", ResponseID: "resp-raw", StartedAt: started.Add(2 * time.Minute), Method: "GET", Path: "/",
			CompletedAt: started.Add(2*time.Minute + time.Second),
			UpstreamURL: "http://example.com/",
			WebRequests: []queue.WebRequest{{ID: "", Name: "GET", URL: "http://example.com/"}},
		},
	}); err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	rows, err := r.WebRequests(t.Context(), ToolCallOptions{})
	if err != nil {
		t.Fatalf("WebRequests() error = %v", err)
	}

	byName := make(map[string]WebRequestRow, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
		if row.Name == "GET" {
			t.Fatalf("WebRequests() returned a raw-HTTP row: %+v", row)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("WebRequests() returned %d rows, want 3 (web_search_call, WebSearch, WebFetch): %+v", len(rows), rows)
	}
	if got := byName["web_search_call"]; got.Provider != "openai" || got.Query != "Go release" {
		t.Fatalf("web_search_call row = %+v, want openai/Go release", got)
	}
	if got := byName["WebSearch"]; got.Provider != "anthropic" || got.Query != "nord theme" {
		t.Fatalf("WebSearch row = %+v, want anthropic/nord theme", got)
	}
	if got := byName["WebFetch"]; got.Provider != "anthropic" || got.URL != "https://example.com/docs" {
		t.Fatalf("WebFetch row = %+v, want anthropic/example.com/docs", got)
	}
}

func TestNew_ReusesExistingStore(t *testing.T) {
	dbPath := seedReportDB(t)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	r := New(st)
	rows, err := r.Inspect(t.Context(), "resp-openai-1", 0)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
}

// A reporting command must not conjure the ledger it claims to read: a
// mistyped --db has to surface as an error, not as "no usage rows".
func TestOpen_MissingDatabaseErrorsWithoutCreatingIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "usage.sqlite")

	r, err := Open(dbPath)
	if err == nil {
		_ = r.Close()
		t.Fatal("Open() error = nil, want a missing-database error")
	}
	if !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("Open() error = %v, want it to name the path %s", err, dbPath)
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Open() created %s; stat err = %v, want not-exist", dbPath, statErr)
	}
	if _, statErr := os.Stat(filepath.Dir(dbPath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Open() created the parent directory of %s", dbPath)
	}
}

func TestRecentErrors_FiltersToErrorRows(t *testing.T) {
	dbPath := seedReportDBWithError(t)

	r, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.RecentErrors(t.Context(), 20)
	if err != nil {
		t.Fatalf("RecentErrors() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.ID != "req-error-1" || got.ErrorType != "parse_error" {
		t.Fatalf("error row = %+v, want req-error-1/parse_error", got)
	}
}

func seedReportDBWithError(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer st.Close()

	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	events := []queue.UsageEvent{
		{
			RequestID:   "req-ok-1",
			StartedAt:   started,
			CompletedAt: started.Add(time.Second),
			Method:      "POST",
			Path:        "/v1/responses",
			UpstreamURL: "https://api.openai.com/v1/responses",
			HTTPStatus:  200,
		},
		{
			RequestID:    "req-error-1",
			StartedAt:    started.Add(time.Minute),
			CompletedAt:  started.Add(time.Minute + time.Second),
			Method:       "POST",
			Path:         "/v1/messages",
			UpstreamURL:  "https://api.anthropic.com/v1/messages",
			HTTPStatus:   500,
			ErrorType:    "parse_error",
			ErrorMessage: "boom",
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}
	return dbPath
}

func seedReportDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer st.Close()

	var (
		started    = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		input10    = int64(10)
		input20    = int64(20)
		input7     = int64(7)
		cached5    = int64(5)
		cacheWrite = int64(3)
		output5    = int64(5)
		output10   = int64(10)
		output11   = int64(11)
		total15    = int64(15)
		total30    = int64(30)
	)
	events := []queue.UsageEvent{
		{
			RequestID:     "req-openai-1",
			ResponseID:    "resp-openai-1",
			StartedAt:     started,
			CompletedAt:   started.Add(time.Second),
			Method:        "POST",
			Path:          "/v1/responses",
			UpstreamURL:   "https://api.openai.com/v1/responses",
			ModelReported: "gpt-5.3-codex",
			HTTPStatus:    200,
			UserAgent:     "codex-tui/0.146.0",
			Usage: queue.Usage{
				InputTokens:       &input10,
				CachedInputTokens: &cached5,
				OutputTokens:      &output5,
				TotalTokens:       &total15,
			},
		},
		{
			RequestID:     "req-openai-2",
			ResponseID:    "resp-openai-2",
			StartedAt:     started.Add(time.Minute),
			CompletedAt:   started.Add(time.Minute + time.Second),
			Method:        "POST",
			Path:          "/v1/responses",
			UpstreamURL:   "https://api.openai.com/v1/responses",
			ModelReported: "gpt-5.3-codex",
			HTTPStatus:    200,
			UserAgent:     "codex-tui/0.146.0",
			Usage: queue.Usage{
				InputTokens:  &input20,
				OutputTokens: &output10,
				TotalTokens:  &total30,
			},
		},
		{
			RequestID:     "req-anthropic-1",
			ResponseID:    "msg-anthropic-1",
			StartedAt:     started.Add(2 * time.Minute),
			CompletedAt:   started.Add(2*time.Minute + time.Second),
			Method:        "POST",
			Path:          "/v1/messages",
			UpstreamURL:   "https://api.anthropic.com/v1/messages",
			ModelReported: "claude-opus-5",
			HTTPStatus:    200,
			UserAgent:     "claude-cli/2.1.218",
			Usage: queue.Usage{
				InputTokens:      &input7,
				CacheWriteTokens: &cacheWrite,
				OutputTokens:     &output11,
			},
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}
	return dbPath
}

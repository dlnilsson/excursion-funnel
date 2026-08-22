package report

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// TestMain clears hub/quack env vars so a developer's shell (e.g. one already
// configured to point `ef` at a live team hub) can't redirect reporter opens in
// this package's tests away from the temp databases they set up.
func TestMain(m *testing.M) {
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR"} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

func TestSummary_GroupsByProviderAndModel(t *testing.T) {
	dbPath := seedReportDB(t)

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
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

func TestKPIs_ComputesOperationalMetrics(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 1)
	input := int64(100)
	cached := int64(25)
	zero := int64(0)
	events := []queue.UsageEvent{
		{
			RequestID: "cross-midnight", StartedAt: since.Add(-time.Minute), CompletedAt: since.Add(2 * time.Minute),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 200,
		},
		{
			RequestID: "success-cache", StartedAt: since.Add(time.Minute), CompletedAt: since.Add(time.Minute + 100*time.Millisecond),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 200,
			Usage: queue.Usage{InputTokens: &input, CachedInputTokens: &cached},
		},
		{
			RequestID: "success-no-cache", StartedAt: since.Add(2 * time.Minute), CompletedAt: since.Add(2*time.Minute + 300*time.Millisecond),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/", HTTPStatus: 200,
			Usage: queue.Usage{InputTokens: &input, CachedInputTokens: &zero},
		},
		{
			RequestID: "http-error", StartedAt: since.Add(3 * time.Minute), CompletedAt: since.Add(3*time.Minute + 10*time.Millisecond),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 429,
		},
		{
			RequestID: "stream-error", StartedAt: since.Add(4 * time.Minute), CompletedAt: since.Add(4*time.Minute + 500*time.Millisecond),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/", HTTPStatus: 200, ErrorType: "overloaded_error",
			Usage: queue.Usage{InputTokens: &input, CachedInputTokens: &cached},
		},
		{
			RequestID: "health", StartedAt: since, CompletedAt: since.Add(10 * time.Minute),
			Method: "GET", Path: "/health", UpstreamURL: "/", HTTPStatus: 500,
		},
		{
			RequestID: "tomorrow", StartedAt: until, CompletedAt: until.Add(time.Second),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 500,
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}

	stats, err := New(st).KPIs(t.Context(), KPIOptions{
		Since: since, Until: until, KnownProvidersOnly: true,
	})
	if err != nil {
		t.Fatalf("KPIs() error = %v", err)
	}
	if stats.Requests != 4 || stats.Errors != 2 {
		t.Fatalf("request/error counts = %d/%d, want 4/2: %+v", stats.Requests, stats.Errors, stats)
	}
	if stats.CacheEligibleRequests != 3 || stats.CacheHitRequests != 2 {
		t.Fatalf("cache counts = %d/%d, want eligible=3 hits=2: %+v", stats.CacheEligibleRequests, stats.CacheHitRequests, stats)
	}
	if stats.LatencyP50MS == nil || math.Abs(*stats.LatencyP50MS-200) > 0.001 {
		t.Fatalf("LatencyP50MS = %v, want 200", stats.LatencyP50MS)
	}
	if stats.LatencyP95MS == nil || math.Abs(*stats.LatencyP95MS-290) > 0.001 {
		t.Fatalf("LatencyP95MS = %v, want 290", stats.LatencyP95MS)
	}
	if stats.PeakConcurrency != 2 {
		t.Fatalf("PeakConcurrency = %d, want 2", stats.PeakConcurrency)
	}
}

func TestKPIs_EmptyLedgerUsesNullLatenciesAndZeroCounts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	stats, err := New(st).KPIs(t.Context(), KPIOptions{KnownProvidersOnly: true})
	if err != nil {
		t.Fatalf("KPIs() error = %v", err)
	}
	if stats.LatencyP50MS != nil || stats.LatencyP95MS != nil || stats.Requests != 0 || stats.Errors != 0 ||
		stats.CacheEligibleRequests != 0 || stats.CacheHitRequests != 0 || stats.PeakConcurrency != 0 {
		t.Fatalf("empty KPI stats = %+v, want zero counts and nil latencies", stats)
	}
}

func TestSessions_CountsFirstSeenAndDistinctActivityByProvider(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	monday := time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local)
	events := []queue.UsageEvent{
		{RequestID: "openai-a-1", SessionID: "shared-id", StartedAt: monday, Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "openai-a-2", SessionID: "shared-id", StartedAt: monday.AddDate(0, 0, 1), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "anthropic-a", SessionID: "shared-id", StartedAt: monday.Add(time.Hour), Method: "POST", Path: "/v1/messages", UpstreamURL: "/"},
		{RequestID: "openai-b", SessionID: "openai-b", StartedAt: monday.AddDate(0, 0, 6), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "openai-a-next-week", SessionID: "shared-id", StartedAt: monday.AddDate(0, 0, 7), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "missing", StartedAt: monday, Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	reporter := New(st)

	daily, err := reporter.Sessions(t.Context(), SessionOptions{GroupBy: "day"})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionRow(t, daily, "2026-08-03", "openai", 1, 1)
	assertSessionRow(t, daily, "2026-08-03", "anthropic", 1, 1)
	assertSessionRow(t, daily, "2026-08-04", "openai", 0, 1)
	assertSessionRow(t, daily, "2026-08-09", "openai", 1, 1)
	assertSessionRow(t, daily, "2026-08-10", "openai", 0, 1)

	weekly, err := reporter.Sessions(t.Context(), SessionOptions{GroupBy: "week"})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionRow(t, weekly, "2026-08-03", "openai", 2, 2)
	assertSessionRow(t, weekly, "2026-08-03", "anthropic", 1, 1)
	assertSessionRow(t, weekly, "2026-08-10", "openai", 0, 1)

	currentWeek, err := reporter.Sessions(t.Context(), SessionOptions{
		Since: monday.AddDate(0, 0, 7), Until: monday.AddDate(0, 0, 14), GroupBy: "week",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(currentWeek) != 1 {
		t.Fatalf("current-week rows = %+v, want one", currentWeek)
	}
	assertSessionRow(t, currentWeek, "2026-08-10", "openai", 0, 1)
	if _, err := reporter.Sessions(t.Context(), SessionOptions{GroupBy: "month"}); err == nil {
		t.Fatal("unsupported session grouping succeeded")
	}
}

func TestSessions_FallsBackToLegacyCodexRequestSchema(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "legacy-first", CodexSessionID: "legacy-session", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "legacy-second", CodexSessionID: "legacy-session", StartedAt: started.AddDate(0, 0, 1), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	downgradeSessionSchema(t, st)

	rows, err := New(st).Sessions(t.Context(), SessionOptions{GroupBy: "day"})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionRow(t, rows, "2026-08-03", "openai", 1, 1)
	assertSessionRow(t, rows, "2026-08-04", "openai", 0, 1)
}

func TestSessions_LegacySchemaThroughQuack(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Date(2026, 8, 3, 9, 0, 0, 0, time.Local)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "legacy-remote", CodexSessionID: "legacy-session", StartedAt: started,
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ErrorType: "legacy_error",
	}}); err != nil {
		t.Fatal(err)
	}
	downgradeSessionSchema(t, st)
	if _, err := st.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}
	reporter, err := OpenRemote(address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reporter.Close() })
	rows, err := reporter.Sessions(t.Context(), SessionOptions{GroupBy: "week"})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionRow(t, rows, "2026-08-03", "openai", 1, 1)
	inspected, err := reporter.Inspect(t.Context(), "legacy-remote", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected) != 1 || inspected[0].SessionID != "legacy-session" {
		t.Fatalf("legacy remote inspect = %+v, want session legacy-session", inspected)
	}
	errors, err := reporter.RecentErrorsWithin(t.Context(), time.Time{}, time.Time{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].SessionID != "legacy-session" {
		t.Fatalf("legacy remote errors = %+v, want session legacy-session", errors)
	}
}

func downgradeSessionSchema(t *testing.T, st *store.Store) {
	t.Helper()
	for _, query := range []string{
		"DROP VIEW usage_by_day_model",
		"DROP INDEX idx_requests_started_at",
		"DROP INDEX idx_requests_model_requested",
		"DROP INDEX idx_requests_response_id",
		"DROP INDEX idx_requests_source",
		"DROP INDEX idx_requests_directory",
		"DROP INDEX idx_requests_git_branch",
		"DROP INDEX idx_requests_session_id",
		"ALTER TABLE requests DROP COLUMN session_id",
		"DROP INDEX idx_sessions_first_seen_at",
		"DROP TABLE sessions",
	} {
		if _, err := st.DB().Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
}

func assertSessionRow(t *testing.T, rows []SessionRow, period, provider string, started, used int64) {
	t.Helper()
	for _, row := range rows {
		if row.Period == period && row.Provider == provider {
			if row.Started != started || row.Used != used {
				t.Fatalf("session row %s/%s = %+v, want started=%d used=%d", period, provider, row, started, used)
			}
			return
		}
	}
	t.Fatalf("missing session row %s/%s in %+v", period, provider, rows)
}

func TestKPIs_PeakConcurrencyTreatsIntervalsAsHalfOpen(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	events := []queue.UsageEvent{
		{RequestID: "first", StartedAt: since.Add(-time.Minute), CompletedAt: since.Add(time.Minute), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "second", StartedAt: since.Add(time.Minute), CompletedAt: since.Add(2 * time.Minute), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "zero", StartedAt: since.Add(2 * time.Minute), CompletedAt: since.Add(2 * time.Minute), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}

	stats, err := New(st).KPIs(t.Context(), KPIOptions{
		Since: since, Until: since.AddDate(0, 0, 1), KnownProvidersOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PeakConcurrency != 1 {
		t.Fatalf("PeakConcurrency = %d, want 1 for touching half-open intervals", stats.PeakConcurrency)
	}
}

func TestKPIs_ExtractsAnthropicThinkingAndCacheTTLDetails(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var (
		since       = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		until       = since.AddDate(0, 0, 1)
		output100   = int64(100)
		output50    = int64(50)
		output25    = int64(25)
		output20    = int64(20)
		cache1000   = int64(1000)
		cache200    = int64(200)
		cache100    = int64(100)
		cache30     = int64(30)
		openAICache = int64(999)
	)
	events := []queue.UsageEvent{
		{
			RequestID: "anthropic-full", StartedAt: since.Add(time.Minute), CompletedAt: since.Add(time.Minute + time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output100, CacheWriteTokens: &cache1000},
			UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":40},"cache_creation":{"ephemeral_5m_input_tokens":300,"ephemeral_1h_input_tokens":600}}`),
		},
		{
			RequestID: "anthropic-no-details", StartedAt: since.Add(2 * time.Minute), CompletedAt: since.Add(2*time.Minute + time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output50, CacheWriteTokens: &cache200},
			UsageJSON: json.RawMessage(`{"output_tokens_details":{},"cache_creation":{}}`),
		},
		{
			RequestID: "anthropic-raw-only", StartedAt: since.Add(3 * time.Minute), CompletedAt: since.Add(3*time.Minute + time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output25},
			UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":0},"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":75}}`),
		},
		{
			RequestID: "anthropic-details-exceed-total", StartedAt: since.Add(4 * time.Minute), CompletedAt: since.Add(4*time.Minute + time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{CacheWriteTokens: &cache100},
			UsageJSON: json.RawMessage(`{"cache_creation":{"ephemeral_5m_input_tokens":80,"ephemeral_1h_input_tokens":70}}`),
		},
		{
			RequestID: "anthropic-malformed", StartedAt: since.Add(5 * time.Minute), CompletedAt: since.Add(5*time.Minute + time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output20, CacheWriteTokens: &cache30},
			UsageJSON: json.RawMessage(`not-json`),
		},
		{
			RequestID: "openai-lookalike", StartedAt: since.Add(6 * time.Minute), CompletedAt: since.Add(6*time.Minute + time.Second),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output100, CacheWriteTokens: &openAICache},
			UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":999},"cache_creation":{"ephemeral_5m_input_tokens":999}}`),
		},
		{
			RequestID: "anthropic-tomorrow", StartedAt: until, CompletedAt: until.Add(time.Second),
			Method: "POST", Path: "/v1/messages", UpstreamURL: "/",
			Usage:     queue.Usage{OutputTokens: &output100, CacheWriteTokens: &cache1000},
			UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":999},"cache_creation":{"ephemeral_1h_input_tokens":999}}`),
		},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}

	stats, err := New(st).KPIs(t.Context(), KPIOptions{
		Since: since, Until: until, KnownProvidersOnly: true,
	})
	if err != nil {
		t.Fatalf("KPIs() error = %v", err)
	}
	got := stats.Anthropic
	if got.Requests != 5 || got.OutputReportedRequests != 4 || got.ThinkingReportedRequests != 2 {
		t.Fatalf("Anthropic request coverage = %+v, want requests=5 output=4 thinking=2", got)
	}
	if got.ThinkingTokens != 40 || got.OutputTokens != 195 {
		t.Fatalf("Anthropic thinking/output = %d/%d, want 40/195", got.ThinkingTokens, got.OutputTokens)
	}
	if got.CacheWriteReportedRequests != 5 || got.CacheWriteTokens != 1505 ||
		got.CacheWrite5MTokens != 430 || got.CacheWrite1HTokens != 745 || got.CacheWriteUnclassifiedTokens != 330 {
		t.Fatalf("Anthropic cache details = %+v, want reported=5 total=1505 5m=430 1h=745 unclassified=330", got)
	}
}

func TestKPIs_QuackRemote(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	ledger, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	if _, err := ledger.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}

	var (
		started    = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		input      = int64(100)
		cached     = int64(50)
		output     = int64(25)
		cacheWrite = int64(100)
	)
	if err := ledger.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "remote-kpi", StartedAt: started, CompletedAt: started.Add(time.Second),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 200,
		Usage: queue.Usage{InputTokens: &input, CachedInputTokens: &cached},
	}, {
		RequestID: "remote-anthropic", StartedAt: started.Add(time.Minute), CompletedAt: started.Add(time.Minute + time.Second),
		Method: "POST", Path: "/v1/messages", UpstreamURL: "/", HTTPStatus: 200,
		Usage:     queue.Usage{OutputTokens: &output, CacheWriteTokens: &cacheWrite},
		UsageJSON: json.RawMessage(`{"output_tokens_details":{"thinking_tokens":10},"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":50}}`),
	}}); err != nil {
		t.Fatal(err)
	}

	rep, err := OpenRemote(address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	stats, err := rep.KPIs(t.Context(), KPIOptions{
		Since: started.Add(-time.Hour), Until: started.Add(time.Hour), KnownProvidersOnly: true,
	})
	if err != nil {
		t.Fatalf("remote KPIs() error = %v", err)
	}
	if stats.Requests != 2 || stats.CacheHitRequests != 1 || stats.PeakConcurrency != 1 ||
		stats.LatencyP50MS == nil || *stats.LatencyP50MS != 1000 {
		t.Fatalf("remote KPI stats = %+v, want two 1000ms requests with one cache hit at peak 1", stats)
	}
	if stats.Anthropic.ThinkingTokens != 10 || stats.Anthropic.CacheWriteTokens != 100 ||
		stats.Anthropic.CacheWrite5MTokens != 40 || stats.Anthropic.CacheWrite1HTokens != 50 ||
		stats.Anthropic.CacheWriteUnclassifiedTokens != 10 {
		t.Fatalf("remote Anthropic KPI stats = %+v, want thinking=10 cache=100/40/50/10", stats.Anthropic)
	}
	recent, err := rep.RecentRequests(t.Context(), 1)
	if err != nil {
		t.Fatalf("remote RecentRequests() error = %v", err)
	}
	if len(recent) != 1 || recent[0].ID != "remote-anthropic" {
		t.Fatalf("remote recent requests = %+v", recent)
	}
}

func TestSummary_GroupsBySourceWithMixedProviderInput(t *testing.T) {
	dbPath := seedReportDB(t)
	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	rows, err := r.Summary(t.Context(), SummaryOptions{
		Since:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Until:   time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		GroupBy: "source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "unknown" {
		t.Fatalf("source rows = %+v", rows)
	}
	// OpenAI: (10 - 5) + 20; Anthropic: 7 - 3 cache-write.
	if rows[0].Input != 37 || rows[0].FreshInput != 29 {
		t.Fatalf("source input totals = %+v, want raw=37 fresh=29", rows[0])
	}
}

func TestSummary_GroupsAndFiltersByProjectContext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	totals := []int64{10, 20, 30, 40}
	events := []queue.UsageEvent{
		{RequestID: "main", Directory: "/work/api", GitBranch: "main", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &totals[0]}},
		{RequestID: "feature-api", Directory: "/work/api", GitBranch: "feature-x", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &totals[1]}},
		{RequestID: "feature-web", Directory: "/work/web", GitBranch: "feature-x", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &totals[2]}},
		{RequestID: "unknown", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &totals[3]}},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	rep := New(st)

	directories, err := rep.Summary(t.Context(), SummaryOptions{GroupBy: "directory", Branch: "feature-x"})
	if err != nil {
		t.Fatal(err)
	}
	gotDirectories := make(map[string]int64)
	for _, row := range directories {
		gotDirectories[row.Directory] = row.Total
	}
	if gotDirectories["/work/api"] != 20 || gotDirectories["/work/web"] != 30 || len(gotDirectories) != 2 {
		t.Fatalf("directory rows = %+v", directories)
	}

	branches, err := rep.Summary(t.Context(), SummaryOptions{GroupBy: "git_branch", Directory: "/work/api"})
	if err != nil {
		t.Fatal(err)
	}
	gotBranches := make(map[string]int64)
	for _, row := range branches {
		gotBranches[row.GitBranch] = row.Total
	}
	if gotBranches["main"] != 10 || gotBranches["feature-x"] != 20 || len(gotBranches) != 2 {
		t.Fatalf("git branch rows = %+v", branches)
	}

	rows, err := rep.Inspect(t.Context(), "feature-api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Directory != "/work/api" || rows[0].GitBranch != "feature-x" {
		t.Fatalf("inspect rows = %+v", rows)
	}
}

func TestSummaryGroupingAcceptsProjectContext(t *testing.T) {
	for _, groupBy := range []string{"directory", "git_branch"} {
		selectGroup, groupExpr, orderBy, err := summaryGrouping(groupBy)
		if err != nil || selectGroup == "" || groupExpr == "" || orderBy == "" {
			t.Fatalf("summaryGrouping(%q) = (%q, %q, %q, %v)", groupBy, selectGroup, groupExpr, orderBy, err)
		}
	}
}

func TestHistorical_ReadsLiveDuckDBAggregates(t *testing.T) {
	dbPath := seedReportDB(t)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
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

func TestHourlyTokens_AggregatesFreshInputAndFillsEmptyHours(t *testing.T) {
	dbPath := seedReportDB(t)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var (
		started = time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC)
		tokens  = int64(100)
	)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID:   "req-unknown-with-tokens",
		StartedAt:   started,
		CompletedAt: started.Add(time.Second),
		Method:      "GET",
		Path:        "/health",
		UpstreamURL: "http://localhost/health",
		Usage:       queue.Usage{InputTokens: &tokens, OutputTokens: &tokens},
	}}); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}

	since := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	rows, err := New(st).HourlyTokens(t.Context(), HourlyTokenOptions{
		Since:              since,
		Until:              since.Add(3 * time.Hour),
		KnownProvidersOnly: true,
	})
	if err != nil {
		t.Fatalf("HourlyTokens() error = %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("HourlyTokens() returned %d rows, want 3: %+v", len(rows), rows)
	}
	for i, row := range rows {
		wantHour := since.Add(time.Duration(i) * time.Hour)
		if !row.Hour.Equal(wantHour) {
			t.Fatalf("rows[%d].Hour = %s, want %s", i, row.Hour, wantHour)
		}
	}
	if rows[0].FreshInput != 0 || rows[0].Output != 0 || rows[2].FreshInput != 0 || rows[2].Output != 0 {
		t.Fatalf("empty hourly rows = %+v and %+v, want zero totals", rows[0], rows[2])
	}
	if rows[1].FreshInput != 29 || rows[1].Output != 26 {
		t.Fatalf("usage hour = %+v, want fresh input 29 and output 26", rows[1])
	}
}

func TestInspect_FindsByResponseID(t *testing.T) {
	dbPath := seedReportDB(t)

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
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

func TestRecentRequests_ReturnsNewestLightweightRows(t *testing.T) {
	dbPath := seedReportDB(t)

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.RecentRequests(t.Context(), 2)
	if err != nil {
		t.Fatalf("RecentRequests() error = %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].ID != "req-anthropic-1" || rows[0].ResponseID != "msg-anthropic-1" ||
		rows[0].Provider != "anthropic" || rows[0].Client != "Claude Code" || rows[0].Model != "claude-opus-5" ||
		!rows[0].HTTPStatus.Valid || rows[0].HTTPStatus.Int64 != 200 || rows[0].Method != "POST" || rows[0].Path != "/v1/messages" {
		t.Fatalf("newest row = %+v", rows[0])
	}
	if rows[1].ID != "req-openai-2" {
		t.Fatalf("second row = %+v, want req-openai-2", rows[1])
	}
}

func TestInspect_IncludesToolCalls(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
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

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
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

// ToolCalls avoids a SQL JOIN between requests and tool_calls (a JOIN across
// two tables fails over a Quack-attached catalog); it instead joins in Go
// after two single-table scans, expanding the requests window until enough
// tool calls are gathered. This test seeds requests with tool calls spaced
// out so the initial window (sized to the limit) undershoots and must expand,
// and asserts the result still comes back correctly ordered by request
// recency then ordinal, and capped at the limit.
func TestToolCalls_OrdersAcrossRequestsAndExpandsWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newEvent := func(id string, offset time.Duration, calls ...queue.ToolCall) queue.UsageEvent {
		started := base.Add(offset)
		return queue.UsageEvent{
			RequestID:   id,
			StartedAt:   started,
			CompletedAt: started.Add(time.Second),
			Method:      "POST",
			Path:        "/v1/messages",
			UpstreamURL: "https://api.anthropic.com/v1/messages",
			ToolCalls:   calls,
		}
	}
	bash := func(cmd string) queue.ToolCall { return queue.ToolCall{Name: "Bash", Command: cmd} }
	// Oldest to newest; only some requests carry tool calls, and the
	// most-recent request has none, so a window sized to the limit must
	// expand to reach the tool calls that satisfy it.
	events := []queue.UsageEvent{
		newEvent("req-1", 1*time.Minute, bash("one"), bash("two"), bash("three")),
		newEvent("req-2", 2*time.Minute, bash("four")),
		newEvent("req-3", 3*time.Minute),
		newEvent("req-4", 4*time.Minute, bash("five"), bash("six")),
		newEvent("req-5", 5*time.Minute),
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.ToolCalls(t.Context(), ToolCallOptions{Limit: 3})
	if err != nil {
		t.Fatalf("ToolCalls() error = %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3: %+v", len(rows), rows)
	}
	wantCommands := []string{"five", "six", "four"}
	for i, want := range wantCommands {
		if rows[i].RequestID != wantRequestFor(want) || rows[i].Command != want {
			t.Fatalf("rows[%d] = %+v, want command %q", i, rows[i], want)
		}
	}
}

func TestToolCalls_CommandsOnlySkipsEmptyCommandsAndExpandsWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newEvent := func(id string, offset time.Duration, calls ...queue.ToolCall) queue.UsageEvent {
		started := base.Add(offset)
		return queue.UsageEvent{
			RequestID:   id,
			StartedAt:   started,
			CompletedAt: started.Add(time.Second),
			Method:      "POST",
			Path:        "/v1/messages",
			UpstreamURL: "https://api.anthropic.com/v1/messages",
			ToolCalls:   calls,
		}
	}
	events := []queue.UsageEvent{
		newEvent("req-1", time.Minute, queue.ToolCall{Name: "Bash", Command: "older"}),
		newEvent("req-2", 2*time.Minute),
		newEvent("req-3", 3*time.Minute, queue.ToolCall{Name: "Bash", Command: "newer"}),
		newEvent("req-4", 4*time.Minute, queue.ToolCall{Name: "Read", Description: "read file"}),
		newEvent("req-5", 5*time.Minute, queue.ToolCall{Name: "Task", Description: "delegate work"}),
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.ToolCalls(t.Context(), ToolCallOptions{Limit: 2, CommandsOnly: true})
	if err != nil {
		t.Fatalf("ToolCalls() error = %v", err)
	}
	if len(rows) != 2 || rows[0].Command != "newer" || rows[1].Command != "older" {
		t.Fatalf("rows = %+v, want the two most recent non-empty commands", rows)
	}

	rows, err = r.ToolCalls(t.Context(), ToolCallOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ToolCalls() unfiltered error = %v", err)
	}
	if len(rows) != 2 || rows[0].Name != "Task" || rows[1].Name != "Read" {
		t.Fatalf("unfiltered rows = %+v, want metadata-only tool calls", rows)
	}
}

func wantRequestFor(command string) string {
	switch command {
	case "five", "six":
		return "req-4"
	case "four":
		return "req-2"
	default:
		return "req-1"
	}
}

func TestInspect_IncludesWebRequests(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
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

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
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

func TestWebRequests_IncludesBothProviders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
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
			CompletedAt: started.Add(2*time.Minute + time.Second), UpstreamURL: "http://example.com/",
			WebRequests: []queue.WebRequest{{Name: "GET", URL: "http://example.com/"}},
		},
	}); err != nil {
		_ = st.Close()
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
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
		t.Fatalf("WebRequests() returned %d rows, want 3: %+v", len(rows), rows)
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
func TestOpenWithHubKey_MissingDatabaseErrorsWithoutCreatingIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "usage.duckdb")

	r, err := OpenWithHubKey(dbPath, "", "")
	if err == nil {
		_ = r.Close()
		t.Fatal("OpenWithHubKey() error = nil, want a missing-database error")
	}
	if !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("OpenWithHubKey() error = %v, want it to name the path %s", err, dbPath)
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenWithHubKey() created %s; stat err = %v, want not-exist", dbPath, statErr)
	}
	if _, statErr := os.Stat(filepath.Dir(dbPath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenWithHubKey() created the parent directory of %s", dbPath)
	}
}

func TestRecentErrors_FiltersToErrorRows(t *testing.T) {
	dbPath := seedReportDBWithError(t)

	r, err := OpenWithHubKey(dbPath, "", "")
	if err != nil {
		t.Fatalf("OpenWithHubKey() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	rows, err := r.RecentErrorsWithin(t.Context(), time.Time{}, time.Time{}, 20)
	if err != nil {
		t.Fatalf("RecentErrorsWithin() error = %v", err)
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

	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
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

	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
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

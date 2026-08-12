package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

func TestOpen_CreatesSchemaAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s1.Close() })

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer s2.Close()

	var schemaObjectCount int
	if err := s2.db.QueryRow(`SELECT COUNT(1) FROM sqlite_master
		WHERE name IN (
			'requests',
			'idx_requests_started_at',
			'idx_requests_model_requested',
			'idx_requests_response_id',
			'usage_by_day_model',
			'usage_daily_model',
			'usage_model_histogram'
		)`).Scan(&schemaObjectCount); err != nil {
		t.Fatalf("query schema objects: %v", err)
	}
	if schemaObjectCount != 7 {
		t.Fatalf("schema object count = %d, want 7", schemaObjectCount)
	}
}

func TestOpen_UsesWALWithNormalSync(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var syncMode int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&syncMode); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if syncMode != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", syncMode)
	}
}

func TestOpenExisting_MissingFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "absent.sqlite")

	st, err := OpenExisting(dbPath)
	if err == nil {
		_ = st.Close()
		t.Fatal("OpenExisting() error = nil, want a missing-database error")
	}
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("OpenExisting() created %s", dbPath)
	}
}

// The daemon's Open still creates and migrates; OpenExisting must then accept
// the same file.
func TestOpenExisting_AcceptsDatabaseCreatedByOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ro, err := OpenExisting(dbPath)
	if err != nil {
		t.Fatalf("OpenExisting() error = %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })

	var n int
	if err := ro.DB().QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatalf("query through OpenExisting: %v", err)
	}
	if n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestOpen_MigratesClientColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE requests (
  id TEXT PRIMARY KEY,
  response_id TEXT,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  duration_ms INTEGER,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  upstream_url TEXT NOT NULL,
  model_requested TEXT,
  model_reported TEXT,
  stream INTEGER NOT NULL DEFAULT 0,
  http_status INTEGER,
  upstream_request_id TEXT,
  user_agent TEXT,
  codex_session_id TEXT,
  error_type TEXT,
  error_message TEXT,
  input_tokens INTEGER,
  cached_input_tokens INTEGER,
  cache_write_tokens INTEGER,
  output_tokens INTEGER,
  reasoning_tokens INTEGER,
  total_tokens INTEGER,
  usage_json TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create pre-client schema: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX idx_requests_started_at ON requests(started_at);
CREATE INDEX idx_requests_model_requested ON requests(model_requested);
CREATE INDEX idx_requests_response_id ON requests(response_id);
CREATE VIEW usage_by_day_model AS SELECT substr(started_at, 1, 10) AS day, COALESCE(model_reported, model_requested, 'unknown') AS model, COUNT(*) AS request_count, SUM(COALESCE(input_tokens, 0)) AS input_tokens, SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens, SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens, SUM(COALESCE(output_tokens, 0)) AS output_tokens, SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens, SUM(COALESCE(total_tokens, 0)) AS total_tokens FROM requests GROUP BY day, model`); err != nil {
		t.Fatalf("create pre-client schema objects: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-client db: %v", err)
	}

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cols, err := tableColumns(st.db, "requests")
	if err != nil {
		t.Fatalf("tableColumns() error = %v", err)
	}
	for _, col := range []string{"originator", "client_name"} {
		if !cols[col] {
			t.Fatalf("missing migrated column %q", col)
		}
	}
}

func TestOpen_RejectsStaleSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE requests (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create stale schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale db: %v", err)
	}

	s, err := Open(dbPath)
	if err == nil {
		_ = s.Close()
		t.Fatalf("Open() error = nil, want stale schema validation error")
	}
}

func TestDeleteRequestsStartedBefore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	oldStart := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newStart := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	events := []queue.UsageEvent{
		{RequestID: "old", StartedAt: oldStart, CompletedAt: oldStart.Add(time.Second), Method: "POST", Path: "/v1/responses", UpstreamURL: "https://api.openai.com/v1/responses", ToolCalls: []queue.ToolCall{{Name: "Bash", Command: "go test ./..."}}, WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: "old"}}},
		{RequestID: "new", StartedAt: newStart, CompletedAt: newStart.Add(time.Second), Method: "POST", Path: "/v1/messages", UpstreamURL: "https://api.anthropic.com/v1/messages"},
	}
	if err := s.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}

	deleted, err := s.DeleteRequestsStartedBefore(t.Context(), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DeleteRequestsStartedBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	var remainingID string
	if err := s.db.QueryRow(`SELECT id FROM requests`).Scan(&remainingID); err != nil {
		t.Fatalf("query remaining row: %v", err)
	}
	if remainingID != "new" {
		t.Fatalf("remaining id = %q, want new", remainingID)
	}
	var remainingTools int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&remainingTools); err != nil {
		t.Fatalf("count remaining tool calls: %v", err)
	}
	if remainingTools != 0 {
		t.Fatalf("remaining tool calls = %d, want 0 after retention cleanup", remainingTools)
	}
	var remainingWeb int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM web_requests`).Scan(&remainingWeb); err != nil {
		t.Fatalf("count remaining web requests: %v", err)
	}
	if remainingWeb != 0 {
		t.Fatalf("remaining web requests = %d, want 0 after retention cleanup", remainingWeb)
	}
}

func TestRefreshUsageAggregates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var (
		started = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		input   = int64(10)
		output  = int64(5)
		total   = int64(15)
	)
	events := []queue.UsageEvent{
		{
			RequestID:     "req-1",
			StartedAt:     started,
			CompletedAt:   started.Add(time.Second),
			Method:        "POST",
			Path:          "/v1/responses",
			UpstreamURL:   "https://api.openai.com/v1/responses",
			ModelReported: "gpt-5.3-codex",
			HTTPStatus:    200,
			UserAgent:     "codex-tui/0.146.0",
			Usage: queue.Usage{
				InputTokens:  &input,
				OutputTokens: &output,
				TotalTokens:  &total,
			},
		},
		{
			RequestID:     "req-2",
			StartedAt:     started.Add(time.Minute),
			CompletedAt:   started.Add(time.Minute + time.Second),
			Method:        "POST",
			Path:          "/v1/responses",
			UpstreamURL:   "https://api.openai.com/v1/responses",
			ModelReported: "gpt-5.3-codex",
			UserAgent:     "codex-tui/0.146.0",
			ErrorType:     "upstream_error",
			ErrorMessage:  "bad status",
		},
	}
	if err := s.InsertBatch(t.Context(), events); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}
	if err := s.RefreshUsageAggregates(t.Context()); err != nil {
		t.Fatalf("RefreshUsageAggregates() error = %v", err)
	}
	if err := s.RefreshUsageAggregates(t.Context()); err != nil {
		t.Fatalf("second RefreshUsageAggregates() error = %v", err)
	}

	var dailyRows, requestCount, errorCount, inputTokens, outputTokens, totalTokens int64
	if err := s.db.QueryRow(`SELECT COUNT(*), request_count, error_count, input_tokens, output_tokens, total_tokens
		FROM usage_daily_model
		WHERE day = '2026-08-01' AND provider = 'openai' AND client = 'Codex CLI' AND model = 'gpt-5.3-codex'`).
		Scan(&dailyRows, &requestCount, &errorCount, &inputTokens, &outputTokens, &totalTokens); err != nil {
		t.Fatalf("query daily aggregate: %v", err)
	}
	if dailyRows != 1 || requestCount != 2 || errorCount != 1 || inputTokens != 10 || outputTokens != 5 || totalTokens != 15 {
		t.Fatalf("daily aggregate = rows:%d req:%d err:%d input:%d output:%d total:%d, want 1/2/1/10/5/15",
			dailyRows, requestCount, errorCount, inputTokens, outputTokens, totalTokens)
	}

	var histogramRows int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_model_histogram
		WHERE provider = 'openai' AND client = 'Codex CLI' AND model = 'gpt-5.3-codex' AND request_count = 2`).Scan(&histogramRows); err != nil {
		t.Fatalf("query model histogram: %v", err)
	}
	if histogramRows != 1 {
		t.Fatalf("histogram rows = %d, want 1", histogramRows)
	}
}

func TestInsertBatch_RoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var (
		started = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		ended   = started.Add(500 * time.Millisecond)
		input   = int64(100)
		ev      = queue.UsageEvent{
			RequestID:      "req-1",
			ResponseID:     "resp-1",
			StartedAt:      started,
			CompletedAt:    ended,
			Method:         "POST",
			Path:           "/codex/responses",
			UpstreamURL:    "https://api.openai.com/v1/responses",
			ModelRequested: "gpt-5.3-codex",
			ModelReported:  "gpt-5.3-codex-2026-01-01",
			Stream:         false,
			HTTPStatus:     200,
			UserAgent:      "codex-tui/0.146.0",
			Originator:     "zed",
			ClientName:     "Zed",
			Usage:          queue.Usage{InputTokens: &input},
			ToolCalls: []queue.ToolCall{{
				ID:            "call-1",
				Name:          "Bash",
				Command:       "go test ./...",
				Description:   "Run tests",
				ArgumentsJSON: `{"command":"go test ./...","description":"Run tests"}`,
			}},
			WebRequests: []queue.WebRequest{{
				ID: "web-1", Name: "web_search_call", Query: "Go release", URL: "https://go.dev",
				ArgumentsJSON: `{"query":"Go release"}`,
			}},
		}
	)

	if err := s.InsertBatch(t.Context(), []queue.UsageEvent{ev}); err != nil {
		t.Fatalf("InsertBatch() error = %v", err)
	}

	row := s.db.QueryRow(`SELECT
		id, response_id, model_requested, model_reported, http_status,
		duration_ms, input_tokens, output_tokens, error_type, user_agent,
		originator, client_name
		FROM requests WHERE id = ?`, "req-1")

	var (
		id, responseID, modelRequested, modelReported string
		httpStatus                                    int
		durationMs                                    int64
		inputTokens                                   sql.NullInt64
		outputTokens                                  sql.NullInt64
		errorType                                     sql.NullString
		userAgent, originator, clientName             sql.NullString
	)
	if err := row.Scan(&id, &responseID, &modelRequested, &modelReported, &httpStatus,
		&durationMs, &inputTokens, &outputTokens, &errorType, &userAgent,
		&originator, &clientName); err != nil {
		t.Fatalf("scan row: %v", err)
	}

	if id != "req-1" || responseID != "resp-1" || modelRequested != "gpt-5.3-codex" || httpStatus != 200 {
		t.Fatalf("row = (id=%s, response_id=%s, model_requested=%s, status=%d), want req-1/resp-1/gpt-5.3-codex/200",
			id, responseID, modelRequested, httpStatus)
	}
	if durationMs != 500 {
		t.Fatalf("duration_ms = %d, want 500", durationMs)
	}
	if !inputTokens.Valid || inputTokens.Int64 != 100 {
		t.Fatalf("input_tokens = %+v, want valid 100", inputTokens)
	}
	if outputTokens.Valid {
		t.Fatalf("output_tokens = %+v, want NULL", outputTokens)
	}
	if errorType.Valid {
		t.Fatalf("error_type = %+v, want NULL", errorType)
	}
	if !userAgent.Valid || userAgent.String != "codex-tui/0.146.0" {
		t.Fatalf("user_agent = %+v, want codex-tui", userAgent)
	}
	if !originator.Valid || originator.String != "zed" {
		t.Fatalf("originator = %+v, want zed", originator)
	}
	if !clientName.Valid || clientName.String != "Zed" {
		t.Fatalf("client_name = %+v, want Zed", clientName)
	}

	var toolID, toolName, command, description, arguments string
	if err := s.db.QueryRow(`SELECT tool_call_id, name, command, description, arguments_json FROM tool_calls WHERE request_id = ?`, "req-1").
		Scan(&toolID, &toolName, &command, &description, &arguments); err != nil {
		t.Fatalf("query tool call: %v", err)
	}
	if toolID != "call-1" || toolName != "Bash" || command != "go test ./..." || description != "Run tests" || arguments != `{"command":"go test ./...","description":"Run tests"}` {
		t.Fatalf("tool call = (%q, %q, %q, %q, %q), want persisted Bash call", toolID, toolName, command, description, arguments)
	}
	var webName, webQuery, webURL, webArgs string
	if err := s.db.QueryRow(`SELECT name, query, url, arguments_json FROM web_requests WHERE request_id = ?`, "req-1").
		Scan(&webName, &webQuery, &webURL, &webArgs); err != nil {
		t.Fatalf("query web request: %v", err)
	}
	if webName != "web_search_call" || webQuery != "Go release" || webURL != "https://go.dev" || webArgs != `{"query":"Go release"}` {
		t.Fatalf("web request = (%q, %q, %q, %q), want persisted web search", webName, webQuery, webURL, webArgs)
	}
}

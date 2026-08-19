package store

import (
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

func TestOpenCreatesLiveDuckDBSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cols, err := tableColumns(st.db, "requests")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"started_at", "source", "host", "directory", "git_branch", "total_tokens"} {
		if !cols[name] {
			t.Fatalf("requests missing %q", name)
		}
	}
	if _, err := tableColumns(st.db, "web_requests"); err != nil {
		t.Fatalf("web_requests schema: %v", err)
	}
	var aggregateTables int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_name IN ('usage_daily_model', 'usage_model_histogram')`).Scan(&aggregateTables); err != nil {
		t.Fatal(err)
	}
	if aggregateTables != 0 {
		t.Fatalf("materialized aggregate tables = %d, want 0", aggregateTables)
	}
}

func TestQuackURI(t *testing.T) {
	for input, want := range map[string]string{
		"127.0.0.1:9494":           "quack:127.0.0.1:9494",
		"quack:localhost":          "quack:localhost",
		"quack://hub.example:9494": "quack:hub.example:9494",
	} {
		if got := QuackURI(input); got != want {
			t.Errorf("QuackURI(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenExistingReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.duckdb")
	if _, err := OpenExisting(path); err == nil {
		t.Fatal("OpenExisting missing file succeeded")
	} else if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("OpenExisting created the file")
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	if _, err := ro.db.Exec(`INSERT INTO requests (id, started_at, method, path, upstream_url)
		VALUES ('bad', current_timestamp, 'POST', '/', '/')`); err == nil {
		t.Fatal("read-only DuckDB accepted a write")
	}
}

func TestInsertBatchIsIdempotentAndPreservesTypes(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	input := int64(100)
	event := queue.UsageEvent{
		RequestID: "req-1", ResponseID: "resp-1", Source: "daniel", Host: "workstation",
		Directory: `C:\work\excursion-funnel`, GitBranch: "feature/project-context",
		StartedAt: started, CompletedAt: started.Add(500 * time.Millisecond),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "https://example.test/v1/responses",
		HTTPStatus: 200, Usage: queue.Usage{InputTokens: &input},
		ToolCalls:   []queue.ToolCall{{ID: "call-1", Name: "Bash", Command: "go test ./..."}},
		WebRequests: []queue.WebRequest{{ID: "web-1", Name: "web_search_call", Query: "DuckDB"}},
	}
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{event, event}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	var count, toolCount, webCount, duration int64
	var gotStarted time.Time
	var source, host, directory, gitBranch string
	if err := st.db.QueryRow(`SELECT COUNT(*), min(started_at), min(source), min(host), min(directory), min(git_branch), min(duration_ms) FROM requests`).
		Scan(&count, &gotStarted, &source, &host, &directory, &gitBranch, &duration); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM web_requests`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	if count != 1 || toolCount != 1 || webCount != 1 || duration != 500 || source != "daniel" || host != "workstation" || directory != `C:\work\excursion-funnel` || gitBranch != "feature/project-context" || !gotStarted.Equal(started) {
		t.Fatalf("round trip count=%d tools=%d web=%d duration=%d source=%q host=%q directory=%q git_branch=%q started=%v", count, toolCount, webCount, duration, source, host, directory, gitBranch, gotStarted)
	}
}

func TestOpenUpgradesProjectContextColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openDuckDB(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP VIEW usage_by_day_model",
		"DROP INDEX idx_requests_started_at",
		"DROP INDEX idx_requests_model_requested",
		"DROP INDEX idx_requests_response_id",
		"DROP INDEX idx_requests_source",
		"DROP INDEX idx_requests_directory",
		"DROP INDEX idx_requests_git_branch",
		"ALTER TABLE requests DROP COLUMN directory",
		"ALTER TABLE requests DROP COLUMN git_branch",
	} {
		if _, err := db.Exec(query); err != nil {
			_ = db.Close()
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("Open() upgrade error = %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	cols, err := tableColumns(upgraded.db, "requests")
	if err != nil {
		t.Fatal(err)
	}
	if !cols["directory"] || !cols["git_branch"] {
		t.Fatalf("upgraded columns = %+v", cols)
	}
}

func TestDeleteRequestsStartedBefore(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	old := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newer := old.AddDate(0, 1, 0)
	events := []queue.UsageEvent{
		{RequestID: "old", StartedAt: old, CompletedAt: old.Add(time.Second), Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ToolCalls: []queue.ToolCall{{Name: "Bash"}}, WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: "old"}}},
		{RequestID: "new", StartedAt: newer, CompletedAt: newer.Add(time.Second), Method: "POST", Path: "/v1/messages", UpstreamURL: "/"},
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.DeleteRequestsStartedBefore(t.Context(), newer)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	var webCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM web_requests`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	if webCount != 0 {
		t.Fatalf("remaining web requests = %d, want 0", webCount)
	}
}

func TestOutboxRoundTripAndAcknowledge(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	event := queue.UsageEvent{RequestID: "req-1", Source: "daniel", Directory: "/work/api", GitBranch: "main", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"}
	if err := outbox.InsertBatch(t.Context(), []queue.UsageEvent{event, event}); err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(t.Context(), 50)
	if err != nil || len(pending) != 1 || pending[0].Source != "daniel" || pending[0].Directory != "/work/api" || pending[0].GitBranch != "main" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := outbox.Acknowledge(t.Context(), []string{"req-1"}); err != nil {
		t.Fatal(err)
	}
	count, err := outbox.Count(t.Context())
	if err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestMigrateSQLiteLegacySchema(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "usage.sqlite")
	duckPath := filepath.Join(dir, "usage.duckdb")
	legacy, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE requests (
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
  created_at TEXT NOT NULL
);
CREATE TABLE tool_calls (
  request_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  tool_call_id TEXT,
  name TEXT NOT NULL,
  command TEXT,
  description TEXT,
  arguments_json TEXT,
  PRIMARY KEY (request_id, ordinal)
);
CREATE TABLE web_requests (
  request_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  web_request_id TEXT,
  name TEXT NOT NULL,
  query TEXT,
  url TEXT,
  domain TEXT,
  arguments_json TEXT,
  PRIMARY KEY (request_id, ordinal)
);
INSERT INTO requests (
  id, response_id, started_at, completed_at, duration_ms, method, path,
  upstream_url, model_requested, model_reported, stream, http_status,
  input_tokens, output_tokens, total_tokens, created_at
) VALUES (
  'legacy-1', 'resp-1', '2026-08-17T12:34:56+02:00',
  '2026-08-17T12:35:00+02:00', 4000, 'POST', '/v1/responses',
  'https://example.test', 'gpt-5', 'gpt-5', 1, 200, 10, 4, 14,
  '2026-08-17T12:35:00+02:00'
);
INSERT INTO tool_calls VALUES ('legacy-1', 0, 'call-1', 'shell', 'echo ok', NULL, '{}');
INSERT INTO web_requests VALUES ('legacy-1', 0, 'web-1', 'web_search_call', 'DuckDB migration', NULL, NULL, '{}');`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// A rerun is intentionally harmless so migration can be resumed safely.
	for range 2 {
		if err := MigrateSQLite(t.Context(), duckPath, sqlitePath); err != nil {
			t.Fatal(err)
		}
	}
	st, err := OpenExisting(duckPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var (
		source                                       string
		started                                      time.Time
		originator, clientName, directory, gitBranch sql.NullString
	)
	if err := st.db.QueryRow(`SELECT source, started_at, originator, client_name, directory, git_branch
FROM requests WHERE id = 'legacy-1'`).Scan(&source, &started, &originator, &clientName, &directory, &gitBranch); err != nil {
		t.Fatal(err)
	}
	if source != "legacy" || !started.Equal(time.Date(2026, 8, 17, 10, 34, 56, 0, time.UTC)) {
		t.Fatalf("source=%q started=%s", source, started)
	}
	if originator.Valid || clientName.Valid || directory.Valid || gitBranch.Valid {
		t.Fatalf("missing legacy fields should migrate as NULL: originator=%+v client=%+v directory=%+v git_branch=%+v", originator, clientName, directory, gitBranch)
	}
	var requestCount, toolCount, webCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM web_requests`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || toolCount != 1 || webCount != 1 {
		t.Fatalf("migrated counts: requests=%d tools=%d web=%d", requestCount, toolCount, webCount)
	}
}

func TestQuackRoundTrip(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	ledger, err := Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	server, err := ledger.StartQuack(t.Context(), address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	var answer int
	if err := ledger.db.QueryRowContext(t.Context(),
		"SELECT * FROM quack_query(?, 'SELECT 42', token = ?)",
		server.URI, "test-token",
	).Scan(&answer); err != nil {
		t.Fatalf("quack_query: %v", err)
	}
	if answer != 42 {
		t.Fatalf("quack_query answer = %d, want 42", answer)
	}
	if bad, err := OpenRemote(t.Context(), address, "wrong-token", false); err == nil {
		_ = bad.Close()
		t.Fatal("OpenRemote accepted a bad token")
	}
	remote, err := OpenRemote(t.Context(), address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	event := queue.UsageEvent{
		RequestID: "remote-1", Source: "integration", StartedAt: time.Now(),
		Directory: "/work/api", GitBranch: "main",
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ErrorMessage: "it's recoverable",
		ToolCalls:   []queue.ToolCall{{ID: "call-1", Name: "shell", Command: "echo 'ok'"}},
		WebRequests: []queue.WebRequest{{ID: "web-1", Name: "web_search_call", Query: "DuckDB Quack"}},
	}
	for range 2 {
		if err := remote.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
			t.Fatal(err)
		}
	}
	var source, directory, gitBranch, errorMessage string
	if err := ledger.db.QueryRow(`SELECT source, directory, git_branch, error_message FROM requests WHERE id = 'remote-1'`).Scan(&source, &directory, &gitBranch, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if source != "integration" || directory != "/work/api" || gitBranch != "main" || errorMessage != "it's recoverable" {
		t.Fatalf("source=%q directory=%q git_branch=%q error_message=%q", source, directory, gitBranch, errorMessage)
	}
	var requestCount, toolCount, webCount int
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE id = 'remote-1'`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE request_id = 'remote-1'`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM web_requests WHERE request_id = 'remote-1'`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || toolCount != 1 || webCount != 1 {
		t.Fatalf("idempotent remote counts: requests=%d tools=%d web=%d", requestCount, toolCount, webCount)
	}
	var remoteCount int
	if err := remote.DB().QueryRow(`SELECT COUNT(*) FROM requests WHERE id = ?`, "remote-1").Scan(&remoteCount); err != nil {
		t.Fatal(err)
	}
	if remoteCount != 1 {
		t.Fatalf("remote read count = %d, want 1", remoteCount)
	}
}

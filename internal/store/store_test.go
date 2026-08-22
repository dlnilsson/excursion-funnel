package store

import (
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
	for _, name := range []string{"started_at", "source", "host", "session_id", "directory", "git_branch", "total_tokens"} {
		if !cols[name] {
			t.Fatalf("requests missing %q", name)
		}
	}
	if _, err := tableColumns(st.db, "web_requests"); err != nil {
		t.Fatalf("web_requests schema: %v", err)
	}
	if _, err := tableColumns(st.db, "sessions"); err != nil {
		t.Fatalf("sessions schema: %v", err)
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

func TestQuackSessionAuthenticator(t *testing.T) {
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
	if _, err := ledger.StartQuackAuthenticated(t.Context(), address, "internal-token", func(token string) bool { return token == "session-token" }); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRemote(t.Context(), address, "internal-token", false); err == nil {
		t.Fatal("internal token authenticated remotely")
	}
	remote, err := OpenRemote(t.Context(), address, "session-token", false)
	if err != nil {
		t.Fatalf("session token rejected: %v", err)
	}
	_ = remote.Close()
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
		SessionID: "session-1", Directory: `C:\work\excursion-funnel`, GitBranch: "feature/project-context",
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
	var source, host, sessionID, directory, gitBranch string
	if err := st.db.QueryRow(`SELECT COUNT(*), min(started_at), min(source), min(host), min(session_id), min(directory), min(git_branch), min(duration_ms) FROM requests`).
		Scan(&count, &gotStarted, &source, &host, &sessionID, &directory, &gitBranch, &duration); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM web_requests`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	var sessionCount int64
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE provider = 'openai' AND session_id = 'session-1' AND first_seen_at = ?`, started).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if count != 1 || toolCount != 1 || webCount != 1 || sessionCount != 1 || duration != 500 || source != "daniel" || host != "workstation" || sessionID != "session-1" || directory != `C:\work\excursion-funnel` || gitBranch != "feature/project-context" || !gotStarted.Equal(started) {
		t.Fatalf("round trip count=%d tools=%d web=%d sessions=%d duration=%d source=%q host=%q session=%q directory=%q git_branch=%q started=%v", count, toolCount, webCount, sessionCount, duration, source, host, sessionID, directory, gitBranch, gotStarted)
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
		"DROP INDEX idx_requests_session_id",
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

func TestOpenBackfillsLegacyCodexSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if _, err := st.db.Exec(`INSERT INTO requests (
id, started_at, method, path, upstream_url, codex_session_id, created_at
) VALUES ('legacy', ?, 'POST', '/v1/responses', '/', 'legacy-session', ?)`, started, started); err != nil {
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
		"DROP INDEX idx_requests_session_id",
		"ALTER TABLE requests DROP COLUMN session_id",
		"DROP INDEX idx_sessions_first_seen_at",
		"DROP TABLE sessions",
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	var requestSession, provider, registrySession string
	var firstSeen time.Time
	if err := upgraded.db.QueryRow(`SELECT session_id FROM requests WHERE id = 'legacy'`).Scan(&requestSession); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT provider, session_id, first_seen_at FROM sessions WHERE session_id = 'legacy-session'`).
		Scan(&provider, &registrySession, &firstSeen); err != nil {
		t.Fatal(err)
	}
	if requestSession != "legacy-session" || provider != "openai" || registrySession != "legacy-session" || !firstSeen.Equal(started) {
		t.Fatalf("backfill request=%q registry=%q/%q/%v", requestSession, provider, registrySession, firstSeen)
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
		{RequestID: "old", SessionID: "preserved-session", StartedAt: old, CompletedAt: old.Add(time.Second), Method: "POST", Path: "/v1/responses", UpstreamURL: "/", ToolCalls: []queue.ToolCall{{Name: "Bash"}}, WebRequests: []queue.WebRequest{{Name: "web_search_call", Query: "old"}}},
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
	var preserved int64
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = 'preserved-session' AND first_seen_at = ?`, old).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != 1 {
		t.Fatalf("preserved sessions = %d, want 1", preserved)
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

func TestOutboxDecodesLegacyCodexSessionField(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	payload := `{"RequestID":"legacy","StartedAt":"2026-08-01T12:00:00Z","Method":"POST","Path":"/v1/responses","UpstreamURL":"/","CodexSessionID":"legacy-session"}`
	if _, err := outbox.db.Exec(`INSERT INTO usage_outbox (id, payload) VALUES ('legacy', ?)`, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SessionID != "" || pending[0].CodexSessionID != "legacy-session" {
		t.Fatalf("legacy pending event = %+v", pending)
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
		RequestID: "remote-1", Source: "integration", SessionID: "remote-session", StartedAt: time.Now(),
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
	var requestCount, sessionCount, toolCount, webCount int
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE id = 'remote-1'`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE request_id = 'remote-1'`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM web_requests WHERE request_id = 'remote-1'`).Scan(&webCount); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE provider = 'openai' AND session_id = 'remote-session'`).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || sessionCount != 1 || toolCount != 1 || webCount != 1 {
		t.Fatalf("idempotent remote counts: requests=%d sessions=%d tools=%d web=%d", requestCount, sessionCount, toolCount, webCount)
	}
	var remoteCount int
	if err := remote.DB().QueryRow(`SELECT COUNT(*) FROM requests WHERE id = ?`, "remote-1").Scan(&remoteCount); err != nil {
		t.Fatal(err)
	}
	if remoteCount != 1 {
		t.Fatalf("remote read count = %d, want 1", remoteCount)
	}
}

func TestQuackForwardsSessionsToLegacyHubAndBackfillsOnUpgrade(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	path := filepath.Join(t.TempDir(), "legacy-hub.duckdb")
	ledger, err := Open(path)
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
		"DROP INDEX idx_requests_session_id",
		"ALTER TABLE requests DROP COLUMN session_id",
		"DROP INDEX idx_sessions_first_seen_at",
		"DROP TABLE sessions",
	} {
		if _, err := ledger.db.Exec(query); err != nil {
			_ = ledger.Close()
			t.Fatalf("%s: %v", query, err)
		}
	}
	if _, err := ledger.StartQuack(t.Context(), address, "test-token", false); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	remote, err := OpenRemote(t.Context(), address, "test-token", false)
	if err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	events := []queue.UsageEvent{
		{RequestID: "legacy-openai", SessionID: "openai-session", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/"},
		{RequestID: "legacy-anthropic", SessionID: "anthropic-session", StartedAt: started.Add(time.Minute), Method: "POST", Path: "/v1/messages", UpstreamURL: "/"},
	}
	if err := remote.InsertBatch(t.Context(), events); err != nil {
		_ = remote.Close()
		_ = ledger.Close()
		t.Fatal(err)
	}
	if err := remote.Close(); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	var legacyIDs string
	if err := ledger.db.QueryRow(`SELECT string_agg(codex_session_id, ',' ORDER BY id)
FROM requests WHERE id IN ('legacy-openai', 'legacy-anthropic')`).Scan(&legacyIDs); err != nil {
		_ = ledger.Close()
		t.Fatal(err)
	}
	if legacyIDs != "anthropic-session,openai-session" {
		_ = ledger.Close()
		t.Fatalf("legacy transported session IDs = %q", legacyIDs)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	var openAICount, anthropicCount int
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM sessions
WHERE provider = 'openai' AND session_id = 'openai-session'`).Scan(&openAICount); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM sessions
WHERE provider = 'anthropic' AND session_id = 'anthropic-session'`).Scan(&anthropicCount); err != nil {
		t.Fatal(err)
	}
	if openAICount != 1 || anthropicCount != 1 {
		t.Fatalf("upgraded session counts = openai:%d anthropic:%d", openAICount, anthropicCount)
	}
}

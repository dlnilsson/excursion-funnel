package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
	proxyproto "github.com/pires/go-proxyproto"
)

// TestMain clears hub/quack env vars so a developer's shell (e.g. one already
// configured to point `ef` at a live team hub) can't redirect these tests
// away from the temp databases they set up via --db.
func TestMain(m *testing.M) {
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR"} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

func TestLoggerRedactsSecretLikeAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)

	log.Info("test",
		"authorization", "Bearer sk-test",
		"upstream_url", "https://example.test/v1/messages?access_token=abc",
		"err", errString("request failed with Authorization: Bearer sk-test"))

	out := buf.String()
	for _, secret := range []string{"sk-test", "access_token=abc", "Authorization: Bearer"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output leaked %q: %s", secret, out)
		}
	}
	if got := strings.Count(out, "[REDACTED]"); got < 3 {
		t.Fatalf("log output redaction count = %d, want at least 3: %s", got, out)
	}
}

func TestLocalTimestamp_ConvertsToLocalZone(t *testing.T) {
	const input = "2026-08-02T19:53:49.514Z"

	parsed, err := time.Parse(time.RFC3339Nano, input)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.Local().Format("2006-01-02T15:04:05.000Z07:00")
	if got := localTimestamp(parsed); got != want {
		t.Fatalf("localTimestamp(%q) = %q, want %q", input, got, want)
	}
}

// `usage today --since X` used to silently report today and discard the range.
func TestRunUsage_TodayWithExplicitRangeIsRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")

	for _, args := range [][]string{
		{"today", "--db", dbPath, "--since", "2026-07-01"},
		{"today", "--db", dbPath, "--until", "2026-07-01"},
	} {
		err := runUsage(args)
		if !errors.Is(err, errTodayWithRange) {
			t.Fatalf("runUsage(%v) error = %v, want errTodayWithRange", args, err)
		}
	}
}

// The reporting commands must not create the ledger they read.
func TestRunUsage_MissingDatabaseIsAnError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "absent.duckdb")

	if err := runUsage([]string{"today", "--db", dbPath}); err == nil {
		t.Fatal("runUsage() error = nil, want a missing-database error")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runUsage() created %s", dbPath)
	}
}

func TestRunUsage_TodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	input, output, total := int64(10), int64(4), int64(14)
	started := beginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID:     "req-today-json",
		StartedAt:     started,
		CompletedAt:   started.Add(time.Second),
		Method:        "POST",
		Path:          "/v1/responses",
		UpstreamURL:   "https://api.openai.com/v1/responses",
		ModelReported: "gpt-5.3-codex",
		HTTPStatus:    200,
		Usage: queue.Usage{
			InputTokens:  &input,
			OutputTokens: &output,
			TotalTokens:  &total,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runUsageTo([]string{"today", "--json", "--db", dbPath}, &out); err != nil {
		t.Fatalf("runUsageTo() error = %v", err)
	}

	var rows []report.SummaryRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("JSON output is invalid: %v: %s", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	if got := rows[0]; got.Provider != "openai" || got.Model != "gpt-5.3-codex" || got.Requests != 1 || got.Total != total {
		t.Fatalf("row = %+v, want openai/gpt-5.3-codex with 1 request and %d total tokens", got, total)
	}
	// `usage today` scopes to a single day, so the row must carry that date
	// rather than an empty Day field even when grouped by model.
	if wantDay := beginningOfDay(time.Now()).Format("2006-01-02"); rows[0].Day != wantDay {
		t.Fatalf("Day = %q, want %q", rows[0].Day, wantDay)
	}
}

func TestRunUsage_ProjectContextGroupingAndFilters(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(14)
	started := beginningOfDay(time.Now()).Add(time.Hour)
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{
		{RequestID: "matching", Directory: "/work/api", GitBranch: "feature-x", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
		{RequestID: "other", Directory: "/work/web", GitBranch: "main", StartedAt: started, Method: "POST", Path: "/v1/responses", UpstreamURL: "/", Usage: queue.Usage{TotalTokens: &total}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runUsageTo([]string{"today", "--db", dbPath, "--group-by", "git_branch", "--directory", "/work/api", "--branch", "feature-x"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "GIT_BRANCH") || !strings.Contains(out.String(), "feature-x") || strings.Contains(out.String(), "main") {
		t.Fatalf("project context output = %s", out.String())
	}
}

func TestRunTools_Today(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID:   "req-tool-today",
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Method:      "POST",
		Path:        "/v1/messages",
		UpstreamURL: "https://api.anthropic.com/v1/messages",
		ToolCalls: []queue.ToolCall{{
			Name:        "Bash",
			Description: "Run tests",
			Command:     "go test ./...",
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runToolsTo([]string{"today", "--db", dbPath}, &out); err != nil {
		t.Fatalf("runToolsTo() error = %v", err)
	}
	for _, want := range []string{"STARTED", "Bash", "Run tests", "go test ./..."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
}

func TestRunTools_TodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID:   "req-tool-today-json",
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Method:      "POST",
		Path:        "/v1/messages",
		UpstreamURL: "https://api.anthropic.com/v1/messages",
		ToolCalls: []queue.ToolCall{{
			Name:        "Bash",
			Description: "Run tests",
			Command:     "go test ./...",
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runToolsTo([]string{"today", "--json", "--db", dbPath}, &out); err != nil {
		t.Fatalf("runToolsTo() error = %v", err)
	}

	var rows []report.ToolCallRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("JSON output is invalid: %v: %s", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %+v", len(rows), rows)
	}
	if got := rows[0]; got.Name != "Bash" || got.Description != "Run tests" || got.Command != "go test ./..." {
		t.Fatalf("row = %+v, want Bash/Run tests/go test ./...", got)
	}
}

func TestRunTools_RequiresToday(t *testing.T) {
	if err := runTools([]string{"yesterday"}); err == nil {
		t.Fatal("runTools() error = nil, want usage error")
	}
}

func TestPrintUsageJSON_EmptyRowsIsArray(t *testing.T) {
	var out bytes.Buffer
	if err := printUsageJSON(&out, nil); err != nil {
		t.Fatalf("printUsageJSON() error = %v", err)
	}
	if got := out.String(); got != "[]\n" {
		t.Fatalf("JSON output = %q, want empty array", got)
	}
}

func TestPrintToolJSON_EmptyRowsIsArray(t *testing.T) {
	var out bytes.Buffer
	if err := printToolJSON(&out, nil); err != nil {
		t.Fatalf("printToolJSON() error = %v", err)
	}
	if got := out.String(); got != "[]\n" {
		t.Fatalf("JSON output = %q, want empty array", got)
	}
}

func TestRunInspect_RejectsNonPositiveLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")

	if err := runInspect([]string{"--db", dbPath, "--limit", "0", "req-1"}); err == nil {
		t.Fatal("runInspect() error = nil, want a --limit validation error")
	}
}

func TestRequireLoopbackAddr(t *testing.T) {
	if err := requireLoopbackAddr("127.0.0.1:9495"); err != nil {
		t.Fatalf("loopback rejected: %v", err)
	}
	if err := requireLoopbackAddr("0.0.0.0:9495"); err == nil {
		t.Fatal("public bind accepted")
	}
}

func TestGatewayListenerUsesProxyProtocolRemoteAddr(t *testing.T) {
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newGatewayListener(rawListener)
	remoteAddr := make(chan string, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remoteAddr <- r.RemoteAddr
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve() error = %v, want http.ErrServerClosed", err)
		}
	})

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	source := &net.TCPAddr{IP: net.ParseIP("100.64.0.42"), Port: 43210}
	header := proxyproto.HeaderProxyFromAddrs(2, source, conn.RemoteAddr())
	if _, err := header.WriteTo(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: hub.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if got := <-remoteAddr; got != "100.64.0.42:43210" {
		t.Fatalf("RemoteAddr = %q, want %q", got, "100.64.0.42:43210")
	}
}

func TestGatewayListenerRejectsHeaderlessConnection(t *testing.T) {
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newGatewayListener(rawListener)
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := fmt.Fprint(client, "GET / HTTP/1.1\r\nHost: hub.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept() error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("Accept() timed out")
	}
	defer serverConn.Close()
	if _, err := serverConn.Read(make([]byte, 1)); !errors.Is(err, proxyproto.ErrNoProxyProtocol) {
		t.Fatalf("Read() error = %v, want proxyproto.ErrNoProxyProtocol", err)
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}

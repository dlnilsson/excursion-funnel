package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

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
	if got := localTimestamp(input); got != want {
		t.Fatalf("localTimestamp(%q) = %q, want %q", input, got, want)
	}
}

func TestLocalTimestamp_PreservesInvalidValue(t *testing.T) {
	const input = "not-a-timestamp"
	if got := localTimestamp(input); got != input {
		t.Fatalf("localTimestamp(%q) = %q, want unchanged", input, got)
	}
}

// `usage today --since X` used to silently report today and discard the range.
func TestRunUsage_TodayWithExplicitRangeIsRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")

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
	dbPath := filepath.Join(t.TempDir(), "absent.sqlite")

	if err := runUsage([]string{"today", "--db", dbPath}); err == nil {
		t.Fatal("runUsage() error = nil, want a missing-database error")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runUsage() created %s", dbPath)
	}
}

func TestRunUsage_TodayJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

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

func TestRunTools_Today(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

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

func TestRunInspect_RejectsNonPositiveLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")

	if err := runInspect([]string{"--db", dbPath, "--limit", "0", "req-1"}); err == nil {
		t.Fatal("runInspect() error = nil, want a --limit validation error")
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}

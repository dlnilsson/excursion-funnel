package inspect

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestRunWithoutIDPrintsRecentRowsForRedirectedStreams(t *testing.T) {
	dbPath := seedInspectDB(t, []queue.UsageEvent{{
		RequestID:     "req-recent",
		ResponseID:    "resp-recent",
		StartedAt:     time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC),
		CompletedAt:   time.Date(2026, time.August, 20, 12, 0, 1, 0, time.UTC),
		Method:        "POST",
		Path:          "/v1/responses",
		UpstreamURL:   "https://api.openai.com/v1/responses",
		ModelReported: "gpt-5.6-sol",
		ClientName:    "Codex CLI",
		HTTPStatus:    200,
	}})
	var output bytes.Buffer
	err := Run(t.Context(), strings.NewReader(""), &output, "", Options{
		Connection: reporting.Connection{DBPath: dbPath},
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{"STARTED", "CLIENT", "MODEL", "STATUS", "req-recent", "gpt-5.6-sol"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "tokens:") {
		t.Fatalf("recent list unexpectedly rendered inspect detail:\n%s", output.String())
	}
}

func TestRunWithoutIDReportsEmptyLedger(t *testing.T) {
	dbPath := seedInspectDB(t, nil)
	var output bytes.Buffer
	err := Run(t.Context(), strings.NewReader(""), &output, "", Options{
		Connection: reporting.Connection{DBPath: dbPath},
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := output.String(); got != "no requests recorded\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunWithIDPreservesDetailedInspectOutput(t *testing.T) {
	dbPath := seedInspectDB(t, []queue.UsageEvent{{
		RequestID:   "req-detail",
		StartedAt:   time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, time.August, 20, 12, 0, 1, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/messages",
		UpstreamURL: "https://api.anthropic.com/v1/messages",
		HTTPStatus:  200,
	}})
	var output bytes.Buffer
	err := Run(t.Context(), strings.NewReader(""), &output, "req-detail", Options{
		Connection: reporting.Connection{DBPath: dbPath},
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{"id: req-detail", "request: POST /v1/messages", "tokens:"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}

func seedInspectDB(t *testing.T, events []queue.UsageEvent) string {
	t.Helper()
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR"} {
		t.Setenv(key, "")
	}
	dbPath := filepath.Join(t.TempDir(), "usage.duckdb")
	ledger, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	if len(events) > 0 {
		if err := ledger.InsertBatch(t.Context(), events); err != nil {
			_ = ledger.Close()
			t.Fatalf("InsertBatch() error = %v", err)
		}
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return dbPath
}

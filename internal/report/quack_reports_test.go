package report

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestQuackReportingParameters(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ledger, err := store.OpenHub(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	const (
		source = "alice's ? workstation"
		id     = "request-?'one"
	)
	var (
		started = reporting.BeginningOfHour(time.Now()).Add(-3 * time.Hour)
		since   = started.Add(-time.Hour)
		until   = started.Add(time.Hour)
		events  = []queue.UsageEvent{{
			RequestID: id, Source: source, SessionID: "session-a", StartedAt: started, CompletedAt: started.Add(time.Second),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 200,
			Usage:       queue.Usage{InputTokens: new(int64(17)), OutputTokens: new(int64(3)), TotalTokens: new(int64(20))},
			ToolCalls:   []queue.ToolCall{{ID: "call-?'one", Name: "shell", Command: "false"}},
			ToolResults: []queue.ToolResult{{ID: "call-?'one", ExitCode: new(int64(1)), Failed: true}},
			WebRequests: []queue.WebRequest{{ID: "web-1", Name: "web_search_call", Query: "query"}},
		}, {
			RequestID: "request-two", Source: source, SessionID: "session-a", StartedAt: started.Add(2 * time.Minute), CompletedAt: started.Add(2*time.Minute + time.Second),
			Method: "POST", Path: "/v1/responses", UpstreamURL: "/", HTTPStatus: 500, ErrorType: "upstream_error",
		}}
	)
	if err := ledger.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}
	rep, err := OpenRemote(address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	t.Run("hourly tokens", func(t *testing.T) {
		rows, err := rep.HourlyTokens(t.Context(), HourlyTokenOptions{Since: since, Until: until})
		if err != nil {
			t.Fatal(err)
		}
		var input, output int64
		for _, row := range rows {
			input += row.FreshInput
			output += row.Output
		}
		if input != 17 || output != 3 {
			t.Fatalf("hourly input=%d output=%d", input, output)
		}
	})
	t.Run("token activity", func(t *testing.T) {
		rows, err := rep.TokenActivity(t.Context(), TokenActivityOptions{Since: since, Until: until, Bucket: "hour", Source: source})
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, row := range rows {
			total += row.Total
		}
		if total != 20 {
			t.Fatalf("activity tokens=%d", total)
		}
	})
	t.Run("KPIs and concurrency", func(t *testing.T) {
		stats, err := rep.KPIs(t.Context(), KPIOptions{Since: since, Until: until})
		if err != nil || stats.Requests != 2 || stats.Errors != 1 || stats.PeakConcurrency != 1 {
			t.Fatalf("KPIs=%+v, %v", stats, err)
		}
	})
	t.Run("recent requests and integer limit", func(t *testing.T) {
		rows, err := rep.RecentRequests(t.Context(), 1, source)
		if err != nil || len(rows) != 1 || rows[0].ID != "request-two" {
			t.Fatalf("recent requests=%+v, %v", rows, err)
		}
		rows, err = rep.RecentRequests(t.Context(), 1, "' OR true --")
		if err != nil || len(rows) != 0 {
			t.Fatalf("source filter escaped its literal: %+v, %v", rows, err)
		}
	})
	t.Run("inspect and child lookups", func(t *testing.T) {
		rows, err := rep.Inspect(t.Context(), id, 1, source)
		if err != nil || len(rows) != 1 || len(rows[0].ToolCalls) != 1 || len(rows[0].WebRequests) != 1 {
			t.Fatalf("inspect=%+v, %v", rows, err)
		}
	})
	t.Run("recent errors", func(t *testing.T) {
		rows, err := rep.RecentErrorsWithin(t.Context(), since, until, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != "request-two" {
			t.Fatalf("errors=%+v, %v", rows, err)
		}
	})
	t.Run("tools and outcomes", func(t *testing.T) {
		rows, err := rep.ToolCalls(t.Context(), ToolCallOptions{Since: since, Until: until, Source: source, FailedOnly: true, Limit: 1})
		if err != nil || len(rows) != 1 || rows[0].ID != "call-?'one" || rows[0].ExitCode == nil || *rows[0].ExitCode != 1 {
			t.Fatalf("tools=%+v, %v", rows, err)
		}
	})
	t.Run("web requests", func(t *testing.T) {
		rows, err := rep.WebRequests(t.Context(), ToolCallOptions{Since: since, Until: until, Source: source, Limit: 1})
		if err != nil || len(rows) != 1 || rows[0].ID != "web-1" {
			t.Fatalf("web requests=%+v, %v", rows, err)
		}
	})
	t.Run("longest session", func(t *testing.T) {
		span, ok, err := rep.LongestSession(t.Context(), TokenActivityOptions{Since: since, Until: until, Source: source})
		if err != nil || !ok || span != 2*time.Minute {
			t.Fatalf("longest session=%s, %t, %v", span, ok, err)
		}
	})
	t.Run("sessions", func(t *testing.T) {
		rows, err := rep.Sessions(t.Context(), SessionOptions{Since: since, Until: until, Source: source})
		if err != nil || len(rows) != 1 || rows[0].Started != 1 || rows[0].Used != 1 {
			t.Fatalf("sessions=%+v, %v", rows, err)
		}
	})
}

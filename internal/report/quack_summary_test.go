package report

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestSummaryQuackFiltered(t *testing.T) {
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
	started := time.Date(2026, 11, 8, 12, 0, 0, 0, time.FixedZone("CET", 3600))
	if err := ledger.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "summary-filtered", Source: "alice's laptop", StartedAt: started,
		Path: "/v1/responses", Method: "POST", UpstreamURL: "/", ModelReported: "test-model",
		Directory: "/work/it's?", GitBranch: "feature/query?",
		Usage: queue.Usage{InputTokens: new(int64(17)), OutputTokens: new(int64(3)), TotalTokens: new(int64(20))},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}
	reporter, err := OpenRemote(address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reporter.Close() })
	for _, source := range []string{"alice's laptop", "' OR true --"} {
		rows, err := reporter.Summary(t.Context(), SummaryOptions{
			Since: started.Add(-time.Hour), Until: started.Add(time.Hour), Source: source,
			Directory: "/work/it's?", Branch: "feature/query?",
		})
		if err != nil {
			t.Fatal(err)
		}
		if source == "alice's laptop" {
			if len(rows) != 1 || rows[0].Requests != 1 || rows[0].Total != 20 {
				t.Fatalf("filtered summary = %+v", rows)
			}
		} else if len(rows) != 0 {
			t.Fatalf("source filter escaped its literal: %+v", rows)
		}
	}
}

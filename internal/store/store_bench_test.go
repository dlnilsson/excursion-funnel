package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

func BenchmarkInsertBatch(b *testing.B) {
	for _, size := range []int{1, 10, 50, 250} {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			dbPath := filepath.Join(b.TempDir(), "usage.duckdb")
			s, err := Open(dbPath)
			if err != nil {
				b.Fatalf("Open() error = %v", err)
			}
			b.Cleanup(func() { _ = s.Close() })

			events := benchmarkEvents(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				for j := range events {
					events[j].RequestID = fmt.Sprintf("req-%d-%d", i, j)
					events[j].ResponseID = fmt.Sprintf("resp-%d-%d", i, j)
				}
				if err := s.InsertBatch(b.Context(), events); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkEvents(n int) []queue.UsageEvent {
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	input := int64(100)
	output := int64(40)
	total := int64(140)

	events := make([]queue.UsageEvent, n)
	for i := range events {
		events[i] = queue.UsageEvent{
			RequestID:         fmt.Sprintf("req-%d", i),
			ResponseID:        fmt.Sprintf("resp-%d", i),
			StartedAt:         started.Add(time.Duration(i) * time.Second),
			CompletedAt:       started.Add(time.Duration(i)*time.Second + 500*time.Millisecond),
			Method:            "POST",
			Path:              "/v1/responses",
			UpstreamURL:       "https://api.openai.com/v1/responses",
			ModelRequested:    "gpt-5.3-codex",
			ModelReported:     "gpt-5.3-codex",
			HTTPStatus:        200,
			UpstreamRequestID: fmt.Sprintf("upstream-%d", i),
			UserAgent:         "codex-tui/0.146.0",
			ClientName:        "Codex CLI",
			Usage: queue.Usage{
				InputTokens:  &input,
				OutputTokens: &output,
				TotalTokens:  &total,
			},
		}
	}
	return events
}

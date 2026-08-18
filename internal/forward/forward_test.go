package forward

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestForwarderDrainsOutboxToQuackHub(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	hub, err := store.Open(filepath.Join(t.TempDir(), "hub.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	if _, err := hub.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}
	outbox, err := store.OpenOutbox(filepath.Join(t.TempDir(), "outbox.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	event := queue.UsageEvent{
		RequestID: "forwarded-1", Source: "integration", StartedAt: time.Now(),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/",
		ToolCalls: []queue.ToolCall{{ID: "call-1", Name: "shell", Command: "echo ok"}},
	}
	if err := outbox.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	forwarder := New(outbox, Config{
		Address: address, Token: "test-token", PollInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	forwarder.Start()
	t.Cleanup(forwarder.Stop)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := outbox.WaitUntilEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := hub.DB().QueryRow(`SELECT source FROM requests WHERE id = 'forwarded-1'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "integration" {
		t.Fatalf("source = %q", source)
	}
	var toolCount int
	if err := hub.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE request_id = 'forwarded-1'`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if toolCount != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCount)
	}
}

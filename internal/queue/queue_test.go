package queue

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/testutil"
)

type fakeWriter struct {
	mu     sync.Mutex
	events []UsageEvent
	block  chan struct{} // if non-nil, InsertBatch waits for it to close
}

func (f *fakeWriter) InsertBatch(_ context.Context, events []UsageEvent) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, events...)
	return nil
}

func (f *fakeWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestQueue_EnqueueFlushesToWriter(t *testing.T) {
	var (
		fw  = &fakeWriter{}
		cfg = Config{Capacity: 10, BatchSize: 5, FlushInterval: 20 * time.Millisecond, EnqueueTimeout: time.Second}
		q   = New(cfg, fw, testLogger())
	)
	q.Start()
	t.Cleanup(func() { _ = q.Close(time.Second) })

	q.Enqueue(UsageEvent{RequestID: "req-1"})

	deadline := time.Now().Add(2 * time.Second)
	for fw.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fw.count(); got != 1 {
		t.Fatalf("writer received %d events, want 1", got)
	}
}

func TestQueue_BackpressureDropsOnFullQueue(t *testing.T) {
	var (
		block = make(chan struct{})
		fw    = &fakeWriter{block: block}
		cfg   = Config{Capacity: 1, BatchSize: 1, FlushInterval: time.Hour, EnqueueTimeout: 30 * time.Millisecond}
		q     = New(cfg, fw, testLogger())
	)
	q.Start()
	defer close(block)
	t.Cleanup(func() { _ = q.Close(time.Second) })

	// First event is picked up by the writer goroutine and blocks inside
	// InsertBatch; the second fills the channel buffer; the third has
	// nowhere to go and must time out and be dropped.
	q.Enqueue(UsageEvent{RequestID: "req-1"})
	time.Sleep(50 * time.Millisecond) // let req-1 be claimed by run()'s select
	q.Enqueue(UsageEvent{RequestID: "req-2"})

	done := make(chan struct{})
	go func() {
		q.Enqueue(UsageEvent{RequestID: "req-3"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not return after EnqueueTimeout; backpressure did not drop the event")
	}

	if got := q.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1", got)
	}
}

func TestQueue_CloseDrainsBufferedEvents(t *testing.T) {
	var (
		fw  = &fakeWriter{}
		cfg = Config{Capacity: 10, BatchSize: 50, FlushInterval: time.Hour, EnqueueTimeout: time.Second}
		q   = New(cfg, fw, testLogger())
	)
	q.Start()

	q.Enqueue(UsageEvent{RequestID: "req-1"})
	q.Enqueue(UsageEvent{RequestID: "req-2"})

	if err := q.Close(time.Second); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := fw.count(); got != 2 {
		t.Fatalf("writer received %d events after Close, want 2", got)
	}
	testutil.AssertNoGoroutineLeaks(t, "internal/queue.(*Queue).run")
}

func TestNewWebRequestProjectsAndRedactsPayload(t *testing.T) {
	request := NewWebRequest("web-1", "web_search", []byte(`{"query":"Go release","url":"https://go.dev","api_key":"secret"}`))
	if request.ID != "web-1" || request.Name != "web_search" || request.Query != "Go release" || request.URL != "https://go.dev" {
		t.Fatalf("request = %+v, want projected web fields", request)
	}
	if strings.Contains(request.ArgumentsJSON, "secret") || !strings.Contains(request.ArgumentsJSON, "REDACTED") {
		t.Fatalf("ArgumentsJSON = %q, want redacted api key", request.ArgumentsJSON)
	}
}

func TestIsWebToolName(t *testing.T) {
	webTools := []string{
		"web_search_call", // OpenAI / Codex
		"web_search",      // Anthropic server-side + synthetic
		"web_fetch",       // Anthropic server-side
		"web-fetch",       // Anthropic hyphenated variant
		"WebSearch",       // Claude Code client-side
		"WebFetch",        // Claude Code client-side
	}
	for _, name := range webTools {
		if !IsWebToolName(name) {
			t.Errorf("IsWebToolName(%q) = false, want true", name)
		}
	}

	nonWeb := []string{"GET", "POST", "CONNECT", "Bash", "Read", "", "search"}
	for _, name := range nonWeb {
		if IsWebToolName(name) {
			t.Errorf("IsWebToolName(%q) = true, want false", name)
		}
	}
}

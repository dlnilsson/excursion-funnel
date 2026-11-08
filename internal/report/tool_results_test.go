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

func TestToolResultsFilterAndScope(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := time.Now().Add(-time.Hour)
	event := func(id string, minute int) queue.UsageEvent {
		return queue.UsageEvent{RequestID: id, Source: "alice", Host: "host-a", SessionID: "session-a",
			StartedAt: base.Add(time.Duration(minute) * time.Minute), Method: "POST", Path: "/v1/responses", UpstreamURL: "/"}
	}
	call := event("calls", 0)
	call.ToolCalls = []queue.ToolCall{
		{ID: "ok", Name: "shell", Command: "true"},
		{ID: "bad", Name: "shell", Command: "false"},
		{ID: "unknown", Name: "Read"},
	}
	result := event("results", 1)
	result.ToolResults = []queue.ToolResult{{ID: "ok", ExitCode: new(int64(0))}, {ID: "bad", ExitCode: new(int64(2)), Failed: true}}
	newer := event("newer-call", 2)
	newer.ToolCalls = []queue.ToolCall{{ID: "newer", Name: "shell", Command: "pwd"}}
	events := []queue.UsageEvent{result, call, newer} // Results can arrive before invocations.
	for i, scope := range []string{"source", "host", "session", "provider"} {
		other := event("other-"+scope, 3+i)
		switch scope {
		case "source":
			other.Source = "bob"
		case "host":
			other.Host = "host-b"
		case "session":
			other.SessionID = "session-b"
		case "provider":
			other.Path = "/v1/messages"
		}
		other.ToolResults = []queue.ToolResult{{ID: "bad", ExitCode: new(int64(0))}, {ID: "unknown", Failed: true}}
		events = append(events, other)
	}
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	// Replayed batches must not duplicate outcomes.
	if err := st.InsertBatch(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	rep := New(st)
	rows, err := rep.ToolCalls(t.Context(), ToolCallOptions{Limit: 1, FailedOnly: true})
	if err != nil || len(rows) != 1 || rows[0].ID != "bad" || rows[0].ExitCode == nil || *rows[0].ExitCode != 2 {
		t.Fatalf("failed calls = %+v, %v", rows, err)
	}
	// The failure follows a successful sibling and an entirely newer call;
	// filtering must happen before limiting, including within a single request.
	rows, err = rep.ToolCalls(t.Context(), ToolCallOptions{Limit: 10})
	if err != nil || len(rows) != 4 {
		t.Fatalf("all calls = %+v, %v", rows, err)
	}
	if rows[0].Failed != nil || rows[1].Failed == nil || *rows[1].Failed || rows[2].Failed == nil || !*rows[2].Failed || rows[3].Failed != nil {
		t.Fatalf("unknown/success/failure outcomes are incorrect: %+v", rows)
	}
	// Read-only reports remain usable against a pre-feature ledger.
	if _, err := st.DB().ExecContext(t.Context(), "DROP TABLE tool_results"); err != nil {
		t.Fatal(err)
	}
	rows, err = rep.ToolCalls(t.Context(), ToolCallOptions{FailedOnly: true})
	if err != nil || len(rows) != 0 {
		t.Fatalf("legacy failed calls = %+v, %v", rows, err)
	}
}

func TestToolResultsQuackRemote(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	st, err := store.OpenHub(filepath.Join(t.TempDir(), "hub.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.StartQuack(t.Context(), address, "test-token", false); err != nil {
		t.Fatal(err)
	}
	remote, err := store.OpenRemote(t.Context(), address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	event := queue.UsageEvent{RequestID: "remote-'tools", StartedAt: time.Now(), Path: "/v1/responses", Method: "POST", UpstreamURL: "/",
		ToolCalls:   []queue.ToolCall{{ID: "call-'1", Name: "shell", Command: "false"}},
		ToolResults: []queue.ToolResult{{ID: "call-'1", ExitCode: new(int64(1)), Failed: true}}}
	if err := remote.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := remote.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeStaging(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := New(remote).ToolCalls(t.Context(), ToolCallOptions{FailedOnly: true, Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ExitCode == nil || *rows[0].ExitCode != 1 {
		t.Fatalf("remote failed tools = %+v, %v", rows, err)
	}
	// A client upgraded before its hub must continue forwarding ordinary usage.
	if _, err := st.DB().ExecContext(t.Context(), "DROP TABLE staging_tool_results"); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.OpenRemote(t.Context(), address, "test-token", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	event.RequestID = "legacy-hub"
	if err := legacy.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatalf("forward usage to older hub: %v", err)
	}
	var staged int
	if err := st.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM staging_requests WHERE id = 'legacy-hub'").Scan(&staged); err != nil || staged != 1 {
		t.Fatalf("older hub usage count = %d, %v", staged, err)
	}
}

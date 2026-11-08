package tools

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestFailedToolsIncludeNonCommandsAndMarkFailures(t *testing.T) {
	for _, key := range []string{"EF_HUB_ADDR", "EF_HUB_TOKEN", "EF_HUB_INSECURE", "EF_QUACK_ADDR", "EF_DB", "EF_HUB_KEY"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "usage.duckdb")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "tools", StartedAt: time.Now(), Path: "/v1/messages", Method: "POST", UpstreamURL: "/",
		ToolCalls:   []queue.ToolCall{{ID: "ok", Name: "Bash", Command: "true"}, {ID: "bad", Name: "Read", Description: "missing file"}},
		ToolResults: []queue.ToolResult{{ID: "ok", ExitCode: new(int64(0))}, {ID: "bad", Failed: true}},
	}}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(t.Context(), strings.NewReader(""), &output, Options{
		Connection: reporting.Connection{DBPath: path, DefaultDBPath: path}, Limit: 1, FailedOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "FAILED") || !strings.Contains(text, "Read missing file") || strings.Contains(text, "true") {
		t.Fatalf("failed output = %s", text)
	}
}

func TestFailureMarkerDoesNotChangeCopiedCommand(t *testing.T) {
	row := report.ToolCallRow{Command: "exit 2", ExitCode: new(int64(2)), Failed: new(true)}
	title, _, filter := commandPickerOptions(false, tea.SetClipboard).Render(row)
	if !strings.Contains(title, "FAILED (exit 2)") || !strings.Contains(filter, "failed") || row.Command != "exit 2" {
		t.Fatalf("picker title=%q filter=%q command=%q", title, filter, row.Command)
	}
}

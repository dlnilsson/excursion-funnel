package tools

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestPrintJSONEmptyRowsIsArray(t *testing.T) {
	var output bytes.Buffer
	if err := printJSON(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "[]\n" {
		t.Fatalf("output = %q, want empty array", got)
	}
}

func TestPrintRowsRendersUsageStyleTable(t *testing.T) {
	rows := []report.ToolCallRow{{
		StartedAt:   time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Client:      "codex",
		Model:       "gpt-5.6-sol",
		Name:        "shell_command",
		Description: "inspect files",
		Command:     "rg --files",
	}}
	var output bytes.Buffer
	if err := printRows(&output, rows); err != nil {
		t.Fatal(err)
	}

	text := output.String()
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("redirected output contains ANSI escapes: %q", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") ||
		!strings.HasPrefix(lines[len(lines)-1], "╰") || !strings.HasSuffix(lines[len(lines)-1], "╯") {
		t.Fatalf("output does not have the expected compact rounded table shape:\n%s", text)
	}
	for _, want := range []string{"STARTED", "CLIENT", "MODEL", "TOOL", "DESCRIPTION", "COMMAND", "codex", "gpt-5.6-sol", "shell_command", "rg --files"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRowsWrapsLongValuesWithinTableWidth(t *testing.T) {
	rows := []report.ToolCallRow{
		{Client: "codex", Model: "gpt-5.6-sol", Name: "exec", Command: strings.Repeat("long-command ", 30)},
		{Client: "codex", Model: "gpt-5.6-sol", Name: "exec", Description: strings.Repeat("long description ", 20)},
	}
	var output bytes.Buffer
	if err := printRows(&output, rows); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) <= 6 {
		t.Fatalf("long cells did not wrap onto continuation lines:\n%s", output.String())
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > defaultTableWidth {
			t.Fatalf("rendered line width = %d, want at most %d:\n%s", width, defaultTableWidth, output.String())
		}
	}
	if separators := strings.Count(output.String(), "\n├"); separators < 2 {
		t.Fatalf("wrapped tool calls are not separated by a row border:\n%s", output.String())
	}
}

func TestPrintRowsEmptyRowsKeepsMessage(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "no tool calls recorded today\n" {
		t.Fatalf("output = %q, want empty-state message", got)
	}
}

func TestPrintRowsPropagatesWriteErrors(t *testing.T) {
	errWrite := errors.New("write failed")
	if err := printRows(toolErrorWriter{err: errWrite}, []report.ToolCallRow{{Client: "codex"}}); !errors.Is(err, errWrite) {
		t.Fatalf("printRows() error = %v, want %v", err, errWrite)
	}
}

type toolErrorWriter struct {
	err error
}

func (w toolErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

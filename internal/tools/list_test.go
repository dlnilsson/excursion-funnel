package tools

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/termio"
)

func TestShouldUseCommandList(t *testing.T) {
	tests := []struct {
		name           string
		json           bool
		stdinTerminal  bool
		stdoutTerminal bool
		want           bool
	}{
		{name: "interactive", stdinTerminal: true, stdoutTerminal: true, want: true},
		{name: "json", json: true, stdinTerminal: true, stdoutTerminal: true},
		{name: "redirected input", stdoutTerminal: true},
		{name: "redirected output", stdinTerminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseCommandList(test.json, test.stdinTerminal, test.stdoutTerminal); got != test.want {
				t.Fatalf("shouldUseCommandList() = %t, want %t", got, test.want)
			}
		})
	}
	if termio.IsTerminal(strings.NewReader("")) || termio.IsTerminal(&bytes.Buffer{}) {
		t.Fatal("buffered streams detected as terminals")
	}
}

func TestCommandPickerPreservesExactCommandAndVerboseMetadata(t *testing.T) {
	row := report.ToolCallRow{
		StartedAt:   time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Client:      "codex",
		Model:       "gpt-5.6-sol",
		Name:        "shell_command",
		Description: "inspect\nfiles",
		Command:     "printf 'one\ntwo'",
	}
	title, description, filterValue := commandPickerOptions(true, tea.SetClipboard).Render(row)
	if filterValue != row.Command {
		t.Fatalf("filter value = %q, want exact %q", filterValue, row.Command)
	}
	if title != "printf 'one ↩ two'" {
		t.Fatalf("title = %q, want compact command", title)
	}
	for _, want := range []string{"started=", "client=codex", "model=gpt-5.6-sol", "tool=shell_command", "description=inspect ↩ files"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q: %s", want, description)
		}
	}

	if _, normalDescription, _ := commandPickerOptions(false, tea.SetClipboard).Render(row); normalDescription != "" {
		t.Fatalf("normal description = %q, want empty", normalDescription)
	}
}

func TestCommandPickerSelectionCopiesExactCommandAndQuits(t *testing.T) {
	const command = "printf 'one\ntwo'"
	var copied string
	opts := commandPickerOptions(false, func(value string) tea.Cmd {
		copied = value
		return nil
	})

	cmd := opts.OnSelect(report.ToolCallRow{Command: command})
	if copied != command {
		t.Fatalf("copied = %q, want exact %q", copied, command)
	}
	if cmd == nil {
		t.Fatal("selection did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("selection command message = %T, want tea.QuitMsg", cmd())
	}
}

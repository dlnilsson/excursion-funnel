package tools

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/dlnilsson/excursion-funnel/internal/report"
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
	if isTerminalReader(strings.NewReader("")) || isTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("buffered streams detected as terminals")
	}
}

func TestCommandListItemsPreserveExactCommandAndVerboseMetadata(t *testing.T) {
	row := report.ToolCallRow{
		StartedAt:   time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Client:      "codex",
		Model:       "gpt-5.6-sol",
		Name:        "shell_command",
		Description: "inspect\nfiles",
		Command:     "printf 'one\ntwo'",
	}
	model := newCommandListModel([]report.ToolCallRow{row}, true)
	item, ok := model.list.Items()[0].(commandItem)
	if !ok {
		t.Fatalf("item type = %T, want commandItem", model.list.Items()[0])
	}
	if item.command != row.Command || item.FilterValue() != row.Command {
		t.Fatalf("stored command = %q, want exact %q", item.command, row.Command)
	}
	if item.Title() != "printf 'one ↩ two'" {
		t.Fatalf("title = %q, want compact command", item.Title())
	}
	for _, want := range []string{"started=", "client=codex", "model=gpt-5.6-sol", "tool=shell_command", "description=inspect ↩ files"} {
		if !strings.Contains(item.Description(), want) {
			t.Fatalf("description missing %q: %s", want, item.Description())
		}
	}

	normal := newCommandListModel([]report.ToolCallRow{row}, false)
	normalItem := normal.list.Items()[0].(commandItem)
	if normalItem.Description() != "" {
		t.Fatalf("normal description = %q, want empty", normalItem.Description())
	}
}

func TestCommandListEnterCopiesExactCommandAndQuits(t *testing.T) {
	const command = "printf 'one\ntwo'"
	model := newCommandListModel([]report.ToolCallRow{{Command: command}}, false)
	var copied string
	model.setClipboard = func(value string) tea.Cmd {
		copied = value
		return nil
	}

	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if copied != command {
		t.Fatalf("copied = %q, want exact %q", copied, command)
	}
	if cmd == nil {
		t.Fatal("Enter did not return a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("Enter command message = %T, want tea.QuitMsg", msg)
	}
	if updated.(commandListModel).list.SelectedItem() == nil {
		t.Fatal("updated model lost its selected item")
	}
}

func TestCommandListQuitDoesNotCopy(t *testing.T) {
	model := newCommandListModel([]report.ToolCallRow{{Command: "pwd"}}, false)
	copied := false
	model.setClipboard = func(string) tea.Cmd {
		copied = true
		return nil
	}

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if copied {
		t.Fatal("quit copied the selected command")
	}
	if cmd == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestCommandListEnterAppliesActiveFilterWithoutCopying(t *testing.T) {
	model := newCommandListModel([]report.ToolCallRow{{Command: "go test ./..."}}, false)
	model.list.SetFilterText("go")
	model.list.SetFilterState(list.Filtering)
	copied := false
	model.setClipboard = func(string) tea.Cmd {
		copied = true
		return nil
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if copied {
		t.Fatal("Enter copied while the filter editor was active")
	}
	if updated.(commandListModel).list.FilterState() == list.Filtering {
		t.Fatal("Enter did not apply the active filter")
	}
}

func TestCommandListResizesAndUsesAlternateScreen(t *testing.T) {
	model := newCommandListModel([]report.ToolCallRow{{Command: "pwd"}}, false)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	resized := updated.(commandListModel)
	if resized.list.Width() != 100 || resized.list.Height() != 40 {
		t.Fatalf("list size = %dx%d, want 100x40", resized.list.Width(), resized.list.Height())
	}
	if !resized.View().AltScreen {
		t.Fatal("command list does not request the alternate screen")
	}
}

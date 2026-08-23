package tools

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dlnilsson/excursion-funnel/internal/picker"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const defaultListWidth = 80

func shouldUseCommandList(json, stdinTerminal, stdoutTerminal bool) bool {
	return !json && stdinTerminal && stdoutTerminal
}

// commandPickerOptions describes the command picker. setClipboard is a
// parameter rather than a direct tea.SetClipboard call so tests can observe
// exactly what would reach the clipboard.
func commandPickerOptions(verbose bool, setClipboard func(string) tea.Cmd) picker.Options[report.ToolCallRow] {
	return picker.Options[report.ToolCallRow]{
		Title:           "Commands today",
		ItemName:        "command",
		ItemPlural:      "commands",
		ActionHelp:      "copy",
		ShowDescription: verbose,
		Width:           defaultListWidth,
		Render: func(row report.ToolCallRow) (title, description, filterValue string) {
			if verbose {
				description = commandMetadata(row)
			}
			// The title is compacted so a multi-line command stays on one row,
			// but filtering matches the raw command so a search for the text the
			// user actually typed still finds it.
			return reporting.CompactValue(row.Command), description, row.Command
		},
		OnSelect: func(row report.ToolCallRow) tea.Cmd {
			return tea.Sequence(setClipboard(row.Command), tea.Quit)
		},
	}
}

func commandMetadata(row report.ToolCallRow) string {
	parts := []string{
		"started=" + reporting.LocalTimestamp(row.StartedAt),
		"client=" + reporting.EmptyAsDash(row.Client),
		"model=" + reporting.EmptyAsDash(row.Model),
		"tool=" + reporting.EmptyAsDash(row.Name),
		"description=" + reporting.EmptyAsDash(reporting.CompactValue(row.Description)),
	}
	return strings.Join(parts, "  •  ")
}

func runCommandList(ctx context.Context, in io.Reader, out io.Writer, rows []report.ToolCallRow, verbose bool) error {
	_, _, err := picker.Run(ctx, in, out, rows, commandPickerOptions(verbose, tea.SetClipboard))
	return err
}

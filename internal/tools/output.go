package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/term"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const (
	defaultTableWidth = 120
	maxTableWidth     = 140
)

func printRows(out io.Writer, rows []report.ToolCallRow, verbose bool) error {
	if len(rows) == 0 {
		return printEmpty(out)
	}

	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		if verbose {
			values = append(values, []string{
				reporting.LocalTimestamp(row.StartedAt), reporting.EmptyAsDash(row.Client), reporting.EmptyAsDash(row.Model),
				reporting.EmptyAsDash(row.Name), reporting.EmptyAsDash(compactValue(row.Description)), reporting.EmptyAsDash(compactValue(row.Command)),
			})
			continue
		}
		values = append(values, []string{compactValue(row.Command)})
	}
	headers := []string{"COMMAND"}
	if verbose {
		headers = []string{"STARTED", "CLIENT", "MODEL", "TOOL", "DESCRIPTION", "COMMAND"}
	}

	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	toolsTable := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(true).
		Width(outputWidth(out)).
		Wrap(true).
		Headers(headers...).
		Rows(values...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		})

	_, err := lipgloss.Fprintln(out, toolsTable.Render())
	return err
}

func printEmpty(out io.Writer) error {
	_, err := fmt.Fprintln(out, "no commands recorded today")
	return err
}

func outputWidth(out io.Writer) int {
	width := defaultTableWidth
	if file, ok := out.(interface{ Fd() uintptr }); ok {
		if terminalWidth, _, err := term.GetSize(file.Fd()); err == nil && terminalWidth > 1 {
			// Leave the last terminal column unused so terminals that eagerly wrap
			// at the right edge do not add a blank line after each rendered row.
			width = terminalWidth - 1
		}
	}
	return min(width, maxTableWidth)
}

func printJSON(out io.Writer, rows []report.ToolCallRow) error {
	if rows == nil {
		rows = []report.ToolCallRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func compactValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ↩ ")
	value = strings.ReplaceAll(value, "\n", " ↩ ")
	return strings.ReplaceAll(value, "\r", " ↩ ")
}

package tools

import (
	"fmt"
	"io"

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
				reporting.EmptyAsDash(row.Name), row.Status(), reporting.EmptyAsDash(reporting.CompactValue(row.Description)),
				reporting.EmptyAsDash(reporting.CompactValue(row.Command)),
			})
			continue
		}
		command := toolTitle(row)
		if row.Failed != nil && *row.Failed {
			command = "[" + row.Status() + "] " + command
		}
		values = append(values, []string{command})
	}
	headers := []string{"COMMAND"}
	if verbose {
		headers = []string{"STARTED", "CLIENT", "MODEL", "TOOL", "STATUS", "DESCRIPTION", "COMMAND"}
	}

	return reporting.RenderTable(out, reporting.Table{
		Headers:   headers,
		Rows:      values,
		BorderRow: true,
		Width:     reporting.TerminalWidth(out, defaultTableWidth, maxTableWidth),
		Wrap:      true,
	})
}

func toolTitle(row report.ToolCallRow) string {
	if row.Command != "" {
		return reporting.CompactValue(row.Command)
	}
	return reporting.CompactValue(row.Name + " " + row.Description)
}

func printEmpty(out io.Writer) error {
	_, err := fmt.Fprintln(out, "no commands recorded today")
	return err
}

func printJSON(out io.Writer, rows []report.ToolCallRow) error {
	return reporting.WriteJSONRows(out, rows)
}

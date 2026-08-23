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
				reporting.EmptyAsDash(row.Name), reporting.EmptyAsDash(reporting.CompactValue(row.Description)),
				reporting.EmptyAsDash(reporting.CompactValue(row.Command)),
			})
			continue
		}
		values = append(values, []string{reporting.CompactValue(row.Command)})
	}
	headers := []string{"COMMAND"}
	if verbose {
		headers = []string{"STARTED", "CLIENT", "MODEL", "TOOL", "DESCRIPTION", "COMMAND"}
	}

	return reporting.RenderTable(out, reporting.Table{
		Headers:   headers,
		Rows:      values,
		BorderRow: true,
		Width:     reporting.TerminalWidth(out, defaultTableWidth, maxTableWidth),
		Wrap:      true,
	})
}

func printEmpty(out io.Writer) error {
	_, err := fmt.Fprintln(out, "no commands recorded today")
	return err
}

func printJSON(out io.Writer, rows []report.ToolCallRow) error {
	return reporting.WriteJSONRows(out, rows)
}

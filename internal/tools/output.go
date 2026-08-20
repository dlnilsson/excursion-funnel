package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

func printRows(out io.Writer, rows []report.ToolCallRow) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no tool calls recorded today")
		return
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STARTED\tCLIENT\tMODEL\tTOOL\tDESCRIPTION\tCOMMAND")
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			reporting.LocalTimestamp(row.StartedAt), reporting.EmptyAsDash(row.Client), reporting.EmptyAsDash(row.Model),
			reporting.EmptyAsDash(row.Name), reporting.EmptyAsDash(compactValue(row.Description)), reporting.EmptyAsDash(compactValue(row.Command)))
	}
	_ = writer.Flush()
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

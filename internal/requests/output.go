package requests

import (
	"fmt"
	"io"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

func printRows(out io.Writer, rows []report.WebRequestRow, today bool) error {
	if len(rows) == 0 {
		message := "no web requests recorded"
		if today {
			message += " today"
		}
		_, err := fmt.Fprintln(out, message)
		return err
	}

	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		destination := row.URL
		if destination == "" {
			destination = row.Domain
		}
		values = append(values, []string{
			reporting.LocalTimestamp(row.StartedAt), reporting.EmptyAsDash(row.Client), reporting.EmptyAsDash(row.Model),
			reporting.EmptyAsDash(row.Name), reporting.EmptyAsDash(reporting.CompactValue(row.Query)),
			reporting.EmptyAsDash(reporting.CompactValue(destination)),
		})
	}

	return reporting.RenderTable(out, reporting.Table{
		Headers: []string{"STARTED", "CLIENT", "MODEL", "WEB TOOL", "QUERY", "URL/DOMAIN"},
		Rows:    values,
	})
}

func printJSON(out io.Writer, rows []report.WebRequestRow) error {
	return reporting.WriteJSONRows(out, rows)
}

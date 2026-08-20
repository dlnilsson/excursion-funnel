package requests

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
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
			reporting.EmptyAsDash(row.Name), reporting.EmptyAsDash(compactValue(row.Query)), reporting.EmptyAsDash(compactValue(destination)),
		})
	}

	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	requestsTable := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(false).
		Headers("STARTED", "CLIENT", "MODEL", "WEB TOOL", "QUERY", "URL/DOMAIN").
		Rows(values...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		})

	_, err := lipgloss.Fprintln(out, requestsTable.Render())
	return err
}

func printJSON(out io.Writer, rows []report.WebRequestRow) error {
	if rows == nil {
		rows = []report.WebRequestRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func compactValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ↩ ")
	value = strings.ReplaceAll(value, "\n", " ↩ ")
	return strings.ReplaceAll(value, "\r", " ↩ ")
}

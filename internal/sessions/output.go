package sessions

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func printJSON(out io.Writer, rows []report.SessionRow) error {
	if rows == nil {
		rows = []report.SessionRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func printRows(out io.Writer, rows []report.SessionRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no session rows")
		return err
	}
	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, []string{row.Period, row.Provider, strconv.FormatInt(row.Started, 10), strconv.FormatInt(row.Used, 10)})
	}
	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	sessionsTable := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(false).
		Headers("PERIOD", "PROVIDER", "STARTED", "USED").
		Rows(values...).
		StyleFunc(func(row, column int) lipgloss.Style {
			style := cellStyle
			if row == table.HeaderRow {
				style = headerStyle
			}
			if column >= 2 {
				style = style.Align(lipgloss.Right)
			}
			return style
		})
	_, err := lipgloss.Fprintln(out, sessionsTable.Render())
	return err
}

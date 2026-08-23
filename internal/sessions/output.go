package sessions

import (
	"fmt"
	"io"
	"strconv"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// countColumn is the first zero-based column holding a count rather than a
// label; it and every column after it are right-aligned.
const countColumn = 2

func printJSON(out io.Writer, rows []report.SessionRow) error {
	return reporting.WriteJSONRows(out, rows)
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
	return reporting.RenderTable(out, reporting.Table{
		Headers:    []string{"PERIOD", "PROVIDER", "STARTED", "USED"},
		Rows:       values,
		RightAlign: func(column int) bool { return column >= countColumn },
	})
}

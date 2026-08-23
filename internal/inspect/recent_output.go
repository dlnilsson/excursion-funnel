package inspect

import (
	"io"
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const (
	defaultRecentTableWidth = 140
	maxRecentTableWidth     = 180
)

func printRecentRows(out io.Writer, rows []report.RecentRequestRow) error {
	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, []string{
			reporting.LocalTimestamp(row.StartedAt),
			displayValue(row.Provider),
			displayClient(row.Client),
			displayValue(row.Model),
			requestStatus(row.HTTPStatus.Valid, row.HTTPStatus.Int64),
			strings.TrimSpace(row.Method + " " + reporting.CompactValue(row.Path)),
			row.ID,
		})
	}

	return reporting.RenderTable(out, reporting.Table{
		Headers:   []string{"STARTED", "PROVIDER", "CLIENT", "MODEL", "STATUS", "REQUEST", "ID"},
		Rows:      values,
		BorderRow: true,
		Width:     reporting.TerminalWidth(out, defaultRecentTableWidth, maxRecentTableWidth),
		Wrap:      true,
	})
}

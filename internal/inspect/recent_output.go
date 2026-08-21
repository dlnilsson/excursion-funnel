package inspect

import (
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/term"
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
			strings.TrimSpace(row.Method + " " + compactValue(row.Path)),
			row.ID,
		})
	}

	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	recentTable := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(true).
		Width(recentOutputWidth(out)).
		Wrap(true).
		Headers("STARTED", "PROVIDER", "CLIENT", "MODEL", "STATUS", "REQUEST", "ID").
		Rows(values...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return cellStyle
		})

	_, err := lipgloss.Fprintln(out, recentTable.Render())
	return err
}

func recentOutputWidth(out io.Writer) int {
	width := defaultRecentTableWidth
	if file, ok := out.(terminalDescriptor); ok {
		if terminalWidth, _, err := term.GetSize(file.Fd()); err == nil && terminalWidth > 1 {
			width = terminalWidth - 1
		}
	}
	return min(width, maxRecentTableWidth)
}

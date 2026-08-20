package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/dlnilsson/excursion-funnel/internal/report"
)

type summaryColumn struct {
	header  string
	value   func(report.SummaryRow) string
	numeric bool
}

func printJSON(out io.Writer, rows []report.SummaryRow) error {
	if rows == nil {
		rows = []report.SummaryRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func printRows(out io.Writer, rows []report.SummaryRow, groupBy string) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no usage rows")
		return err
	}

	columns := summaryColumns(groupBy)
	headers := make([]string, len(columns))
	numeric := make([]bool, len(columns))
	values := make([][]string, 0, len(rows))
	for index, column := range columns {
		headers[index] = column.header
		numeric[index] = column.numeric
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for index, column := range columns {
			cells[index] = column.value(row)
		}
		values = append(values, cells)
	}

	cellStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	usageTable := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(false).
		Headers(headers...).
		Rows(values...).
		StyleFunc(func(row, column int) lipgloss.Style {
			style := cellStyle
			if row == table.HeaderRow {
				style = headerStyle
			}
			if numeric[column] {
				style = style.Align(lipgloss.Right)
			}
			return style
		})

	_, err := lipgloss.Fprintln(out, usageTable.Render())
	return err
}

func summaryColumns(groupBy string) []summaryColumn {
	text := func(header string, value func(report.SummaryRow) string) summaryColumn {
		return summaryColumn{header: header, value: value}
	}
	number := func(header string, value func(report.SummaryRow) int64) summaryColumn {
		return summaryColumn{
			header:  header,
			numeric: true,
			value: func(row report.SummaryRow) string {
				return strconv.FormatInt(value(row), 10)
			},
		}
	}

	usage := []summaryColumn{
		number("REQ", func(row report.SummaryRow) int64 { return row.Requests }),
		number("ERR", func(row report.SummaryRow) int64 { return row.Errors }),
		number("INPUT", func(row report.SummaryRow) int64 { return row.FreshInput }),
		number("CACHED", func(row report.SummaryRow) int64 { return row.Cached }),
		number("CACHE_WRITE", func(row report.SummaryRow) int64 { return row.CacheWrite }),
		number("OUTPUT", func(row report.SummaryRow) int64 { return row.Output }),
		number("REASONING", func(row report.SummaryRow) int64 { return row.Reasoning }),
		number("TOTAL", func(row report.SummaryRow) int64 { return row.Total }),
	}

	var dimensions []summaryColumn
	switch groupBy {
	case "day":
		dimensions = []summaryColumn{
			text("DAY", func(row report.SummaryRow) string { return row.Day }),
			text("PROVIDER", func(row report.SummaryRow) string { return row.Provider }),
			text("CLIENT", func(row report.SummaryRow) string { return row.Client }),
			text("MODEL", func(row report.SummaryRow) string { return row.Model }),
		}
	case "provider":
		dimensions = []summaryColumn{text("PROVIDER", func(row report.SummaryRow) string { return row.Provider })}
	case "source":
		dimensions = []summaryColumn{text("SOURCE", func(row report.SummaryRow) string { return row.Source })}
	case "directory":
		dimensions = []summaryColumn{text("DIRECTORY", func(row report.SummaryRow) string { return row.Directory })}
	case "git_branch":
		dimensions = []summaryColumn{text("GIT_BRANCH", func(row report.SummaryRow) string { return row.GitBranch })}
	default:
		dimensions = []summaryColumn{
			text("PROVIDER", func(row report.SummaryRow) string { return row.Provider }),
			text("CLIENT", func(row report.SummaryRow) string { return row.Client }),
			text("MODEL", func(row report.SummaryRow) string { return row.Model }),
		}
	}

	return append(dimensions, usage...)
}

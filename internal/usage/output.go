package usage

import (
	"fmt"
	"io"
	"strconv"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

type summaryColumn struct {
	header      string
	value       func(report.SummaryRow) string
	footerValue func(report.SummaryRow) string
	numeric     bool
}

func printJSON(out io.Writer, rows []report.SummaryRow) error {
	return reporting.WriteJSONRows(out, rows)
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
	values = append(values, summaryTotalRow(columns, rows))

	return reporting.RenderTable(out, reporting.Table{
		Headers:    headers,
		Rows:       values,
		FooterRows: 1,
		RightAlign: func(column int) bool { return numeric[column] },
	})
}

func summaryTotalRow(columns []summaryColumn, rows []report.SummaryRow) []string {
	var total report.SummaryRow
	for _, row := range rows {
		total.Requests += row.Requests
		total.Errors += row.Errors
		total.Input += row.Input
		total.FreshInput += row.FreshInput
		total.Cached += row.Cached
		total.CacheWrite += row.CacheWrite
		total.Output += row.Output
		total.Reasoning += row.Reasoning
		total.Total += row.Total
	}

	cells := make([]string, len(columns))
	for index, column := range columns {
		if index == 0 {
			cells[index] = "TOTAL"
			continue
		}
		if column.footerValue != nil {
			cells[index] = column.footerValue(total)
		}
	}
	return cells
}

func summaryColumns(groupBy string) []summaryColumn {
	text := func(header string, value func(report.SummaryRow) string) summaryColumn {
		return summaryColumn{header: header, value: value}
	}
	number := func(header string, value func(report.SummaryRow) int64) summaryColumn {
		return summaryColumn{
			header:      header,
			footerValue: func(row report.SummaryRow) string { return strconv.FormatInt(value(row), 10) },
			numeric:     true,
			value: func(row report.SummaryRow) string {
				return strconv.FormatInt(value(row), 10)
			},
		}
	}
	tokenCount := func(header string, value func(report.SummaryRow) int64) summaryColumn {
		column := number(header, value)
		column.value = func(row report.SummaryRow) string { return reporting.FormatTokenCount(value(row)) }
		column.footerValue = func(row report.SummaryRow) string { return reporting.FormatTokenCount(value(row)) }
		return column
	}

	usage := []summaryColumn{
		number("REQ", func(row report.SummaryRow) int64 { return row.Requests }),
		number("ERR", func(row report.SummaryRow) int64 { return row.Errors }),
		tokenCount("INPUT", func(row report.SummaryRow) int64 { return row.FreshInput }),
		tokenCount("CACHED", func(row report.SummaryRow) int64 { return row.Cached }),
		tokenCount("CACHE_WRITE", func(row report.SummaryRow) int64 { return row.CacheWrite }),
		tokenCount("OUTPUT", func(row report.SummaryRow) int64 { return row.Output }),
		tokenCount("REASONING", func(row report.SummaryRow) int64 { return row.Reasoning }),
		tokenCount("TOTAL", func(row report.SummaryRow) int64 { return row.Total }),
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

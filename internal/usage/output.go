package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func printJSON(out io.Writer, rows []report.SummaryRow) error {
	if rows == nil {
		rows = []report.SummaryRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func printRows(out io.Writer, rows []report.SummaryRow, groupBy string) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no usage rows")
		return
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	switch groupBy {
	case "day":
		fmt.Fprintln(writer, "DAY\tPROVIDER\tCLIENT\tMODEL\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.Day, row.Provider, row.Client, row.Model, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	case "provider":
		fmt.Fprintln(writer, "PROVIDER\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.Provider, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	case "source":
		fmt.Fprintln(writer, "SOURCE\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.Source, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	case "directory":
		fmt.Fprintln(writer, "DIRECTORY\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.Directory, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	case "git_branch":
		fmt.Fprintln(writer, "GIT_BRANCH\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.GitBranch, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	default:
		fmt.Fprintln(writer, "PROVIDER\tCLIENT\tMODEL\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", row.Provider, row.Client, row.Model, row.Requests, row.Errors, row.FreshInput, row.Cached, row.CacheWrite, row.Output, row.Reasoning, row.Total)
		}
	}
	_ = writer.Flush()
}

// Package usage queries and renders usage summaries.
package usage

import (
	"context"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls a usage summary execution.
type Options struct {
	Connection reporting.Connection
	Since      string
	Until      string
	GroupBy    string
	Directory  string
	Branch     string
	Source     string
	JSON       bool
	Today      bool
}

// Run queries and writes a usage summary.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	shortcut := reporting.ShortcutNone
	if opts.Today {
		shortcut = reporting.ShortcutToday
	}
	window, err := reporting.ResolveRange("usage", shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return err
	}

	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return err
	}
	defer reporter.Close()

	rows, err := reporter.Summary(ctx, report.SummaryOptions{
		Since:     window.Since,
		Until:     window.Until,
		GroupBy:   opts.GroupBy,
		Directory: opts.Directory,
		Branch:    opts.Branch,
		Source:    opts.Source,
	})
	if err != nil {
		return err
	}
	// A single-day window has one obvious date, but only the day grouping
	// selects it. Stamp it on the rows so the JSON output still carries it.
	if opts.GroupBy != "day" && window.IsSingleDay() {
		day := window.Since.Format("2006-01-02")
		for index := range rows {
			rows[index].Day = day
		}
	}
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows, opts.GroupBy)
}

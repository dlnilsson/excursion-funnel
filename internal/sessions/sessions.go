// Package sessions queries and renders provider session lifecycle summaries.
package sessions

import (
	"context"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls a session summary execution.
type Options struct {
	Connection reporting.Connection
	Since      string
	Until      string
	GroupBy    string
	Shortcut   string
	Source     string
	JSON       bool
}

// Load queries the ledger without writing anything.
//
// Loading is separated from rendering so a caller can show a loading animation
// while the query runs and still render styled output straight to the terminal.
// Rendering into a buffer would strip every color, because the color profile is
// detected from the writer.
func Load(ctx context.Context, opts Options) ([]report.SessionRow, error) {
	window, err := reporting.ResolveRange("sessions", opts.Shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return nil, err
	}
	// The period shortcuts also fix the grouping: `sessions week` means weekly
	// buckets, not weekly data in whatever grouping --group-by happened to hold.
	groupBy := opts.GroupBy
	switch opts.Shortcut {
	case reporting.ShortcutToday:
		groupBy = "day"
	case reporting.ShortcutWeek:
		groupBy = "week"
	}

	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return nil, err
	}
	defer reporter.Close()

	return reporter.Sessions(ctx, report.SessionOptions{
		Since: window.Since, Until: window.Until, GroupBy: groupBy, Source: opts.Source,
	})
}

// Render writes loaded session rows in the requested output format.
func Render(out io.Writer, rows []report.SessionRow, opts Options) error {
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows)
}

// Run queries and writes a session lifecycle summary.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	rows, err := Load(ctx, opts)
	if err != nil {
		return err
	}
	return Render(out, rows, opts)
}

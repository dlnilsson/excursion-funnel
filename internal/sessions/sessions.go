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
	JSON       bool
}

// Run queries and writes a session lifecycle summary.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	window, err := reporting.ResolveRange("sessions", opts.Shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return err
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
		return err
	}
	defer reporter.Close()

	rows, err := reporter.Sessions(ctx, report.SessionOptions{
		Since: window.Since, Until: window.Until, GroupBy: groupBy,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows)
}

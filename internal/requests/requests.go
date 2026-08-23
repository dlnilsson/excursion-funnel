// Package requests queries and renders recorded provider web requests.
package requests

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls a web-request report execution.
type Options struct {
	Connection reporting.Connection
	Limit      int
	Since      string
	Until      string
	JSON       bool
	Today      bool
}

// Run queries and writes web requests in the selected date range.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	if opts.Limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	shortcut := reporting.ShortcutNone
	if opts.Today {
		shortcut = reporting.ShortcutToday
	}
	window, err := reporting.ResolveRange("requests", shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return err
	}

	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return err
	}
	defer reporter.Close()
	rows, err := reporter.WebRequests(ctx, report.ToolCallOptions{
		Since: window.Since,
		Until: window.Until,
		Limit: opts.Limit,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows, opts.Today)
}

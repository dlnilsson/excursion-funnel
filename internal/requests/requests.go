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
	Source     string
	JSON       bool
	Today      bool
}

// Load queries the ledger without writing anything.
//
// Loading is separated from rendering so a caller can show a loading animation
// while the query runs and still render styled output straight to the terminal.
// Rendering into a buffer would strip every color, because the color profile is
// detected from the writer.
func Load(ctx context.Context, opts Options) ([]report.WebRequestRow, error) {
	if opts.Limit <= 0 {
		return nil, fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	shortcut := reporting.ShortcutNone
	if opts.Today {
		shortcut = reporting.ShortcutToday
	}
	window, err := reporting.ResolveRange("requests", shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return nil, err
	}

	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return nil, err
	}
	defer reporter.Close()
	return reporter.WebRequests(ctx, report.ToolCallOptions{
		Since:  window.Since,
		Until:  window.Until,
		Limit:  opts.Limit,
		Source: opts.Source,
	})
}

// Render writes loaded web requests in the requested output format.
func Render(out io.Writer, rows []report.WebRequestRow, opts Options) error {
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows, opts.Today)
}

// Run queries and writes web requests in the selected date range.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	rows, err := Load(ctx, opts)
	if err != nil {
		return err
	}
	return Render(out, rows, opts)
}

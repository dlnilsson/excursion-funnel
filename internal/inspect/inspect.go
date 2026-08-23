// Package inspect queries and renders request details.
package inspect

import (
	"context"
	"fmt"
	"io"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/termio"
)

// Options controls an inspect execution.
type Options struct {
	Connection reporting.Connection
	Limit      int
}

// Run opens a recent-request picker when id is empty, otherwise it queries and
// writes requests matching id.
func Run(ctx context.Context, in io.Reader, out io.Writer, id string, opts Options) error {
	if opts.Limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return err
	}
	defer reporter.Close()
	if id == "" {
		rows, err := reporter.RecentRequests(ctx, report.DefaultRecentRequestLimit)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			_, err = fmt.Fprintln(out, "no requests recorded")
			return err
		}
		if !shouldUseRequestList(termio.IsTerminal(in), termio.IsTerminal(out)) {
			return printRecentRows(out, rows)
		}
		id, err = runRequestList(ctx, in, out, rows)
		if err != nil || id == "" {
			return err
		}
	}
	return printInspect(ctx, out, reporter, id, opts.Limit)
}

func printInspect(ctx context.Context, out io.Writer, reporter *report.Reporter, id string, limit int) error {
	rows, err := reporter.Inspect(ctx, id, limit+1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no matching requests")
		return nil
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	printRows(out, rows)
	if truncated {
		fmt.Fprintf(out, "\nshowing the %d most recent matches; pass --limit to see more\n", limit)
	}
	return nil
}

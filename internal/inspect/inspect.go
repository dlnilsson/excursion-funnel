// Package inspect queries and renders request details.
package inspect

import (
	"context"
	"fmt"
	"io"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls an inspect execution.
type Options struct {
	Connection reporting.Connection
	Limit      int
}

// Run queries and writes requests matching id.
func Run(ctx context.Context, out io.Writer, id string, opts Options) error {
	if opts.Limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	reporter, err := report.OpenWithHubKey(opts.Connection.DBPath, opts.Connection.DefaultDBPath, opts.Connection.HubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer reporter.Close()
	rows, err := reporter.Inspect(ctx, id, opts.Limit+1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no matching requests")
		return nil
	}
	truncated := len(rows) > opts.Limit
	if truncated {
		rows = rows[:opts.Limit]
	}
	printRows(out, rows)
	if truncated {
		fmt.Fprintf(out, "\nshowing the %d most recent matches; pass --limit to see more\n", opts.Limit)
	}
	return nil
}

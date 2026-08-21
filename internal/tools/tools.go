// Package tools queries and renders recorded tool calls.
package tools

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls a tool-call report execution.
type Options struct {
	Connection reporting.Connection
	Limit      int
	JSON       bool
	Verbose    bool
}

// Run queries and writes today's tool calls.
func Run(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	if opts.Limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	since := reporting.BeginningOfDay(time.Now())
	reporter, err := report.OpenWithHubKey(opts.Connection.DBPath, opts.Connection.DefaultDBPath, opts.Connection.HubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer reporter.Close()
	rows, err := reporter.ToolCalls(ctx, report.ToolCallOptions{
		Since:        since,
		Until:        since.AddDate(0, 0, 1),
		Limit:        opts.Limit,
		CommandsOnly: !opts.JSON,
	})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out, rows)
	}
	if len(rows) == 0 {
		return printEmpty(out)
	}
	if shouldUseCommandList(opts.JSON, isTerminalReader(in), isTerminalWriter(out)) {
		return runCommandList(ctx, in, out, rows, opts.Verbose)
	}
	return printRows(out, rows, opts.Verbose)
}

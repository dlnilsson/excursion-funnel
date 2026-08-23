// Package tools queries and renders recorded tool calls.
package tools

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/dlnilsson/excursion-funnel/internal/termio"
)

// Options controls a tool-call report execution.
type Options struct {
	Connection reporting.Connection
	Limit      int
	JSON       bool
	Verbose    bool
}

// Load queries today's tool calls.
func Load(ctx context.Context, opts Options) ([]report.ToolCallRow, error) {
	if opts.Limit <= 0 {
		return nil, fmt.Errorf("--limit must be positive, got %d", opts.Limit)
	}
	since := reporting.BeginningOfDay(time.Now())
	reporter, err := report.OpenWithHubKey(opts.Connection.DBPath, opts.Connection.DefaultDBPath, opts.Connection.HubKey)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer reporter.Close()
	rows, err := reporter.ToolCalls(ctx, report.ToolCallOptions{
		Since:        since,
		Until:        since.AddDate(0, 0, 1),
		Limit:        opts.Limit,
		CommandsOnly: !opts.JSON,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// Render writes loaded tool calls in the requested output format.
func Render(ctx context.Context, in io.Reader, out io.Writer, rows []report.ToolCallRow, opts Options) error {
	if opts.JSON {
		return printJSON(out, rows)
	}
	if len(rows) == 0 {
		return printEmpty(out)
	}
	if shouldUseCommandList(opts.JSON, termio.IsTerminal(in), termio.IsTerminal(out)) {
		return runCommandList(ctx, in, out, rows, opts.Verbose)
	}
	return printRows(out, rows, opts.Verbose)
}

// Run queries and writes today's tool calls.
func Run(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	rows, err := Load(ctx, opts)
	if err != nil {
		return err
	}
	return Render(ctx, in, out, rows, opts)
}

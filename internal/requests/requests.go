// Package requests queries and renders recorded provider web requests.
package requests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// ErrTodayWithRange prevents an explicit range from being silently ignored.
var ErrTodayWithRange = errors.New("`requests today` cannot be combined with --since/--until; drop `today` to use an explicit range")

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
	if opts.Today && (opts.Since != "" || opts.Until != "") {
		return ErrTodayWithRange
	}

	var since, until time.Time
	if opts.Today {
		since = reporting.BeginningOfDay(time.Now())
		until = since.AddDate(0, 0, 1)
	} else {
		var err error
		if opts.Since != "" {
			since, err = parseDate(opts.Since)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
		}
		if opts.Until != "" {
			until, err = parseDate(opts.Until)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			until = until.AddDate(0, 0, 1)
		}
	}

	reporter, err := report.OpenWithHubKey(opts.Connection.DBPath, opts.Connection.DefaultDBPath, opts.Connection.HubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer reporter.Close()
	rows, err := reporter.WebRequests(ctx, report.ToolCallOptions{
		Since: since,
		Until: until,
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

func parseDate(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return parsed, nil
}

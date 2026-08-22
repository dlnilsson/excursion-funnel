// Package sessions queries and renders provider session lifecycle summaries.
package sessions

import (
	"context"
	"fmt"
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
	var (
		since   time.Time
		until   time.Time
		groupBy = opts.GroupBy
	)
	switch opts.Shortcut {
	case "today":
		since = reporting.BeginningOfDay(time.Now())
		until = since.AddDate(0, 0, 1)
		groupBy = "day"
	case "week":
		since = reporting.BeginningOfWeek(time.Now())
		until = since.AddDate(0, 0, 7)
		groupBy = "week"
	case "":
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
	default:
		return fmt.Errorf("unsupported session shortcut %q", opts.Shortcut)
	}

	reporter, err := report.OpenWithHubKey(opts.Connection.DBPath, opts.Connection.DefaultDBPath, opts.Connection.HubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer reporter.Close()

	rows, err := reporter.Sessions(ctx, report.SessionOptions{Since: since, Until: until, GroupBy: groupBy})
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(out, rows)
	}
	return printRows(out, rows)
}

func parseDate(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return parsed, nil
}

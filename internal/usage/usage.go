// Package usage queries and renders usage summaries.
package usage

import (
	"context"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Options controls a usage summary execution.
type Options struct {
	Connection reporting.Connection
	Since      string
	Until      string
	GroupBy    string
	Directory  string
	Branch     string
	Source     string
	JSON       bool
	Today      bool
	Heatmap    bool
	// Width is the terminal columns available to the heatmap. It is resolved by
	// the command layer from the real output stream, because rendering may be
	// handed a buffer that has no terminal to measure.
	Width int
}

// Data is the result of a usage query, ready to render.
type Data struct {
	Rows     []report.SummaryRow
	Activity []report.TokenActivityRow
	// Window is the resolved reporting window the activity covers.
	Window reporting.Range
	// Hourly reports whether Activity holds hours rather than days.
	Hourly bool
	// LongestSession is zero unless HasSession is true.
	LongestSession time.Duration
	HasSession     bool
}

// Load queries the ledger without writing anything.
//
// Loading is separated from rendering so a caller can show a loading animation
// while the query runs and still render styled output straight to the terminal.
// Rendering into a buffer would strip every color, because the color profile is
// detected from the writer.
func Load(ctx context.Context, opts Options) (Data, error) {
	shortcut := reporting.ShortcutNone
	if opts.Today {
		shortcut = reporting.ShortcutToday
	}
	window, err := reporting.ResolveRange("usage", shortcut, opts.Since, opts.Until, time.Now())
	if err != nil {
		return Data{}, err
	}

	reporter, err := report.OpenConnection(opts.Connection)
	if err != nil {
		return Data{}, err
	}
	defer reporter.Close()

	if opts.Heatmap {
		return loadHeatmap(ctx, reporter, window, opts)
	}

	rows, err := reporter.Summary(ctx, report.SummaryOptions{
		Since:     window.Since,
		Until:     window.Until,
		GroupBy:   opts.GroupBy,
		Directory: opts.Directory,
		Branch:    opts.Branch,
		Source:    opts.Source,
	})
	if err != nil {
		return Data{}, err
	}
	// A single-day window has one obvious date, but only the day grouping
	// selects it. Stamp it on the rows so the JSON output still carries it.
	if opts.GroupBy != "day" && window.IsSingleDay() {
		day := window.Since.Format("2006-01-02")
		for index := range rows {
			rows[index].Day = day
		}
	}
	return Data{Rows: rows, Window: window}, nil
}

// Render writes loaded usage data in the requested output format.
func Render(out io.Writer, data Data, opts Options) error {
	if opts.Heatmap {
		if opts.JSON {
			return reporting.WriteJSONRows(out, data.Activity)
		}
		return renderHeatmap(out, data, opts)
	}
	if opts.JSON {
		return printJSON(out, data.Rows)
	}
	return printRows(out, data.Rows, opts.GroupBy)
}

// Run queries and writes a usage summary.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	data, err := Load(ctx, opts)
	if err != nil {
		return err
	}
	return Render(out, data, opts)
}

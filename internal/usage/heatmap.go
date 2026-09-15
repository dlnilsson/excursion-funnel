package usage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/heatmap"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// loadHeatmap fetches the buckets and the session stat the calendar shows.
func loadHeatmap(ctx context.Context, reporter *report.Reporter, window reporting.Range, opts Options) (Data, error) {
	var (
		now    = time.Now()
		hourly = window.IsSingleDay()
		bucket = "day"
	)
	if hourly {
		bucket = "hour"
	}
	window = heatmapWindow(window, now)

	activityOpts := report.TokenActivityOptions{
		Since:     window.Since,
		Until:     window.Until,
		Bucket:    bucket,
		Directory: opts.Directory,
		Branch:    opts.Branch,
		Source:    opts.Source,
	}
	activity, err := reporter.TokenActivity(ctx, activityOpts)
	if err != nil {
		return Data{}, err
	}
	longest, hasSession, err := reporter.LongestSession(ctx, activityOpts)
	if err != nil {
		return Data{}, err
	}
	return Data{
		Activity:       activity,
		Window:         window,
		Hourly:         hourly,
		LongestSession: longest,
		HasSession:     hasSession,
	}, nil
}

// heatmapWindow fills an unbounded side of the window with the calendar
// default, leaving an explicit --since/--until untouched.
func heatmapWindow(window reporting.Range, now time.Time) reporting.Range {
	since, until := heatmap.Window(now, heatmap.Weeks)
	if window.Since.IsZero() {
		window.Since = since
	}
	if window.Until.IsZero() {
		window.Until = until
	}
	return window
}

// renderHeatmap draws the loaded buckets as a calendar, or as a single strip
// when the window covers one day.
func renderHeatmap(out io.Writer, data Data, opts Options) error {
	buckets := make([]heatmap.Bucket, 0, len(data.Activity))
	for _, row := range data.Activity {
		buckets = append(buckets, heatmap.Bucket{At: row.At, Total: row.Total})
	}

	options := heatmap.Options{
		Title: "Token activity",
		Unit:  "tokens",
		Now:   time.Now(),
		Width: opts.Width,
	}
	if data.HasSession {
		options.Extra = []string{fmt.Sprintf("Longest session %s", reporting.FormatDuration(data.LongestSession))}
	}
	if data.Hourly {
		return heatmap.RenderStrip(out, buckets, options)
	}
	return heatmap.RenderCalendar(out, buckets, options)
}

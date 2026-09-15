// Package heatmap renders GitHub-style activity calendars for the terminal.
//
// It deliberately knows nothing about tokens, requests or the usage ledger: a
// caller hands it buckets of an int64 count and supplies the unit noun, so any
// report with an "activity over time" shape can reuse the same grid.
package heatmap

import (
	"math"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

// Weeks is the calendar's span in columns, matching the web dashboard's
// heatmapWeeks constant so both views cover the same window.
const Weeks = 53

const (
	gutter    = 3 // two-letter weekday label plus one space
	cellWidth = 2 // one glyph plus one space

	// FullWidth is the number of columns an untruncated calendar needs, so
	// callers can size a terminal query without knowing the layout.
	FullWidth = gutter + Weeks*cellWidth - 1
)

// Bucket is one time bucket of activity. At is the bucket's start in local
// time; Total is the count being shaded.
type Bucket struct {
	At    time.Time
	Total int64
}

// Options controls one rendered calendar or strip.
type Options struct {
	// Title heads the output, for example "Token activity".
	Title string
	// Unit names what is counted in the stats line and the empty state, for
	// example "tokens".
	Unit string
	// Now is injected rather than read from the clock so renders are
	// deterministic, as reporting.ResolveRange does.
	Now time.Time
	// Width is the columns available. Zero renders the full grid.
	Width int
	// Extra holds caller-supplied stat chips appended to the stats line, for
	// example "Longest session 5h 6m". It exists so domain facts the renderer
	// cannot derive from buckets stay out of this package.
	Extra []string
}

// Stats are the figures derivable from the buckets alone.
type Stats struct {
	Total       int64
	ActiveCount int
	Peak        Bucket
	// StreakDays is the run of active days ending now; LongestStreakDays is the
	// longest run anywhere in the buckets. Both are zero for hour buckets.
	StreakDays        int
	LongestStreakDays int
}

// levels holds the shading ramp. Glyph and color both carry the level so the
// grid stays readable under NO_COLOR and when piped to a file.
var levels = [5]struct {
	glyph string
	color ansi.BasicColor
}{
	{"·", lipgloss.BrightBlack},
	{"░", lipgloss.Blue},
	{"▒", lipgloss.Cyan},
	{"▓", lipgloss.BrightCyan},
	{"█", lipgloss.White},
}

// Window returns the default calendar range ending on now's day: since is the
// Monday that starts the earliest column, until is exclusive.
func Window(now time.Time, weeks int) (since, until time.Time) {
	if weeks < 1 {
		weeks = 1
	}
	since = reporting.BeginningOfWeek(now).AddDate(0, 0, -(weeks-1)*7)
	until = reporting.BeginningOfDay(now).AddDate(0, 0, 1)
	return since, until
}

// Level buckets a total into 0-4. It is a direct port of the dashboard's
// heatmapLevel so the CLI and the web view never shade the same day
// differently.
func Level(total, maximum int64) int {
	if total <= 0 || maximum <= 0 {
		return 0
	}
	level := int(math.Ceil(4 * math.Log1p(float64(total)) / math.Log1p(float64(maximum))))
	return min(4, max(1, level))
}

// Summarize computes the stats line figures from buckets. StreakDays is only
// meaningful for day buckets and stays zero when buckets are finer grained.
func Summarize(buckets []Bucket, now time.Time) Stats {
	var stats Stats
	for _, bucket := range buckets {
		if bucket.Total <= 0 {
			continue
		}
		stats.Total += bucket.Total
		stats.ActiveCount++
		if bucket.Total > stats.Peak.Total {
			stats.Peak = bucket
		}
	}
	stats.StreakDays, stats.LongestStreakDays = streaks(buckets, now)
	return stats
}

// streaks returns the run of consecutive active days ending today — or ending
// yesterday when today has no activity yet, so checking early in the morning
// does not report a broken streak — and the longest run anywhere in the
// buckets. Both are zero unless the buckets are whole days.
func streaks(buckets []Bucket, now time.Time) (current, longest int) {
	if len(buckets) == 0 {
		return 0, 0
	}
	active := make(map[string]bool, len(buckets))
	for _, bucket := range buckets {
		if !bucket.At.Equal(reporting.BeginningOfDay(bucket.At)) {
			return 0, 0
		}
		if bucket.Total > 0 {
			active[dayKey(bucket.At)] = true
		}
	}

	// Walk the calendar rather than the slice so a caller that passes gappy
	// buckets cannot join two runs that are not actually adjacent.
	run := 0
	last := reporting.BeginningOfDay(buckets[len(buckets)-1].At)
	for day := reporting.BeginningOfDay(buckets[0].At); !day.After(last); day = day.AddDate(0, 0, 1) {
		if !active[dayKey(day)] {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}

	day := reporting.BeginningOfDay(now)
	if !active[dayKey(day)] {
		day = day.AddDate(0, 0, -1)
	}
	for active[dayKey(day)] {
		current++
		day = day.AddDate(0, 0, -1)
	}
	return current, longest
}

func dayKey(value time.Time) string { return value.Format("2006-01-02") }

// maximum returns the largest bucket total, which sets the top of the ramp.
func maximum(buckets []Bucket) int64 {
	var highest int64
	for _, bucket := range buckets {
		highest = max(highest, bucket.Total)
	}
	return highest
}

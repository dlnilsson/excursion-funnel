package heatmap

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

var renderNow = time.Date(2026, 9, 16, 9, 0, 0, 0, time.Local)

func calendarOptions() Options {
	return Options{Title: "Token activity", Unit: "tokens", Now: renderNow}
}

// calendarBuckets fills the default window, giving every day the same total so
// the grid shape is what the assertions are about.
func calendarBuckets(total int64) []Bucket {
	since, until := Window(renderNow, Weeks)
	var buckets []Bucket
	for at := since; at.Before(until); at = at.AddDate(0, 0, 1) {
		buckets = append(buckets, Bucket{At: at, Total: total})
	}
	return buckets
}

func render(t *testing.T, buckets []Bucket, opts Options, strip bool) string {
	t.Helper()
	var output bytes.Buffer
	var err error
	if strip {
		err = RenderStrip(&output, buckets, opts)
	} else {
		err = RenderCalendar(&output, buckets, opts)
	}
	if err != nil {
		t.Fatalf("render error = %v", err)
	}
	return output.String()
}

func TestRenderCalendarWritesNoANSIWhenRedirected(t *testing.T) {
	t.Parallel()
	text := render(t, calendarBuckets(1_000), calendarOptions(), false)
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("redirected output contains ANSI escapes: %q", text)
	}
}

func TestRenderCalendarGridShape(t *testing.T) {
	t.Parallel()
	text := render(t, calendarBuckets(1_000), calendarOptions(), false)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	// title, stats, blank, months, 7 weekdays, blank, legend
	if len(lines) != 13 {
		t.Fatalf("rendered %d lines, want 13: %q", len(lines), text)
	}
	if !strings.HasPrefix(lines[0], "Token activity") || !strings.Contains(lines[0], "last 12 months") {
		t.Fatalf("title line = %q", lines[0])
	}
	for row, label := range weekdayLabels {
		line := lines[4+row]
		if !strings.HasPrefix(line, label) {
			t.Fatalf("row %d = %q, want it to start with %q", row, line, label)
		}
		if width := lipgloss.Width(line); width > FullWidth {
			t.Fatalf("row %d is %d columns wide, want at most %d", row, width, FullWidth)
		}
	}
	if !strings.HasPrefix(lines[12], "Less") || !strings.HasSuffix(lines[12], "More") {
		t.Fatalf("legend line = %q", lines[12])
	}
}

func TestRenderCalendarAlignsMonthLabelsOverTheirColumn(t *testing.T) {
	t.Parallel()
	text := render(t, calendarBuckets(1_000), calendarOptions(), false)
	lines := strings.Split(text, "\n")
	months, firstRow := lines[3], lines[4]

	// The first grid column is the Monday of the window, so its month label
	// must sit exactly above that column's cell.
	since, _ := Window(renderNow, Weeks)
	label := since.Format("Jan")
	if got := strings.Index(months, label); got != gutter {
		t.Fatalf("first month label %q at column %d, want %d (months=%q)", label, got, gutter, months)
	}
	if len(firstRow) < gutter {
		t.Fatalf("first weekday row too short: %q", firstRow)
	}
}

func TestRenderCalendarTruncatesToTheMostRecentWeeks(t *testing.T) {
	t.Parallel()
	opts := calendarOptions()
	opts.Width = 40
	text := render(t, calendarBuckets(1_000), opts, false)

	if !strings.Contains(text, "last 19 weeks") {
		t.Fatalf("narrow render should name its shortened range, got %q", text)
	}
	if strings.Contains(text, "last 12 months") {
		t.Fatal("narrow render still claims a full year")
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if width := lipgloss.Width(line); width > opts.Width {
			t.Fatalf("line %q is %d columns wide, want at most %d", line, width, opts.Width)
		}
	}
}

func TestRenderCalendarLeavesFutureDaysBlank(t *testing.T) {
	t.Parallel()
	text := render(t, calendarBuckets(1_000), calendarOptions(), false)
	lines := strings.Split(text, "\n")

	// renderNow is a Wednesday, so Thursday through Sunday of the final column
	// are in the future and must not be shaded.
	wednesday, sunday := lines[4+2], lines[4+6]
	if lipgloss.Width(sunday) >= lipgloss.Width(wednesday) {
		t.Fatalf("Sunday row (%d cols) should be shorter than Wednesday (%d cols): %q",
			lipgloss.Width(sunday), lipgloss.Width(wednesday), text)
	}
}

func TestRenderCalendarShowsStatsAndExtraChips(t *testing.T) {
	t.Parallel()
	opts := calendarOptions()
	opts.Extra = []string{"Longest session 5h 6m"}
	buckets := []Bucket{
		{At: time.Date(2026, 9, 14, 0, 0, 0, 0, time.Local), Total: 1_000},
		{At: time.Date(2026, 9, 15, 0, 0, 0, 0, time.Local), Total: 2_000_000},
		{At: time.Date(2026, 9, 16, 0, 0, 0, 0, time.Local), Total: 500},
	}
	text := render(t, buckets, opts, false)

	for _, want := range []string{
		"2.0M tokens across 3 active days",
		"Peak 2.0M on 2026-09-15",
		"Streak 3d",
		"Longest streak 3d",
		"Longest session 5h 6m",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stats line missing %q: %q", want, text)
		}
	}
}

func TestRenderCalendarEmptyStates(t *testing.T) {
	t.Parallel()
	want := "no token activity, last 12 months\n"
	if got := render(t, nil, calendarOptions(), false); got != want {
		t.Errorf("no buckets = %q, want %q", got, want)
	}
	if got := render(t, calendarBuckets(0), calendarOptions(), false); got != want {
		t.Errorf("all-zero buckets = %q, want %q", got, want)
	}
}

func hourBuckets(total int64) []Bucket {
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.Local)
	buckets := make([]Bucket, 0, 24)
	for hour := range 24 {
		buckets = append(buckets, Bucket{At: start.Add(time.Duration(hour) * time.Hour), Total: total})
	}
	return buckets
}

func TestRenderStripLabelsEverySecondHour(t *testing.T) {
	t.Parallel()
	text := render(t, hourBuckets(1_000), calendarOptions(), true)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	// title, stats, blank, hour labels, cells, blank, legend
	if len(lines) != 7 {
		t.Fatalf("rendered %d lines, want 7: %q", len(lines), text)
	}
	if !strings.Contains(lines[0], "today") {
		t.Fatalf("title line = %q, want it to name today", lines[0])
	}
	labels, cells := lines[3], lines[4]
	for hour := 0; hour < 24; hour += 2 {
		label := time.Date(2026, 9, 16, hour, 0, 0, 0, time.Local).Format("15")
		column := gutter + hour*cellWidth
		if !strings.HasPrefix(labels[column:], label) {
			t.Fatalf("hour %s not at column %d: %q", label, column, labels)
		}
	}
	if got := strings.Count(cells, "█"); got != 24 {
		t.Fatalf("strip drew %d cells, want 24: %q", got, cells)
	}
	if strings.Contains(text, "Streak") {
		t.Fatalf("a single day has no day streak: %q", text)
	}
}

func TestRenderStripEmptyState(t *testing.T) {
	t.Parallel()
	if got := render(t, hourBuckets(0), calendarOptions(), true); got != "no token activity, today\n" {
		t.Errorf("empty strip = %q", got)
	}
}

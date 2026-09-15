package heatmap

import (
	"testing"
	"time"
)

// day builds a bucket at local midnight, the shape RenderCalendar expects.
func day(t *testing.T, year int, month time.Month, dayOfMonth int, total int64) Bucket {
	t.Helper()
	return Bucket{At: time.Date(year, month, dayOfMonth, 0, 0, 0, 0, time.Local), Total: total}
}

// TestLevelPortsTheDashboardFormula pins Level against values produced by the
// dashboard's own heatmapLevel (internal/ui/assets/index.html), so the terminal
// and the web view never shade the same day differently.
func TestLevelPortsTheDashboardFormula(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		total, maximum int64
		want           int
	}{
		{"no activity", 0, 100, 0},
		{"negative total", -5, 100, 0},
		{"no maximum", 10, 0, 0},
		{"single token floors at one", 1, 1_000_000, 1},
		{"total equals maximum tops out", 100, 100, 4},
		{"above maximum still clamps", 500, 100, 4},
		{"low", 10, 1_000_000, 1},
		{"low midrange", 31, 1_000_000, 2},
		{"high midrange", 1_000, 1_000_000, 3},
		{"just below maximum", 999_999, 1_000_000, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Level(tt.total, tt.maximum); got != tt.want {
				t.Errorf("Level(%d, %d) = %d, want %d", tt.total, tt.maximum, got, tt.want)
			}
		})
	}
}

func TestWindowStartsOnMondayAndEndsExclusive(t *testing.T) {
	t.Parallel()
	// 2026-09-16 is a Wednesday.
	now := time.Date(2026, 9, 16, 14, 30, 0, 0, time.Local)
	since, until := Window(now, Weeks)

	if since.Weekday() != time.Monday {
		t.Fatalf("since = %s, want a Monday", since)
	}
	if got := until.Sub(since); got < 52*7*24*time.Hour {
		t.Fatalf("window spans %s, want about %d weeks", got, Weeks)
	}
	wantUntil := time.Date(2026, 9, 17, 0, 0, 0, 0, time.Local)
	if !until.Equal(wantUntil) {
		t.Fatalf("until = %s, want %s (exclusive end of now's day)", until, wantUntil)
	}
}

func TestSummarizeCountsActiveBucketsAndPeak(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.Local)
	buckets := []Bucket{
		day(t, 2026, 9, 14, 10),
		day(t, 2026, 9, 15, 0),
		day(t, 2026, 9, 16, 30),
	}

	stats := Summarize(buckets, now)
	if stats.Total != 40 {
		t.Errorf("Total = %d, want 40", stats.Total)
	}
	if stats.ActiveCount != 2 {
		t.Errorf("ActiveCount = %d, want 2", stats.ActiveCount)
	}
	if stats.Peak.Total != 30 || stats.Peak.At.Day() != 16 {
		t.Errorf("Peak = %+v, want 30 on the 16th", stats.Peak)
	}
}

func TestSummarizeStreak(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.Local)
	tests := []struct {
		name    string
		buckets []Bucket
		want    int
	}{
		{
			name:    "run ending today",
			buckets: []Bucket{day(t, 2026, 9, 14, 1), day(t, 2026, 9, 15, 1), day(t, 2026, 9, 16, 1)},
			want:    3,
		},
		{
			// Checking early in the morning must not report a broken streak.
			name:    "run ending yesterday",
			buckets: []Bucket{day(t, 2026, 9, 14, 1), day(t, 2026, 9, 15, 1), day(t, 2026, 9, 16, 0)},
			want:    2,
		},
		{
			name:    "two day gap ends the run",
			buckets: []Bucket{day(t, 2026, 9, 13, 1), day(t, 2026, 9, 14, 0), day(t, 2026, 9, 15, 0), day(t, 2026, 9, 16, 0)},
			want:    0,
		},
		{
			name:    "no activity at all",
			buckets: []Bucket{day(t, 2026, 9, 15, 0), day(t, 2026, 9, 16, 0)},
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Summarize(tt.buckets, now).StreakDays; got != tt.want {
				t.Errorf("StreakDays = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSummarizeLeavesStreakZeroForHourBuckets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.Local)
	start := time.Date(2026, 9, 16, 0, 0, 0, 0, time.Local)
	buckets := make([]Bucket, 0, 24)
	for hour := range 24 {
		buckets = append(buckets, Bucket{At: start.Add(time.Duration(hour) * time.Hour), Total: 5})
	}

	if got := Summarize(buckets, now).StreakDays; got != 0 {
		t.Fatalf("StreakDays = %d for hourly buckets, want 0", got)
	}
}

func TestAvailableWeeks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		width int
		want  int
	}{
		{"no terminal detected renders the full grid", 0, Weeks},
		{"negative width renders the full grid", -1, Weeks},
		{"full width fits every column", FullWidth, Weeks},
		{"wider than needed still caps at the span", FullWidth + 40, Weeks},
		{"narrow terminal truncates", 40, 19},
		{"tiny terminal keeps one column", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := availableWeeks(tt.width); got != tt.want {
				t.Errorf("availableWeeks(%d) = %d, want %d", tt.width, got, tt.want)
			}
		})
	}
}

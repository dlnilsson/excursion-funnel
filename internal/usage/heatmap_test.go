package usage

import (
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/heatmap"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

func TestHeatmapWindowFillsUnboundedSidesOnly(t *testing.T) {
	t.Parallel()
	var (
		now           = time.Date(2026, 9, 16, 14, 0, 0, 0, time.Local)
		since, until  = heatmap.Window(now, heatmap.Weeks)
		explicitSince = time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
		explicitUntil = time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)
	)
	tests := []struct {
		name      string
		in        reporting.Range
		wantSince time.Time
		wantUntil time.Time
	}{
		{"unbounded defaults to the calendar window", reporting.Range{}, since, until},
		{"explicit since is kept", reporting.Range{Since: explicitSince}, explicitSince, until},
		{"explicit until is kept", reporting.Range{Until: explicitUntil}, since, explicitUntil},
		{
			"explicit range is untouched",
			reporting.Range{Since: explicitSince, Until: explicitUntil},
			explicitSince, explicitUntil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := heatmapWindow(tt.in, now)
			if !got.Since.Equal(tt.wantSince) {
				t.Errorf("Since = %s, want %s", got.Since, tt.wantSince)
			}
			if !got.Until.Equal(tt.wantUntil) {
				t.Errorf("Until = %s, want %s", got.Until, tt.wantUntil)
			}
		})
	}
}

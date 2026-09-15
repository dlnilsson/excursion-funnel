package reporting

import (
	"testing"
	"time"
)

func TestLocalTimestampConvertsToLocalZone(t *testing.T) {
	parsed, err := time.Parse(time.RFC3339Nano, "2026-08-02T19:53:49.514Z")
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.Local().Format("2006-01-02T15:04:05.000Z07:00")
	if got := LocalTimestamp(parsed); got != want {
		t.Fatalf("LocalTimestamp() = %q, want %q", got, want)
	}
}

func TestBeginningOfWeekUsesMondayInInputLocation(t *testing.T) {
	location := time.FixedZone("test", 2*60*60)
	for _, input := range []time.Time{
		time.Date(2026, 8, 3, 15, 0, 0, 0, location),
		time.Date(2026, 8, 9, 23, 59, 0, 0, location),
	} {
		want := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
		if got := BeginningOfWeek(input); !got.Equal(want) || got.Location() != location {
			t.Fatalf("BeginningOfWeek(%v) = %v, want %v", input, got, want)
		}
	}
}

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value int64
		want  string
	}{
		{"zero", 0, "0"},
		{"small positive", 123, "123"},
		{"small negative", -456, "-456"},
		{"boundary 999", 999, "999"},
		{"boundary -999", -999, "-999"},
		{"one thousand", 1_000, "1.0k"},
		{"negative one thousand", -1_000, "-1.0k"},
		{"mid thousands", 12_500, "12.5k"},
		{"negative mid thousands", -12_500, "-12.5k"},
		{"near million", 999_999, "1000.0k"},
		{"one million", 1_000_000, "1.0M"},
		{"negative one million", -1_000_000, "-1.0M"},
		{"mid millions", 123_456_789, "123.5M"},
		{"negative mid millions", -123_456_789, "-123.5M"},
		{"near billion", 999_999_999, "1000.0M"},
		{"one billion", 1_000_000_000, "1.0B"},
		{"negative one billion", -1_000_000_000, "-1.0B"},
		{"large value", 12_345_678_901, "12.3B"},
		{"large negative", -12_345_678_901, "-12.3B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatTokenCount(tt.value); got != tt.want {
				t.Errorf("FormatTokenCount(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value time.Duration
		want  string
	}{
		{"zero", 0, "0s"},
		{"sub second", 999 * time.Millisecond, "0s"},
		{"negative", -time.Hour, "0s"},
		{"seconds", 12 * time.Second, "12s"},
		{"boundary 59s", 59 * time.Second, "59s"},
		{"one minute", time.Minute, "1m"},
		{"minutes", 47 * time.Minute, "47m"},
		{"boundary 59m", 59*time.Minute + 59*time.Second, "59m"},
		{"whole hour", time.Hour, "1h"},
		{"hours and minutes", 5*time.Hour + 6*time.Minute, "5h 6m"},
		{"hours drop seconds", 5*time.Hour + 6*time.Minute + 30*time.Second, "5h 6m"},
		{"many hours", 49*time.Hour + 1*time.Minute, "49h 1m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatDuration(tt.value); got != tt.want {
				t.Errorf("FormatDuration(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

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

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

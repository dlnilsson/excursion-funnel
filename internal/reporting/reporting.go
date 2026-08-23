// Package reporting contains behavior shared by read-only report applications.
package reporting

import (
	"fmt"
	"strings"
	"time"
)

// Connection identifies the selected report ledger and optional hub key.
type Connection struct {
	DBPath        string
	DefaultDBPath string
	HubKey        string
}

// BeginningOfDay returns midnight in the input time's location.
func BeginningOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

// BeginningOfHour returns the top of the hour in the input time's location.
func BeginningOfHour(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, value.Hour(), 0, 0, 0, value.Location())
}

// BeginningOfWeek returns Monday midnight in the input time's location.
func BeginningOfWeek(value time.Time) time.Time {
	day := BeginningOfDay(value)
	daysSinceMonday := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}

// LocalTimestamp formats a timestamp in the process-local time zone.
func LocalTimestamp(value time.Time) string {
	return value.Local().Format("2006-01-02T15:04:05.000Z07:00")
}

// EmptyAsDash returns a display placeholder for an empty value.
func EmptyAsDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// CompactValue folds line breaks into a visible marker so a multi-line command
// or path stays on one table row instead of breaking the border.
func CompactValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ↩ ")
	value = strings.ReplaceAll(value, "\n", " ↩ ")
	return strings.ReplaceAll(value, "\r", " ↩ ")
}

// ParseDate parses a YYYY-MM-DD command-line date in the local time zone.
func ParseDate(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return parsed, nil
}

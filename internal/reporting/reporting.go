// Package reporting contains behavior shared by read-only report applications.
package reporting

import "time"

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

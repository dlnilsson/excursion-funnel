package reporting

import (
	"errors"
	"fmt"
	"time"
)

// Shortcut names a preset reporting window. The empty shortcut means the caller
// supplied an explicit --since/--until range instead.
const (
	ShortcutNone  = ""
	ShortcutToday = "today"
	ShortcutWeek  = "week"
)

// Range is a half-open reporting window: Since is inclusive, Until exclusive.
// A zero bound is unbounded on that side.
type Range struct {
	Since time.Time
	Until time.Time
}

// ErrShortcutWithRange is the sentinel every shortcut-plus-range rejection
// unwraps to, so callers can test for the condition without matching text.
var ErrShortcutWithRange = errors.New("shortcut subcommand cannot be combined with --since/--until")

// ShortcutRangeError reports a shortcut subcommand combined with an explicit
// range, which would silently ignore one of them. Its message names the
// specific command so the user is told which words to drop.
type ShortcutRangeError struct {
	Command  string
	Shortcut string
}

func (e *ShortcutRangeError) Error() string {
	return fmt.Sprintf("`%s %s` cannot be combined with --since/--until; drop `%s` to use an explicit range",
		e.Command, e.Shortcut, e.Shortcut)
}

func (e *ShortcutRangeError) Unwrap() error { return ErrShortcutWithRange }

// ResolveRange turns a shortcut subcommand or an explicit --since/--until pair
// into a half-open window.
//
// Dates are parsed in the local zone and until is advanced by a day, so the
// user-facing "--until 2026-08-20" (inclusive, as documented on the flag)
// becomes an exclusive bound covering all of that day.
//
// command names the caller for error messages. now is passed in rather than
// read from the clock so callers can test the shortcut windows.
func ResolveRange(command, shortcut, since, until string, now time.Time) (Range, error) {
	if shortcut != ShortcutNone && (since != "" || until != "") {
		return Range{}, &ShortcutRangeError{Command: command, Shortcut: shortcut}
	}

	switch shortcut {
	case ShortcutToday:
		start := BeginningOfDay(now)
		return Range{Since: start, Until: start.AddDate(0, 0, 1)}, nil
	case ShortcutWeek:
		start := BeginningOfWeek(now)
		return Range{Since: start, Until: start.AddDate(0, 0, 7)}, nil
	case ShortcutNone:
	default:
		return Range{}, fmt.Errorf("unsupported %s shortcut %q", command, shortcut)
	}

	var out Range
	if since != "" {
		parsed, err := ParseDate(since)
		if err != nil {
			return Range{}, fmt.Errorf("--since: %w", err)
		}
		out.Since = parsed
	}
	if until != "" {
		parsed, err := ParseDate(until)
		if err != nil {
			return Range{}, fmt.Errorf("--until: %w", err)
		}
		out.Until = parsed.AddDate(0, 0, 1)
	}
	return out, nil
}

// IsSingleDay reports whether the range covers exactly one calendar day, which
// lets a summary stamp its rows with that date even when grouping hides it.
func (r Range) IsSingleDay() bool {
	return !r.Since.IsZero() && r.Until.Equal(r.Since.AddDate(0, 0, 1))
}

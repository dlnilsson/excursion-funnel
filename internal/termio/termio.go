// Package termio reports whether a stream is attached to an interactive
// terminal. Commands use it to decide between an interactive view (a picker, a
// spinner) and plain piped output.
package termio

import "github.com/charmbracelet/x/term"

// descriptor is satisfied by *os.File and by test doubles that expose a file
// descriptor. A stream without one cannot be a terminal.
type descriptor interface {
	Fd() uintptr
}

// IsTerminal reports whether stream is an interactive terminal. It accepts any
// value so a single helper serves both io.Reader and io.Writer callers.
func IsTerminal(stream any) bool {
	fd, ok := stream.(descriptor)
	return ok && term.IsTerminal(fd.Fd())
}

package reporting

import (
	"io"

	"github.com/charmbracelet/x/term"
)

// streamWidth reports the terminal width of out, if it has one.
func streamWidth(out io.Writer) (int, bool) {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}
	width, _, err := term.GetSize(file.Fd())
	if err != nil {
		return 0, false
	}
	return width, true
}

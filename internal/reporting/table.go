package reporting

import (
	"encoding/json"
	"io"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

// Table describes a terminal table in the shared report style: a rounded
// border in bright black, single-space cell padding, and a bold magenta header
// row. Every report command renders through this so the CLI reads as one tool
// rather than five that happen to agree.
type Table struct {
	Headers []string
	Rows    [][]string
	// BorderRow draws a separator between data rows. Wrapping tables need it to
	// keep multi-line cells distinguishable; compact tables read better without.
	BorderRow bool
	// Width constrains the rendered table. Zero lets the content size itself.
	Width int
	// Wrap allows cell contents to flow onto additional lines instead of being
	// truncated. Only meaningful together with Width.
	Wrap bool
	// RightAlign reports whether a zero-based column holds numbers and should be
	// right-aligned. A nil func left-aligns everything.
	RightAlign func(column int) bool
}

// RenderTable writes t to out in the shared report style.
func RenderTable(out io.Writer, t Table) error {
	var (
		cellStyle   = lipgloss.NewStyle().Padding(0, 1)
		headerStyle = cellStyle.Bold(true).Foreground(lipgloss.Magenta)
	)
	rendered := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)).
		BorderRow(t.BorderRow).
		Headers(t.Headers...).
		Rows(t.Rows...).
		StyleFunc(func(row, column int) lipgloss.Style {
			style := cellStyle
			if row == table.HeaderRow {
				style = headerStyle
			}
			if t.RightAlign != nil && t.RightAlign(column) {
				style = style.Align(lipgloss.Right)
			}
			return style
		})
	if t.Width > 0 {
		rendered = rendered.Width(t.Width)
	}
	if t.Wrap {
		rendered = rendered.Wrap(true)
	}
	_, err := lipgloss.Fprintln(out, rendered.Render())
	return err
}

// TerminalWidth returns the width a table should render at: the terminal's own
// width when out is a terminal, otherwise fallback, in both cases capped at
// maximum. The last terminal column is left unused so terminals that wrap
// eagerly at the right edge do not add a blank line after every row.
func TerminalWidth(out io.Writer, fallback, maximum int) int {
	width := fallback
	if terminalWidth, ok := streamWidth(out); ok && terminalWidth > 1 {
		width = terminalWidth - 1
	}
	return min(width, maximum)
}

// WriteJSONRows encodes rows as a JSON array. A nil slice is written as [] so
// consumers never have to distinguish "no rows" from "null".
func WriteJSONRows[T any](out io.Writer, rows []T) error {
	if rows == nil {
		rows = []T{}
	}
	return json.NewEncoder(out).Encode(rows)
}

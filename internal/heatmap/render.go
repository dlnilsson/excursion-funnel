package heatmap

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

// weekdayLabels are the grid's rows, Monday first, matching
// reporting.BeginningOfWeek and the dashboard's heatmapWeekStart.
var weekdayLabels = [7]string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}

// RenderCalendar writes a weeks-by-days calendar grid of daily buckets. The
// grid always starts on a Monday, so buckets whose first day falls mid-week are
// padded with blanks.
func RenderCalendar(out io.Writer, buckets []Bucket, opts Options) error {
	if len(buckets) == 0 {
		return writeEmpty(out, opts, "last 12 months")
	}

	var (
		today      = reporting.BeginningOfDay(opts.Now)
		gridStart  = reporting.BeginningOfWeek(buckets[0].At)
		gridEnd    = reporting.BeginningOfDay(buckets[len(buckets)-1].At)
		totalWeeks = int(gridEnd.Sub(gridStart)/(24*time.Hour))/7 + 1
		weeksShown = min(totalWeeks, availableWeeks(opts.Width))
		firstWeek  = totalWeeks - weeksShown
		byDay      = make(map[string]int64, len(buckets))
		highest    = maximum(buckets)
	)
	for _, bucket := range buckets {
		byDay[dayKey(bucket.At)] += bucket.Total
	}

	rangeLabel := fmt.Sprintf("last %d weeks", weeksShown)
	if weeksShown == Weeks {
		rangeLabel = "last 12 months"
	}
	if highest == 0 {
		return writeEmpty(out, opts, rangeLabel)
	}

	var page strings.Builder
	page.Grow((weeksShown*cellWidth + gutter + 1) * 12)
	writeHeader(&page, opts, rangeLabel, Summarize(buckets, opts.Now), "days")

	page.WriteString(monthHeader(gridStart, firstWeek, weeksShown))
	page.WriteByte('\n')
	firstDay := reporting.BeginningOfDay(buckets[0].At)
	for row, label := range weekdayLabels {
		var line strings.Builder
		line.WriteString(labelStyle.Render(label))
		for week := range weeksShown {
			day := gridStart.AddDate(0, 0, (firstWeek+week)*7+row)
			line.WriteString(" ")
			if day.After(today) || day.Before(firstDay) {
				line.WriteString(" ")
				continue
			}
			line.WriteString(cell(byDay[dayKey(day)], highest))
		}
		page.WriteString(strings.TrimRight(line.String(), " "))
		page.WriteByte('\n')
	}
	page.WriteByte('\n')
	page.WriteString(legend())

	_, err := lipgloss.Fprintln(out, page.String())
	return err
}

// RenderStrip writes a single row of buckets, used for a window too short to
// fill a calendar. Labels sit over every second cell.
func RenderStrip(out io.Writer, buckets []Bucket, opts Options) error {
	if len(buckets) == 0 {
		return writeEmpty(out, opts, "today")
	}

	var (
		day        = reporting.BeginningOfDay(buckets[0].At)
		rangeLabel = day.Format("2006-01-02")
		highest    = maximum(buckets)
	)
	if day.Equal(reporting.BeginningOfDay(opts.Now)) {
		rangeLabel = "today"
	}
	if highest == 0 {
		return writeEmpty(out, opts, rangeLabel)
	}

	var page strings.Builder
	page.Grow((len(buckets)*cellWidth + gutter) * 4)
	writeHeader(&page, opts, rangeLabel, Summarize(buckets, opts.Now), "hours")

	page.WriteString(strings.Repeat(" ", gutter))
	for index, bucket := range buckets {
		if index%2 == 1 {
			continue
		}
		page.WriteString(labelStyle.Render(bucket.At.Format("15")))
		if index+2 < len(buckets) {
			page.WriteString(strings.Repeat(" ", cellWidth))
		}
	}
	page.WriteByte('\n')

	page.WriteString(strings.Repeat(" ", gutter-1))
	for _, bucket := range buckets {
		page.WriteString(" ")
		page.WriteString(cell(bucket.Total, highest))
	}
	page.WriteString("\n\n")
	page.WriteString(legend())

	_, err := lipgloss.Fprintln(out, page.String())
	return err
}

// writeHeader writes the title line and the stats line. unit names the bucket
// granularity for the active count ("days", "hours").
func writeHeader(page *strings.Builder, opts Options, rangeLabel string, stats Stats, bucketUnit string) {
	page.WriteString(titleStyle.Render(opts.Title))
	page.WriteString("   ")
	page.WriteString(labelStyle.Render(rangeLabel))
	page.WriteByte('\n')

	chips := make([]string, 0, 4+len(opts.Extra))
	chips = append(chips, fmt.Sprintf("%s %s across %d active %s",
		reporting.FormatTokenCount(stats.Total), opts.Unit, stats.ActiveCount, bucketUnit))
	if stats.Peak.Total > 0 {
		preposition, when := "on", stats.Peak.At.Format("2006-01-02")
		if bucketUnit == "hours" {
			preposition, when = "at", stats.Peak.At.Format("15:04")
		}
		chips = append(chips, fmt.Sprintf("Peak %s %s %s",
			reporting.FormatTokenCount(stats.Peak.Total), preposition, when))
	}
	if stats.LongestStreakDays > 0 {
		chips = append(chips, fmt.Sprintf("Streak %dd", stats.StreakDays),
			fmt.Sprintf("Longest streak %dd", stats.LongestStreakDays))
	}
	chips = append(chips, opts.Extra...)
	page.WriteString(wrapChips(chips, opts.Width))
	page.WriteString("\n\n")
}

// wrapChips joins stat chips with a separator, breaking to a new line rather
// than overflowing a narrow terminal. A chip wider than the budget still
// overflows: truncating it would hide a number.
func wrapChips(chips []string, width int) string {
	if width <= 0 {
		return strings.Join(chips, " · ")
	}
	const separator = " · "
	var (
		line strings.Builder
		used int
	)
	for index, chip := range chips {
		size := lipgloss.Width(chip)
		switch {
		case index == 0:
			line.WriteString(chip)
			used = size
		case used+len(separator)+size <= width:
			line.WriteString(separator)
			line.WriteString(chip)
			used += len(separator) + size
		default:
			line.WriteByte('\n')
			line.WriteString(chip)
			used = size
		}
	}
	return line.String()
}

// writeEmpty reports that nothing was recorded, in the lowercase style the
// other report commands use for their empty states.
func writeEmpty(out io.Writer, opts Options, rangeLabel string) error {
	_, err := fmt.Fprintf(out, "no %s, %s\n", strings.ToLower(opts.Title), rangeLabel)
	return err
}

// availableWeeks reports how many columns fit in width. A zero or negative
// width means no terminal was detected, so the full grid is rendered.
func availableWeeks(width int) int {
	if width <= 0 {
		return Weeks
	}
	return min(Weeks, max(1, (width-gutter+1)/cellWidth))
}

// monthHeader places a month label at the column of the week containing that
// month's first day. A label that would overlap its predecessor is skipped,
// which the dashboard gets for free from a CSS grid span.
func monthHeader(gridStart time.Time, firstWeek, weeksShown int) string {
	header := []byte(strings.Repeat(" ", gutter+weeksShown*cellWidth))
	lastEnd := 0
	for week := range weeksShown {
		weekStart := gridStart.AddDate(0, 0, (firstWeek+week)*7)
		label, ok := monthLabel(weekStart, week == 0)
		column := gutter + week*cellWidth
		if !ok || column < lastEnd {
			continue
		}
		copy(header[column:], label)
		lastEnd = column + len(label) + 1
	}
	return labelStyle.Render(strings.TrimRight(string(header), " "))
}

// monthLabel returns the short month name to print over a week: the month
// starting inside it, or the week's own month when it is the first column.
func monthLabel(weekStart time.Time, first bool) (string, bool) {
	for offset := range 7 {
		if weekStart.AddDate(0, 0, offset).Day() == 1 {
			return weekStart.AddDate(0, 0, offset).Format("Jan"), true
		}
	}
	if first {
		return weekStart.Format("Jan"), true
	}
	return "", false
}

// cell renders one shaded bucket.
func cell(total, highest int64) string {
	level := levels[Level(total, highest)]
	return lipgloss.NewStyle().Foreground(level.color).Render(level.glyph)
}

func legend() string {
	var line strings.Builder
	line.WriteString(labelStyle.Render("Less"))
	for index := range levels {
		line.WriteString(" ")
		line.WriteString(lipgloss.NewStyle().Foreground(levels[index].color).Render(levels[index].glyph))
	}
	line.WriteString(" ")
	line.WriteString(labelStyle.Render("More"))
	return line.String()
}

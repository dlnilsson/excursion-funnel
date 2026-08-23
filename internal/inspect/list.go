package inspect

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/picker"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const (
	defaultListWidth = 100
	shortIDLength    = 12
)

func shouldUseRequestList(stdinTerminal, stdoutTerminal bool) bool {
	return stdinTerminal && stdoutTerminal
}

func runRequestList(ctx context.Context, in io.Reader, out io.Writer, rows []report.RecentRequestRow) (string, error) {
	selected, chosen, err := picker.Run(ctx, in, out, rows, picker.Options[report.RecentRequestRow]{
		Title:           "Recent requests",
		ItemName:        "request",
		ItemPlural:      "requests",
		ActionHelp:      "inspect",
		ShowDescription: true,
		Width:           defaultListWidth,
		Render:          renderRequestItem,
	})
	if err != nil || !chosen {
		return "", err
	}
	return selected.ID, nil
}

func renderRequestItem(row report.RecentRequestRow) (title, description, filterValue string) {
	status := requestStatus(row.HTTPStatus.Valid, row.HTTPStatus.Int64)
	title = displayValue(row.Model) + " · " + displayClient(row.Client)
	description = strings.Join([]string{
		reporting.LocalTimestamp(row.StartedAt),
		displayValue(row.Provider),
		"status=" + status,
		strings.TrimSpace(row.Method + " " + reporting.CompactValue(row.Path)),
		"id=" + shortRequestID(row.ID),
	}, "  •  ")
	filterValue = strings.Join([]string{
		row.ID, row.ResponseID, row.Provider, row.Client, row.Model, row.Method, row.Path, status,
	}, " ")
	return title, description, filterValue
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func displayClient(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "unknown") {
		return "-"
	}
	return displayValue(value)
}

func requestStatus(valid bool, status int64) string {
	if !valid {
		return "-"
	}
	return strconv.FormatInt(status, 10)
}

func shortRequestID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength] + "…"
}

package usage

import (
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

// modelFamily drops version segments so releases of one model line share a
// row: claude-opus-5-5 -> claude-opus, gpt-6.1-sol -> gpt-sol. It mirrors
// modelFamily in the dashboard's index.html.
func modelFamily(model string) string {
	parts := strings.Split(model, "-")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if !isVersionSegment(part) {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return model
	}
	return strings.Join(kept, "-")
}

// isVersionSegment reports whether part is dot-separated digits, like 5 or 6.1.
func isVersionSegment(part string) bool {
	if part == "" {
		return false
	}
	for number := range strings.SplitSeq(part, ".") {
		if number == "" || strings.Trim(number, "0123456789") != "" {
			return false
		}
	}
	return true
}

// mergeRows merges rows that match once model versions are reduced to their
// family (family) and clients are dropped (clients), keeping the order in
// which each group first appears.
func mergeRows(rows []report.SummaryRow, family, clients bool) []report.SummaryRow {
	var (
		merged = make([]report.SummaryRow, 0, len(rows))
		index  = make(map[report.SummaryRow]int, len(rows))
	)
	for _, row := range rows {
		if family {
			row.Model = modelFamily(row.Model)
		}
		if clients {
			row.Client = ""
		}
		key := report.SummaryRow{
			Day:       row.Day,
			Provider:  row.Provider,
			Client:    row.Client,
			Model:     row.Model,
			Source:    row.Source,
			Directory: row.Directory,
			GitBranch: row.GitBranch,
		}
		at, ok := index[key]
		if !ok {
			index[key] = len(merged)
			merged = append(merged, row)
			continue
		}
		total := &merged[at]
		total.Requests += row.Requests
		total.Errors += row.Errors
		total.Input += row.Input
		total.FreshInput += row.FreshInput
		total.Cached += row.Cached
		total.CacheWrite += row.CacheWrite
		total.Output += row.Output
		total.Reasoning += row.Reasoning
		total.Total += row.Total
	}
	return merged
}

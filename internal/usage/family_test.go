package usage

import (
	"slices"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestModelFamily(t *testing.T) {
	tests := map[string]string{
		"claude-opus-5":              "claude-opus",
		"claude-opus-5-5":            "claude-opus",
		"claude-opus-4-8":            "claude-opus",
		"claude-sonnet-5":            "claude-sonnet",
		"claude-sonnet-4-5-20250929": "claude-sonnet",
		"gpt-5.6-sol":                "gpt-sol",
		"gpt-6.1-sol":                "gpt-sol",
		"gpt-6-astra":                "gpt-astra",
		"gpt-5.6-terra":              "gpt-terra",
		"gpt-4o":                     "gpt-4o",
		"gpt-5.":                     "gpt-5.",
		"unknown":                    "unknown",
		"5":                          "5",
		"":                           "",
	}
	for model, want := range tests {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestGroupByFamily(t *testing.T) {
	rows := []report.SummaryRow{
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-opus-5", Requests: 1, Input: 10, Output: 1, Total: 11},
		{Provider: "openai", Client: "Codex", Model: "gpt-5.6-sol", Requests: 2, Input: 20, Output: 2, Total: 22},
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-opus-5-5", Requests: 3, Input: 30, Output: 3, Total: 33},
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-sonnet-5", Requests: 4, Input: 40, Output: 4, Total: 44},
		{Provider: "openai", Client: "Codex", Model: "gpt-6-astra", Requests: 5, Input: 50, Output: 5, Total: 55},
		{Provider: "openai", Client: "Codex", Model: "gpt-6.1-sol", Requests: 6, Input: 60, Output: 6, Total: 66},
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-opus-4-8", Requests: 7, Input: 70, Output: 7, Total: 77},
		{Provider: "openai", Client: "Codex", Model: "gpt-5.6-terra", Requests: 8, Input: 80, Output: 8, Total: 88},
	}

	got := groupByFamily(rows)

	want := []report.SummaryRow{
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-opus", Requests: 11, Input: 110, Output: 11, Total: 121},
		{Provider: "openai", Client: "Codex", Model: "gpt-sol", Requests: 8, Input: 80, Output: 8, Total: 88},
		{Provider: "anthropic", Client: "Claude Code", Model: "claude-sonnet", Requests: 4, Input: 40, Output: 4, Total: 44},
		{Provider: "openai", Client: "Codex", Model: "gpt-astra", Requests: 5, Input: 50, Output: 5, Total: 55},
		{Provider: "openai", Client: "Codex", Model: "gpt-terra", Requests: 8, Input: 80, Output: 8, Total: 88},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("groupByFamily() =\n%+v\nwant\n%+v", got, want)
	}
	if rows[0].Model != "claude-opus-5" {
		t.Fatalf("groupByFamily mutated input: Model = %q", rows[0].Model)
	}
}

func TestGroupByFamilyKeepsOtherDimensionsApart(t *testing.T) {
	rows := []report.SummaryRow{
		{Day: "2026-10-15", Model: "claude-opus-5", Total: 1},
		{Day: "2026-10-16", Model: "claude-opus-5-5", Total: 2},
		{Day: "2026-10-16", Model: "claude-opus-5", Total: 3},
	}

	got := groupByFamily(rows)

	want := []report.SummaryRow{
		{Day: "2026-10-15", Model: "claude-opus", Total: 1},
		{Day: "2026-10-16", Model: "claude-opus", Total: 5},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("groupByFamily() =\n%+v\nwant\n%+v", got, want)
	}
}

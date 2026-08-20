package usage

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestPrintJSONEmptyRowsIsArray(t *testing.T) {
	var output bytes.Buffer
	if err := printJSON(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "[]\n" {
		t.Fatalf("output = %q, want empty array", got)
	}
}

func TestPrintRowsEmptyRowsKeepsMessage(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, nil, "model"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "no usage rows\n" {
		t.Fatalf("output = %q, want empty-state message", got)
	}
}

func TestPrintRowsRendersEveryGroupingAsTable(t *testing.T) {
	row := report.SummaryRow{
		Day:        "2026-08-20",
		Provider:   "openai",
		Client:     "codex",
		Model:      "gpt-5.6-sol",
		Source:     "workstation",
		Directory:  "/work/api",
		GitBranch:  "feature/table",
		Requests:   1,
		Errors:     2,
		FreshInput: 3,
		Cached:     4,
		CacheWrite: 5,
		Output:     6,
		Reasoning:  7,
		Total:      8,
	}
	tests := []struct {
		groupBy string
		want    []string
	}{
		{groupBy: "model", want: []string{"PROVIDER", "CLIENT", "MODEL", "openai", "codex", "gpt-5.6-sol"}},
		{groupBy: "provider", want: []string{"PROVIDER", "openai"}},
		{groupBy: "day", want: []string{"DAY", "PROVIDER", "CLIENT", "MODEL", "2026-08-20", "openai", "codex", "gpt-5.6-sol"}},
		{groupBy: "source", want: []string{"SOURCE", "workstation"}},
		{groupBy: "directory", want: []string{"DIRECTORY", "/work/api"}},
		{groupBy: "git_branch", want: []string{"GIT_BRANCH", "feature/table"}},
	}

	for _, test := range tests {
		t.Run(test.groupBy, func(t *testing.T) {
			var output bytes.Buffer
			if err := printRows(&output, []report.SummaryRow{row}, test.groupBy); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if strings.Contains(text, "\x1b[") {
				t.Fatalf("redirected output contains ANSI escapes: %q", text)
			}
			lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
			if len(lines) != 5 || !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") ||
				!strings.HasPrefix(lines[len(lines)-1], "╰") || !strings.HasSuffix(lines[len(lines)-1], "╯") {
				t.Fatalf("output does not have the expected compact rounded table shape:\n%s", text)
			}
			for _, want := range append(test.want, "REQ", "ERR", "INPUT", "CACHED", "CACHE_WRITE", "OUTPUT", "REASONING", "TOTAL") {
				if !strings.Contains(text, want) {
					t.Errorf("output missing %q:\n%s", want, text)
				}
			}
		})
	}
}

func TestPrintRowsRightAlignsNumbersWithoutRowSeparators(t *testing.T) {
	rows := []report.SummaryRow{
		{Provider: "openai", Requests: 1},
		{Provider: "anthropic", Requests: 1000},
	}
	var output bytes.Buffer
	if err := printRows(&output, rows, "provider"); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("line count = %d, want 6 without data-row separators:\n%s", len(lines), output.String())
	}
	firstCells := strings.Split(lines[3], "│")
	secondCells := strings.Split(lines[4], "│")
	if len(firstCells) < 3 || len(secondCells) < 3 {
		t.Fatalf("could not split rendered rows into cells:\n%s", output.String())
	}
	if first := strings.Index(firstCells[2], "1"); first <= strings.Index(secondCells[2], "1000") {
		t.Fatalf("REQ values are not right-aligned: %q and %q", firstCells[2], secondCells[2])
	}
}

func TestPrintRowsPropagatesWriteErrors(t *testing.T) {
	errWrite := errors.New("write failed")
	writer := errorWriter{err: errWrite}
	if err := printRows(writer, []report.SummaryRow{{Provider: "openai"}}, "provider"); !errors.Is(err, errWrite) {
		t.Fatalf("printRows() error = %v, want %v", err, errWrite)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

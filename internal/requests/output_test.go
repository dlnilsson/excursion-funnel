package requests

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestPrintRowsRendersDashboardColumns(t *testing.T) {
	rows := []report.WebRequestRow{{
		StartedAt: time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Client:    "codex",
		Model:     "gpt-5.6-sol",
		Name:      "web_search_call",
		Query:     "Go release",
		Domain:    "go.dev",
	}}
	var output bytes.Buffer
	if err := printRows(&output, rows, false); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"STARTED", "CLIENT", "MODEL", "WEB TOOL", "QUERY", "URL/DOMAIN", "codex", "web_search_call", "Go release", "go.dev"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintRowsPrefersURLOverDomain(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, []report.WebRequestRow{{URL: "https://example.com/page", Domain: "example.com"}}, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "https://example.com/page") || strings.Contains(got, "│ example.com") {
		t.Fatalf("destination does not match dashboard preference:\n%s", got)
	}
}

func TestPrintRowsEmptyRowsKeepsMessage(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, nil, true); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "no web requests recorded today\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestPrintRowsPropagatesWriteErrors(t *testing.T) {
	errWrite := errors.New("write failed")
	if err := printRows(requestErrorWriter{err: errWrite}, []report.WebRequestRow{{Client: "codex"}}, false); !errors.Is(err, errWrite) {
		t.Fatalf("printRows() error = %v, want %v", err, errWrite)
	}
}

type requestErrorWriter struct{ err error }

func (w requestErrorWriter) Write([]byte) (int, error) { return 0, w.err }

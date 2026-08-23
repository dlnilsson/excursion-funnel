package inspect

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/termio"
)

func TestShouldUseRequestList(t *testing.T) {
	tests := []struct {
		name           string
		stdinTerminal  bool
		stdoutTerminal bool
		want           bool
	}{
		{name: "interactive", stdinTerminal: true, stdoutTerminal: true, want: true},
		{name: "redirected input", stdoutTerminal: true},
		{name: "redirected output", stdinTerminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseRequestList(test.stdinTerminal, test.stdoutTerminal); got != test.want {
				t.Fatalf("shouldUseRequestList() = %t, want %t", got, test.want)
			}
		})
	}
	if termio.IsTerminal(strings.NewReader("")) || termio.IsTerminal(&bytes.Buffer{}) {
		t.Fatal("buffered streams detected as terminals")
	}
}

func TestRequestItemEmphasizesModelAndMetadata(t *testing.T) {
	row := report.RecentRequestRow{
		ID:         "req-1234567890abcdef",
		ResponseID: "resp-full-value",
		StartedAt:  time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Provider:   "openai",
		Client:     "Codex CLI",
		Model:      "gpt-5.6-sol",
		HTTPStatus: sql.NullInt64{Int64: 200, Valid: true},
		Method:     "POST",
		Path:       "/v1/responses",
	}
	title, description, filterValue := renderRequestItem(row)
	if title != "gpt-5.6-sol · Codex CLI" {
		t.Fatalf("title = %q", title)
	}
	for _, want := range []string{"openai", "status=200", "POST /v1/responses", "id=req-12345678…"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q: %s", want, description)
		}
	}
	for _, want := range []string{row.ID, row.ResponseID, row.Model, row.Client, row.Path} {
		if !strings.Contains(filterValue, want) {
			t.Fatalf("filter value missing %q: %s", want, filterValue)
		}
	}
}

package store

import (
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// The column tables generate the DDL, the column lists, both INSERT forms and
// the staging merge. These tests hold those derivations to each other, so a new
// column cannot be half-added.

func TestRequestInsertBindsEveryColumn(t *testing.T) {
	placeholders := strings.Count(insertRequestSQL, "?")
	bound := 0
	serverSupplied := 0
	for _, col := range requestsTable.columns {
		if col.value == nil {
			serverSupplied++
			continue
		}
		bound++
	}
	if placeholders != bound {
		t.Fatalf("insert has %d placeholders for %d bound columns", placeholders, bound)
	}
	if serverSupplied != 1 {
		t.Fatalf("server-supplied columns = %d, want exactly created_at", serverSupplied)
	}

	args := requestsTable.args(newRequestRow(queue.UsageEvent{RequestID: "req-1"}))
	if len(args) != bound {
		t.Fatalf("args = %d, want %d bound columns", len(args), bound)
	}
}

func TestChildInsertsBindEveryColumn(t *testing.T) {
	if got, want := strings.Count(insertToolCallSQL, "?"), len(toolCallsTable.columns); got != want {
		t.Fatalf("tool call insert has %d placeholders, want %d", got, want)
	}
	if got, want := strings.Count(insertWebRequestSQL, "?"), len(webRequestsTable.columns); got != want {
		t.Fatalf("web request insert has %d placeholders, want %d", got, want)
	}
}

func TestValuesTupleRendersOneLiteralPerColumn(t *testing.T) {
	row := newRequestRow(queue.UsageEvent{
		RequestID:   "req-1",
		StartedAt:   time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2026, time.August, 20, 10, 0, 1, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/messages",
		UpstreamURL: "https://api.anthropic.com/v1/messages",
		HTTPStatus:  200,
		Stream:      true,
	})
	tuple := requestsTable.valuesTuple(row)
	if !strings.HasPrefix(tuple, "(") || !strings.HasSuffix(tuple, ")") {
		t.Fatalf("tuple is not parenthesized: %s", tuple)
	}
	// created_at is the one column rendered as a server-side literal.
	if !strings.HasSuffix(tuple, ", current_timestamp)") {
		t.Fatalf("tuple does not end with the created_at literal: %s", tuple)
	}
	// A NULL for every absent optional column, and no empty-string stand-ins.
	if strings.Contains(tuple, "''") {
		t.Fatalf("absent value rendered as an empty string rather than NULL: %s", tuple)
	}
	if !strings.Contains(tuple, "NULL") {
		t.Fatalf("absent optional columns not rendered as NULL: %s", tuple)
	}
}

func TestColumnNamesMatchDDLOrder(t *testing.T) {
	names := strings.Split(requestsTable.columnNames(), ", ")
	ddlLines := strings.Split(requestsTable.columnsDDL(), ",\n  ")
	if len(names) != len(ddlLines) {
		t.Fatalf("%d names but %d DDL columns", len(names), len(ddlLines))
	}
	for i, name := range names {
		if !strings.HasPrefix(ddlLines[i], name+" ") {
			t.Fatalf("column %d: name %q does not head DDL %q", i, name, ddlLines[i])
		}
	}
}

func TestMergeStagingSelectsTheSameColumnsItInserts(t *testing.T) {
	// A mismatch here would silently shift values between columns during the
	// hub merge, which no constraint would catch.
	for _, sql := range []string{
		requestsTable.mergeStagingSQL(),
		toolCallsTable.mergeStagingSQL(),
		webRequestsTable.mergeStagingSQL(),
	} {
		inserted := between(t, sql, "(", ")")
		selected := strings.TrimSpace(between(t, sql, "SELECT ", " FROM "))
		if inserted != selected {
			t.Fatalf("merge inserts (%s) but selects (%s)", inserted, selected)
		}
	}
}

func TestSQLLiteralRendersDriverTypes(t *testing.T) {
	value := int64(42)
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{name: "nil", input: nil, want: "NULL"},
		{name: "nil pointer", input: (*int64)(nil), want: "NULL"},
		{name: "pointer", input: &value, want: "42"},
		{name: "int", input: 7, want: "7"},
		{name: "int64", input: int64(7), want: "7"},
		{name: "bool", input: true, want: "true"},
		{name: "string", input: "plain", want: "'plain'"},
		{name: "string with quote", input: "it's", want: "'it''s'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sqlLiteral(tt.input); got != tt.want {
				t.Fatalf("sqlLiteral(%v) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
	if got := sqlLiteral(time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)); !strings.HasSuffix(got, "::TIMESTAMPTZ") {
		t.Fatalf("time literal = %s, want a TIMESTAMPTZ cast", got)
	}
}

func TestWebRequestRowsKeepProviderOrdinals(t *testing.T) {
	// Ordinals index the provider's own output, so filtering a non-web entry
	// must not renumber the rows that follow it: the ordinal is half the
	// primary key and has to stay stable across a retry.
	rows := webRequestRows(queue.UsageEvent{
		RequestID: "req-1",
		WebRequests: []queue.WebRequest{
			{Name: "web_search"},
			{Name: "GET"},
			{Name: "WebFetch"},
		},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 web entries", len(rows))
	}
	if rows[0].ordinal != 0 || rows[1].ordinal != 2 {
		t.Fatalf("ordinals = %d, %d; want 0, 2", rows[0].ordinal, rows[1].ordinal)
	}
}

func TestToolCallRowsKeepProviderOrdinals(t *testing.T) {
	rows := toolCallRows(queue.UsageEvent{
		RequestID: "req-1",
		ToolCalls: []queue.ToolCall{{Name: "Bash"}, {Name: ""}, {Name: "Read"}},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 named calls", len(rows))
	}
	if rows[0].ordinal != 0 || rows[1].ordinal != 2 {
		t.Fatalf("ordinals = %d, %d; want 0, 2", rows[0].ordinal, rows[1].ordinal)
	}
}

func between(t *testing.T, s, open, close string) string {
	t.Helper()
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatalf("missing %q in %s", open, s)
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("missing %q in %s", close, s)
	}
	return rest[:j]
}

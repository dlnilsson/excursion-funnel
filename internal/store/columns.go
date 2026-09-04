package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/provider"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// The ledger projection used to be spelled out five times per table: the DDL,
// the column-name list, the INSERT statement, the placeholder argument list,
// and — for the remote path — the same row again as SQL literals. Adding a
// column meant five coordinated edits with no compiler help, and validateSchema
// held a sixth copy of the names.
//
// A column table is the single source instead. Everything below is derived from
// it, so a new column is one entry.

// column is one ledger column: its name, its DDL type and constraints, and how
// to read its value out of a row being written.
type column[T any] struct {
	name string
	ddl  string
	// value returns the driver argument for this column. A nil value marks a
	// column the server supplies (created_at), which is emitted as a literal in
	// the INSERT rather than bound.
	value func(T) any
	// literal overrides how value is rendered on the remote path, where the row
	// is inlined into SQL text rather than bound. Empty means render value.
	literal string
}

// table is a ledger table and the constraint that makes an insert idempotent.
type table[T any] struct {
	name       string
	primaryKey string
	columns    []column[T]
}

// requestRow is one request insert, with the fields InsertBatch derives before
// writing computed once so both the bound and the literal path see identical
// values.
type requestRow struct {
	event       queue.UsageEvent
	completedAt any
	durationMS  any
	usageJSON   any
	source      string
	sessionID   string
}

func newRequestRow(ev queue.UsageEvent) requestRow {
	row := requestRow{event: ev, source: ev.Source, sessionID: eventSessionID(ev)}
	if !ev.CompletedAt.IsZero() {
		row.completedAt = ev.CompletedAt
		row.durationMS = ev.CompletedAt.Sub(ev.StartedAt).Milliseconds()
	}
	if len(ev.UsageJSON) > 0 {
		row.usageJSON = string(ev.UsageJSON)
	}
	if row.source == "" {
		row.source = "unknown"
	}
	return row
}

// toolCallRow and webRequestRow pair a child record with its parent request and
// its position, which together form the primary key.
type toolCallRow struct {
	requestID string
	ordinal   int
	call      queue.ToolCall
}

type webRequestRow struct {
	requestID string
	ordinal   int
	request   queue.WebRequest
}

// Only constant defaults appear in the DDL: Quack 1.5.x cannot reconstruct
// attached catalogs whose column defaults contain a bound expression such as
// current_timestamp, so created_at is supplied explicitly on every insert.
var requestsTable = table[requestRow]{
	name:       "requests",
	primaryKey: "id",
	columns: []column[requestRow]{
		{name: "id", ddl: "VARCHAR", value: func(r requestRow) any { return r.event.RequestID }},
		{name: "response_id", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ResponseID) }},
		{name: "source", ddl: "VARCHAR NOT NULL DEFAULT 'unknown'", value: func(r requestRow) any { return r.source }},
		{name: "host", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.Host) }},
		{name: "started_at", ddl: "TIMESTAMPTZ NOT NULL", value: func(r requestRow) any { return r.event.StartedAt }},
		{name: "completed_at", ddl: "TIMESTAMPTZ", value: func(r requestRow) any { return r.completedAt }},
		{name: "duration_ms", ddl: "BIGINT", value: func(r requestRow) any { return r.durationMS }},
		{name: "method", ddl: "VARCHAR NOT NULL", value: func(r requestRow) any { return r.event.Method }},
		{name: "path", ddl: "VARCHAR NOT NULL", value: func(r requestRow) any { return r.event.Path }},
		{name: "upstream_url", ddl: "VARCHAR NOT NULL", value: func(r requestRow) any { return r.event.UpstreamURL }},
		{name: "model_requested", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ModelRequested) }},
		{name: "model_reported", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ModelReported) }},
		{name: "stream", ddl: "BOOLEAN NOT NULL DEFAULT false", value: func(r requestRow) any { return r.event.Stream }},
		{name: "effort", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.Effort) }},
		{name: "http_status", ddl: "INTEGER", value: func(r requestRow) any { return r.event.HTTPStatus }},
		{name: "upstream_request_id", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.UpstreamRequestID) }},
		{name: "user_agent", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.UserAgent) }},
		{name: "originator", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.Originator) }},
		{name: "client_name", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ClientName) }},
		{name: "session_id", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.sessionID) }},
		{name: "codex_session_id", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.CodexSessionID) }},
		{name: "directory", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.Directory) }},
		{name: "git_branch", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.GitBranch) }},
		{name: "error_type", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ErrorType) }},
		{name: "error_message", ddl: "VARCHAR", value: func(r requestRow) any { return nullableString(r.event.ErrorMessage) }},
		{name: "input_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.InputTokens }},
		{name: "cached_input_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.CachedInputTokens }},
		{name: "cache_write_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.CacheWriteTokens }},
		{name: "output_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.OutputTokens }},
		{name: "reasoning_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.ReasoningTokens }},
		{name: "total_tokens", ddl: "BIGINT", value: func(r requestRow) any { return r.event.Usage.TotalTokens }},
		{name: "usage_json", ddl: "VARCHAR", value: func(r requestRow) any { return r.usageJSON }},
		{name: "created_at", ddl: "TIMESTAMPTZ NOT NULL", literal: "current_timestamp"},
	},
}

var toolCallsTable = table[toolCallRow]{
	name:       "tool_calls",
	primaryKey: "request_id, ordinal",
	columns: []column[toolCallRow]{
		{name: "request_id", ddl: "VARCHAR NOT NULL", value: func(r toolCallRow) any { return r.requestID }},
		{name: "ordinal", ddl: "INTEGER NOT NULL", value: func(r toolCallRow) any { return r.ordinal }},
		{name: "tool_call_id", ddl: "VARCHAR", value: func(r toolCallRow) any { return nullableString(r.call.ID) }},
		{name: "name", ddl: "VARCHAR NOT NULL", value: func(r toolCallRow) any { return r.call.Name }},
		{name: "command", ddl: "VARCHAR", value: func(r toolCallRow) any { return nullableString(r.call.Command) }},
		{name: "description", ddl: "VARCHAR", value: func(r toolCallRow) any { return nullableString(r.call.Description) }},
		{name: "arguments_json", ddl: "VARCHAR", value: func(r toolCallRow) any { return nullableString(r.call.ArgumentsJSON) }},
	},
}

var webRequestsTable = table[webRequestRow]{
	name:       "web_requests",
	primaryKey: "request_id, ordinal",
	columns: []column[webRequestRow]{
		{name: "request_id", ddl: "VARCHAR NOT NULL", value: func(r webRequestRow) any { return r.requestID }},
		{name: "ordinal", ddl: "INTEGER NOT NULL", value: func(r webRequestRow) any { return r.ordinal }},
		{name: "web_request_id", ddl: "VARCHAR", value: func(r webRequestRow) any { return nullableString(r.request.ID) }},
		{name: "name", ddl: "VARCHAR NOT NULL", value: func(r webRequestRow) any { return r.request.Name }},
		{name: "query", ddl: "VARCHAR", value: func(r webRequestRow) any { return nullableString(r.request.Query) }},
		{name: "url", ddl: "VARCHAR", value: func(r webRequestRow) any { return nullableString(r.request.URL) }},
		{name: "domain", ddl: "VARCHAR", value: func(r webRequestRow) any { return nullableString(r.request.Domain) }},
		{name: "arguments_json", ddl: "VARCHAR", value: func(r webRequestRow) any { return nullableString(r.request.ArgumentsJSON) }},
	},
}

// sessionRow and sessionsTable complete the set. The registry has only three
// columns and no derived values, but sharing the table type keeps schema
// creation and the primary-key repair loop uniform.
type sessionRow struct {
	provider  string
	sessionID string
	firstSeen time.Time
}

var sessionsTable = table[sessionRow]{
	name:       "sessions",
	primaryKey: "provider, session_id",
	columns: []column[sessionRow]{
		{name: "provider", ddl: "VARCHAR NOT NULL", value: func(r sessionRow) any { return r.provider }},
		{name: "session_id", ddl: "VARCHAR NOT NULL", value: func(r sessionRow) any { return r.sessionID }},
		{name: "first_seen_at", ddl: "TIMESTAMPTZ NOT NULL", value: func(r sessionRow) any { return r.firstSeen }},
	},
}

// columnsDDL renders the column definitions for a CREATE TABLE body.
func (t table[T]) columnsDDL() string {
	var b strings.Builder
	for i, col := range t.columns {
		if i > 0 {
			b.WriteString(",\n  ")
		}
		b.WriteString(col.name)
		b.WriteByte(' ')
		b.WriteString(col.ddl)
	}
	return b.String()
}

// columnNames renders the column list for an INSERT or SELECT.
func (t table[T]) columnNames() string {
	names := make([]string, 0, len(t.columns))
	for _, col := range t.columns {
		names = append(names, col.name)
	}
	return strings.Join(names, ", ")
}

// names returns the column names as a slice, for schema validation.
func (t table[T]) names() []string {
	out := make([]string, 0, len(t.columns))
	for _, col := range t.columns {
		out = append(out, col.name)
	}
	return out
}

// createSQL renders a constrained CREATE TABLE IF NOT EXISTS.
func (t table[T]) createSQL() string {
	return "CREATE TABLE IF NOT EXISTS " + t.name + " (\n  " + t.columnsDDL() +
		",\n  PRIMARY KEY (" + t.primaryKey + ")\n);"
}

// createStagingSQL renders the constraint-free, index-free mirror that remote
// clients write into. The absence of an ART index is what keeps Quack's
// remote-write path from crashing.
func (t table[T]) createStagingSQL() string {
	return "CREATE TABLE IF NOT EXISTS staging_" + t.name + " (\n  " + t.columnsDDL() + "\n);"
}

// insertSQL renders an idempotent INSERT with one placeholder per bound column.
// Server-supplied columns are emitted as their literal instead.
func (t table[T]) insertSQL() string {
	values := make([]string, 0, len(t.columns))
	for _, col := range t.columns {
		if col.value == nil {
			values = append(values, col.literal)
			continue
		}
		values = append(values, "?")
	}
	return "INSERT INTO " + t.name + " (" + t.columnNames() + ")\nVALUES (" + strings.Join(values, ", ") + ")\n" +
		"ON CONFLICT (" + t.primaryKey + ") DO NOTHING"
}

// args returns the driver arguments for one row, in insertSQL's placeholder
// order.
func (t table[T]) args(row T) []any {
	out := make([]any, 0, len(t.columns))
	for _, col := range t.columns {
		if col.value == nil {
			continue
		}
		out = append(out, col.value(row))
	}
	return out
}

// valuesTuple renders one row as a parenthesized SQL literal tuple, for the
// remote path where rows are inlined into query text rather than bound.
func (t table[T]) valuesTuple(row T) string {
	rendered := make([]string, 0, len(t.columns))
	for _, col := range t.columns {
		if col.value == nil {
			rendered = append(rendered, col.literal)
			continue
		}
		rendered = append(rendered, sqlLiteral(col.value(row)))
	}
	return "(" + strings.Join(rendered, ", ") + ")"
}

// mergeStagingSQL folds the staging mirror into the indexed table.
func (t table[T]) mergeStagingSQL() string {
	names := t.columnNames()
	return "INSERT INTO " + t.name + " (" + names + ")\n" +
		"SELECT " + names + " FROM staging_" + t.name + "\n" +
		"ON CONFLICT (" + t.primaryKey + ") DO NOTHING"
}

// sqlLiteral renders a driver argument as SQL text. It covers exactly the types
// the column tables produce; anything else is a programming error, so it fails
// loudly as an unquotable literal rather than silently writing bad data.
func sqlLiteral(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return quoteLiteral(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case *int64:
		if v == nil {
			return "NULL"
		}
		return strconv.FormatInt(*v, 10)
	case time.Time:
		return sqlTime(v)
	default:
		// Unreachable for the current column tables; keep it explicit so a new
		// column with an unhandled type fails visibly at insert time.
		return "NULL"
	}
}

// toolCallRows projects an event's tool calls onto insertable rows, dropping
// unnamed calls. The ordinal is the call's index in the provider's own output,
// so it is taken before filtering and forms half the primary key.
func toolCallRows(ev queue.UsageEvent) []toolCallRow {
	rows := make([]toolCallRow, 0, len(ev.ToolCalls))
	for ordinal, call := range ev.ToolCalls {
		if call.Name == "" {
			continue
		}
		rows = append(rows, toolCallRow{requestID: ev.RequestID, ordinal: ordinal, call: call})
	}
	return rows
}

// webRequestRows projects an event's web activity onto insertable rows. The
// activity ledger records provider web-tool calls from both providers (Codex's
// web_search_call and Claude Code's client-side WebSearch/WebFetch); generic
// forward-proxy traffic is not a web-tool call, so IsWebToolName keeps it out.
func webRequestRows(ev queue.UsageEvent) []webRequestRow {
	rows := make([]webRequestRow, 0, len(ev.WebRequests))
	for ordinal, request := range ev.WebRequests {
		if !provider.IsWebToolName(request.Name) {
			continue
		}
		rows = append(rows, webRequestRow{requestID: ev.RequestID, ordinal: ordinal, request: request})
	}
	return rows
}

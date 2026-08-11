// Package report reads usage summaries from the SQLite ledger.
package report

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// Reporter owns a SQLite connection used for reporting queries.
type Reporter struct {
	store *store.Store
}

// Open opens an existing usage ledger for reporting. It fails rather than
// creating one, so a mistyped --db reports the mistake instead of silently
// returning an empty result set.
func Open(path string) (*Reporter, error) {
	st, err := store.OpenExisting(path)
	if err != nil {
		return nil, err
	}
	return &Reporter{store: st}, nil
}

// New builds a Reporter over an already-open Store, for callers (such as the
// daemon itself) that already hold a connection and should not open a second
// one. Unlike Open, Close must not be called on a Reporter built this way —
// the caller owns the underlying Store's lifecycle.
func New(st *store.Store) *Reporter {
	return &Reporter{store: st}
}

// Close closes the underlying database connection.
func (r *Reporter) Close() error {
	return r.store.Close()
}

// SummaryOptions controls a usage aggregate query.
type SummaryOptions struct {
	Since   time.Time
	Until   time.Time
	GroupBy string
}

// SummaryRow is one aggregate row from the usage ledger.
type SummaryRow struct {
	Day        string
	Provider   string
	Client     string
	Model      string
	Requests   int64
	Errors     int64
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
	Reasoning  int64
	Total      int64
}

// InspectRow is a detailed request row for the inspect command.
type InspectRow struct {
	ID                string
	ResponseID        string
	StartedAt         string
	CompletedAt       string
	DurationMS        sql.NullInt64
	Method            string
	Path              string
	Provider          string
	UpstreamURL       string
	ModelRequested    string
	ModelReported     string
	Stream            bool
	HTTPStatus        sql.NullInt64
	UpstreamRequestID string
	UserAgent         string
	Originator        string
	Client            string
	CodexSessionID    string
	ErrorType         string
	ErrorMessage      string
	Input             sql.NullInt64
	Cached            sql.NullInt64
	CacheWrite        sql.NullInt64
	Output            sql.NullInt64
	Reasoning         sql.NullInt64
	Total             sql.NullInt64
	UsageJSON         string
	ToolCalls         []ToolCall
}

// ToolCall is one tool invocation associated with an inspected request.
type ToolCall struct {
	Ordinal       int
	ID            string
	Name          string
	Command       string
	Description   string
	ArgumentsJSON string
}

// ToolCallRow is a tool invocation with the request metadata needed by the
// dashboard's recent-tool table.
type ToolCallRow struct {
	RequestID   string
	StartedAt   string
	Provider    string
	Client      string
	Model       string
	Ordinal     int
	ID          string
	Name        string
	Command     string
	Description string
}

// ToolCallOptions controls a tool-call query. Since is inclusive and Until is
// exclusive, matching the usage-reporting date range semantics.
type ToolCallOptions struct {
	Since time.Time
	Until time.Time
	Limit int
}

// Summary returns usage totals for the selected interval.
func (r *Reporter) Summary(ctx context.Context, opts SummaryOptions) ([]SummaryRow, error) {
	groupBy := opts.GroupBy
	if groupBy == "" {
		groupBy = "model"
	}

	selectGroup, groupExpr, orderBy, err := summaryGrouping(groupBy)
	if err != nil {
		return nil, err
	}

	var (
		args  []any
		where []string
	)
	if !opts.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, formatTime(opts.Since))
	}
	if !opts.Until.IsZero() {
		where = append(where, "started_at < ?")
		args = append(args, formatTime(opts.Until))
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	query := buildSummaryQuery(selectGroup, whereSQL, groupExpr, orderBy)

	return r.scanSummary(ctx, query, args...)
}

// HistoricalByModel returns all-time per-model usage totals from the
// materialized usage_model_histogram table, refreshed periodically by the
// daemon's aggregate scheduler. Unlike Summary it never scans requests, so it
// stays cheap as history grows; the tradeoff is that results are only as fresh
// as the last store.RefreshUsageAggregates run. Day is always empty.
func (r *Reporter) HistoricalByModel(ctx context.Context) ([]SummaryRow, error) {
	const query = `
SELECT
  '' AS day, provider, client, model,
  request_count, error_count,
  input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens
FROM usage_model_histogram
ORDER BY total_tokens DESC, provider, client, model`
	return r.scanSummary(ctx, query)
}

// HistoricalByDay returns all-time per-day, per-model usage totals from the
// materialized usage_daily_model table, newest day first. See HistoricalByModel
// for freshness semantics.
func (r *Reporter) HistoricalByDay(ctx context.Context) ([]SummaryRow, error) {
	const query = `
SELECT
  day, provider, client, model,
  request_count, error_count,
  input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens
FROM usage_daily_model
ORDER BY day DESC, total_tokens DESC, provider, client, model`
	return r.scanSummary(ctx, query)
}

// scanSummary runs a query whose columns match SummaryRow's field order and
// collects the rows. Shared by Summary and the historical aggregate readers.
func (r *Reporter) scanSummary(ctx context.Context, query string, args ...any) ([]SummaryRow, error) {
	rows, err := r.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query summary: %w", err)
	}
	defer rows.Close()

	var out []SummaryRow
	for rows.Next() {
		var row SummaryRow
		if err := rows.Scan(
			&row.Day, &row.Provider, &row.Client, &row.Model,
			&row.Requests, &row.Errors,
			&row.Input, &row.Cached, &row.CacheWrite,
			&row.Output, &row.Reasoning, &row.Total,
		); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate summary: %w", err)
	}
	return out, nil
}

// DefaultInspectLimit bounds how many matching rows Inspect returns when the
// caller has no preference.
const DefaultInspectLimit = 20

// DefaultRecentToolCallLimit bounds the dashboard's recent tool-call query.
const DefaultRecentToolCallLimit = 50

// Inspect returns matching request details by internal request id or upstream
// response id, newest first, capped at limit. A limit of zero or less falls
// back to DefaultInspectLimit. Callers that need to know whether the result was
// truncated should ask for limit+1 and check the length.
func (r *Reporter) Inspect(ctx context.Context, id string, limit int) ([]InspectRow, error) {
	if limit <= 0 {
		limit = DefaultInspectLimit
	}
	return r.queryInspectRows(ctx, "WHERE id = ? OR response_id = ?", "started_at DESC", limit, id, id)
}

// RecentErrors returns the most recent requests that recorded an error,
// newest first.
func (r *Reporter) RecentErrors(ctx context.Context, limit int) ([]InspectRow, error) {
	return r.queryInspectRows(ctx, "WHERE error_type IS NOT NULL", "started_at DESC", limit)
}

// RecentErrorsWithin returns the most recent requests that recorded an error
// within the requested interval, newest first.
func (r *Reporter) RecentErrorsWithin(ctx context.Context, since, until time.Time, limit int) ([]InspectRow, error) {
	where := []string{"error_type IS NOT NULL"}
	var args []any
	if !since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, formatTime(since))
	}
	if !until.IsZero() {
		where = append(where, "started_at < ?")
		args = append(args, formatTime(until))
	}
	return r.queryInspectRows(ctx, "WHERE "+strings.Join(where, " AND "), "started_at DESC", limit, args...)
}

// RecentToolCalls returns the newest emitted tool calls, with their request
// metadata, newest request first and call order preserved within a request.
func (r *Reporter) RecentToolCalls(ctx context.Context, limit int) ([]ToolCallRow, error) {
	return r.ToolCalls(ctx, ToolCallOptions{Limit: limit})
}

// ToolCalls returns tool invocations within the requested interval, newest
// request first and call order preserved within a request.
func (r *Reporter) ToolCalls(ctx context.Context, opts ToolCallOptions) ([]ToolCallRow, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRecentToolCallLimit
	}
	providerExpr := providerSQL("r.path")
	clientExpr := clientSQL()
	var (
		args  []any
		where []string
	)
	if !opts.Since.IsZero() {
		where = append(where, "r.started_at >= ?")
		args = append(args, formatTime(opts.Since))
	}
	if !opts.Until.IsZero() {
		where = append(where, "r.started_at < ?")
		args = append(args, formatTime(opts.Until))
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT
  tc.request_id, r.started_at, `+providerExpr+`, `+clientExpr+`,
  COALESCE(r.model_reported, r.model_requested, 'unknown'),
  tc.ordinal, COALESCE(tc.tool_call_id, ''), tc.name,
  COALESCE(tc.command, ''), COALESCE(tc.description, '')
FROM tool_calls AS tc
JOIN requests AS r ON r.id = tc.request_id
`+whereSQL+`
ORDER BY r.started_at DESC, tc.ordinal ASC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("query recent tool calls: %w", err)
	}
	defer rows.Close()

	var out []ToolCallRow
	for rows.Next() {
		var row ToolCallRow
		if err := rows.Scan(
			&row.RequestID, &row.StartedAt, &row.Provider, &row.Client, &row.Model,
			&row.Ordinal, &row.ID, &row.Name, &row.Command, &row.Description,
		); err != nil {
			return nil, fmt.Errorf("scan recent tool call: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent tool calls: %w", err)
	}
	return out, nil
}

func (r *Reporter) queryInspectRows(ctx context.Context, whereClause, orderBy string, limit int, args ...any) ([]InspectRow, error) {
	query := buildInspectQuery(whereClause, orderBy)

	rows, err := r.store.DB().QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query inspect: %w", err)
	}
	defer rows.Close()

	var out []InspectRow
	for rows.Next() {
		var row InspectRow
		if err := rows.Scan(
			&row.ID, &row.ResponseID, &row.StartedAt, &row.CompletedAt,
			&row.DurationMS, &row.Method, &row.Path, &row.Provider, &row.UpstreamURL,
			&row.ModelRequested, &row.ModelReported, &row.Stream, &row.HTTPStatus,
			&row.UpstreamRequestID, &row.UserAgent, &row.Originator, &row.Client, &row.CodexSessionID,
			&row.ErrorType, &row.ErrorMessage,
			&row.Input, &row.Cached, &row.CacheWrite,
			&row.Output, &row.Reasoning, &row.Total, &row.UsageJSON,
		); err != nil {
			return nil, fmt.Errorf("scan inspect: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inspect: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close inspect rows: %w", err)
	}
	for i := range out {
		calls, err := r.toolCalls(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].ToolCalls = calls
	}
	return out, nil
}

func (r *Reporter) toolCalls(ctx context.Context, requestID string) ([]ToolCall, error) {
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT ordinal, COALESCE(tool_call_id, ''), name,
  COALESCE(command, ''), COALESCE(description, ''), COALESCE(arguments_json, '')
FROM tool_calls
WHERE request_id = ?
ORDER BY ordinal`, requestID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls for request %s: %w", requestID, err)
	}
	defer rows.Close()

	var out []ToolCall
	for rows.Next() {
		var call ToolCall
		if err := rows.Scan(&call.Ordinal, &call.ID, &call.Name, &call.Command, &call.Description, &call.ArgumentsJSON); err != nil {
			return nil, fmt.Errorf("scan tool call for request %s: %w", requestID, err)
		}
		out = append(out, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool calls for request %s: %w", requestID, err)
	}
	return out, nil
}

func summaryGrouping(groupBy string) (selectGroup, groupExpr, orderBy string, err error) {
	const (
		dayExpr   = "substr(started_at, 1, 10)"
		modelExpr = "COALESCE(model_reported, model_requested, 'unknown')"
	)
	providerExpr := providerSQL("path")
	clientExpr := clientSQL()

	switch groupBy {
	case "model":
		return summarySelect(providerExpr, clientExpr, modelExpr),
			joinSQLExprs(providerExpr, clientExpr, modelExpr),
			"provider, client, model", nil
	case "provider":
		return summarySelect(providerExpr, "", ""),
			providerExpr,
			"provider", nil
	case "day":
		return summarySelect(dayExpr, providerExpr, clientExpr, modelExpr),
			joinSQLExprs(dayExpr, providerExpr, clientExpr, modelExpr),
			"day, provider, client, model", nil
	default:
		return "", "", "", fmt.Errorf("unsupported group-by %q (want model, provider, or day)", groupBy)
	}
}

func buildSummaryQuery(selectGroup, whereSQL, groupExpr, orderBy string) string {
	var b strings.Builder
	b.Grow(len(selectGroup) + len(whereSQL) + len(groupExpr) + len(orderBy) + 520)
	b.WriteString("\nSELECT\n  ")
	b.WriteString(selectGroup)
	b.WriteString(`,
  COUNT(*) AS requests,
  SUM(CASE WHEN error_type IS NULL THEN 0 ELSE 1 END) AS errors,
  SUM(COALESCE(input_tokens, 0)) AS input_tokens,
  SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
  SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens,
  SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
  SUM(COALESCE(total_tokens, 0)) AS total_tokens
FROM requests
`)
	b.WriteString(whereSQL)
	b.WriteString("\nGROUP BY ")
	b.WriteString(groupExpr)
	b.WriteString("\nORDER BY ")
	b.WriteString(orderBy)
	return b.String()
}

func buildInspectQuery(whereClause, orderBy string) string {
	providerExpr := providerSQL("path")
	clientExpr := clientSQL()

	var b strings.Builder
	b.Grow(len(providerExpr) + len(clientExpr) + len(whereClause) + len(orderBy) + 620)
	b.WriteString(`
SELECT
  id, COALESCE(response_id, ''), started_at, COALESCE(completed_at, ''),
  duration_ms, method, path, `)
	b.WriteString(providerExpr)
	b.WriteString(`, upstream_url,
  COALESCE(model_requested, ''), COALESCE(model_reported, ''),
  stream, http_status, COALESCE(upstream_request_id, ''),
  COALESCE(user_agent, ''), COALESCE(originator, ''), `)
	b.WriteString(clientExpr)
	b.WriteString(`,
  COALESCE(codex_session_id, ''),
  COALESCE(error_type, ''), COALESCE(error_message, ''),
  input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens,
  COALESCE(usage_json, '')
FROM requests
`)
	b.WriteString(whereClause)
	b.WriteString("\nORDER BY ")
	b.WriteString(orderBy)
	b.WriteString("\nLIMIT ?")
	return b.String()
}

func summarySelect(parts ...string) string {
	switch len(parts) {
	case 3:
		var b strings.Builder
		b.Grow(len(parts[0]) + len(parts[1]) + len(parts[2]) + 50)
		b.WriteString("'' AS day, ")
		b.WriteString(parts[0])
		b.WriteString(" AS provider, ")
		if parts[1] == "" {
			b.WriteString("'' AS client")
		} else {
			b.WriteString(parts[1])
			b.WriteString(" AS client")
		}
		b.WriteString(", ")
		if parts[2] == "" {
			b.WriteString("'' AS model")
		} else {
			b.WriteString(parts[2])
			b.WriteString(" AS model")
		}
		return b.String()
	case 4:
		var b strings.Builder
		b.Grow(len(parts[0]) + len(parts[1]) + len(parts[2]) + len(parts[3]) + 55)
		b.WriteString(parts[0])
		b.WriteString(" AS day, ")
		b.WriteString(parts[1])
		b.WriteString(" AS provider, ")
		b.WriteString(parts[2])
		b.WriteString(" AS client, ")
		b.WriteString(parts[3])
		b.WriteString(" AS model")
		return b.String()
	default:
		return ""
	}
}

func joinSQLExprs(exprs ...string) string {
	var size int
	for _, expr := range exprs {
		size += len(expr)
	}
	size += 2 * (len(exprs) - 1)

	var b strings.Builder
	b.Grow(size)
	for i, expr := range exprs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(expr)
	}
	return b.String()
}

func formatTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000Z07:00")
}

// providerSQL and clientSQL delegate to the store package, the single source of
// truth for provider/client classification, so live reporting and the
// materialized aggregate refresh classify identically.
func providerSQL(column string) string { return store.ProviderSQL(column) }

func clientSQL() string { return store.ClientSQL() }

// Package report reads live usage summaries from DuckDB ledgers.
package report

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// Reporter owns a Store only when created by Open or OpenRemote.
type Reporter struct {
	store *store.Store
	owned bool
}

// Open first tries the configured daemon/hub through Quack, then falls back to
// a read-only file open when a standalone daemon is not running. A configured
// hub never falls back to a stale local ledger.
//
// Quack is only consulted when path equals defaultPath — the caller's
// unmodified default ledger location. An explicit --db override naming a
// different file always reads that file directly, even if some daemon
// happens to be reachable on the default Quack port: otherwise a user
// pointing at a specific archived ledger would silently get a different
// (whichever daemon's) ledger instead, with no indication their flag was
// ignored.
func Open(path, defaultPath string) (*Reporter, error) {
	if hub := os.Getenv("EF_HUB_ADDR"); hub != "" {
		insecure := false
		if raw := os.Getenv("EF_HUB_INSECURE"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("EF_HUB_INSECURE: %w", err)
			}
			insecure = parsed
		}
		return OpenRemote(hub, os.Getenv("EF_HUB_TOKEN"), insecure)
	}
	var remoteErr error
	if path == defaultPath {
		address := os.Getenv("EF_QUACK_ADDR")
		if address == "" {
			address = "127.0.0.1:9494"
		}
		if quackReachable(address) {
			remote, err := OpenRemote(address, store.LocalQuackToken, false)
			if err == nil {
				return remote, nil
			}
			remoteErr = err
		}
	}
	st, err := store.OpenExisting(path)
	if err != nil {
		if remoteErr != nil {
			return nil, errors.Join(remoteErr, err)
		}
		return nil, err
	}
	return &Reporter{store: st, owned: true}, nil
}

// OpenRemote opens a Quack-backed reporter directly.
func OpenRemote(address, token string, insecure bool) (*Reporter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.OpenRemote(ctx, address, token, insecure)
	if err != nil {
		return nil, err
	}
	return &Reporter{store: st, owned: true}, nil
}

// New builds a Reporter over an already-open in-process Store.
func New(st *store.Store) *Reporter { return &Reporter{store: st} }

// Close closes an owned Store and is a no-op for New reporters.
func (r *Reporter) Close() error {
	if !r.owned {
		return nil
	}
	return r.store.Close()
}

// SummaryOptions controls a usage aggregate query.
type SummaryOptions struct {
	Since              time.Time
	Until              time.Time
	GroupBy            string
	Directory          string
	Branch             string
	KnownProvidersOnly bool
}

// SummaryRow is one aggregate row from the usage ledger.
type SummaryRow struct {
	Day        string
	Provider   string
	Client     string
	Model      string
	Source     string
	Directory  string
	GitBranch  string
	Requests   int64
	Errors     int64
	Input      int64
	FreshInput int64
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
	Source            string
	Host              string
	StartedAt         time.Time
	CompletedAt       sql.NullTime
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
	Directory         string
	GitBranch         string
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
	WebRequests       []WebRequest
}

type ToolCall struct {
	Ordinal       int
	ID            string
	Name          string
	Command       string
	Description   string
	ArgumentsJSON string
}

// WebRequest is one provider-reported external web request associated with an
// inspected model request.
type WebRequest struct {
	Ordinal       int
	ID            string
	Name          string
	Query         string
	URL           string
	Domain        string
	ArgumentsJSON string
}

// ToolCallRow is a tool invocation with the request metadata needed by the
// dashboard's recent-tool table.
type ToolCallRow struct {
	RequestID   string
	Source      string
	StartedAt   time.Time
	Provider    string
	Client      string
	Model       string
	Ordinal     int
	ID          string
	Name        string
	Command     string
	Description string
}

// WebRequestRow is a web request with parent request metadata for reporting
// and the dashboard.
type WebRequestRow struct {
	RequestID     string
	StartedAt     time.Time
	Provider      string
	Client        string
	Model         string
	Ordinal       int
	ID            string
	Name          string
	Query         string
	URL           string
	Domain        string
	ArgumentsJSON string
}

// ToolCallOptions controls a tool-call query. Since is inclusive and Until is
// exclusive, matching the usage-reporting date range semantics.
type ToolCallOptions struct {
	Since time.Time
	Until time.Time
	Limit int
}

// Summary returns live usage totals for the selected interval.
func (r *Reporter) Summary(ctx context.Context, opts SummaryOptions) ([]SummaryRow, error) {
	groupBy := opts.GroupBy
	if groupBy == "" {
		groupBy = "model"
	}
	selectGroup, groupExpr, orderBy, err := summaryGrouping(groupBy)
	if err != nil {
		return nil, err
	}
	where, args := timeRange("started_at", opts.Since, opts.Until)
	if opts.Directory != "" {
		where = appendWherePredicate(where, "directory = ?")
		args = append(args, opts.Directory)
	}
	if opts.Branch != "" {
		where = appendWherePredicate(where, "git_branch = ?")
		args = append(args, opts.Branch)
	}
	if opts.KnownProvidersOnly {
		predicate := providerSQL("path") + " != 'unknown'"
		where = appendWherePredicate(where, predicate)
	}
	return r.scanSummary(ctx, buildSummaryQuery(selectGroup, where, groupExpr, orderBy), args...)
}

// HistoricalByModel computes all-time model totals directly from requests.
func (r *Reporter) HistoricalByModel(ctx context.Context) ([]SummaryRow, error) {
	selectGroup, groupExpr, _, _ := summaryGrouping("model")
	return r.scanSummary(ctx, buildSummaryQuery(selectGroup, "", groupExpr, "total_tokens DESC, provider, client, model"))
}

// HistoricalByDay computes all-time daily totals directly from requests.
func (r *Reporter) HistoricalByDay(ctx context.Context) ([]SummaryRow, error) {
	selectGroup, groupExpr, _, _ := summaryGrouping("day")
	return r.scanSummary(ctx, buildSummaryQuery(selectGroup, "", groupExpr, "day DESC, total_tokens DESC, provider, client, model"))
}

func (r *Reporter) scanSummary(ctx context.Context, query string, args ...any) ([]SummaryRow, error) {
	rows, err := r.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query summary: %w", err)
	}
	defer rows.Close()
	var out []SummaryRow
	for rows.Next() {
		var row SummaryRow
		if err := rows.Scan(&row.Day, &row.Provider, &row.Client, &row.Model, &row.Source, &row.Directory, &row.GitBranch,
			&row.Requests, &row.Errors, &row.Input, &row.FreshInput, &row.Cached, &row.CacheWrite,
			&row.Output, &row.Reasoning, &row.Total); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate summary: %w", err)
	}
	return out, nil
}

const DefaultInspectLimit = 20
const DefaultRecentToolCallLimit = 50

func (r *Reporter) Inspect(ctx context.Context, id string, limit int) ([]InspectRow, error) {
	if limit <= 0 {
		limit = DefaultInspectLimit
	}
	return r.queryInspectRows(ctx, "WHERE id = ? OR response_id = ?", "started_at DESC", limit, id, id)
}

func (r *Reporter) RecentErrors(ctx context.Context, limit int) ([]InspectRow, error) {
	return r.queryInspectRows(ctx, "WHERE error_type IS NOT NULL", "started_at DESC", limit)
}

func (r *Reporter) RecentErrorsWithin(ctx context.Context, since, until time.Time, limit int) ([]InspectRow, error) {
	where, args := timeRange("started_at", since, until)
	if where == "" {
		where = "WHERE error_type IS NOT NULL"
	} else {
		where += " AND error_type IS NOT NULL"
	}
	return r.queryInspectRows(ctx, where, "started_at DESC", limit, args...)
}

func (r *Reporter) RecentToolCalls(ctx context.Context, limit int) ([]ToolCallRow, error) {
	return r.ToolCalls(ctx, ToolCallOptions{Limit: limit})
}

// ToolCalls reports the most recent tool calls across requests. It deliberately
// avoids a SQL JOIN between requests and tool_calls: a Quack-attached remote
// catalog rejects queries that stream-scan two tables at once ("Multiple
// streaming scans... not currently supported"), which a JOIN across those
// tables triggers. Instead it fetches candidate requests and their tool calls
// as two independent single-table scans and joins/orders/limits in Go,
// expanding the request window until enough tool calls are gathered or the
// requests are exhausted.
func (r *Reporter) ToolCalls(ctx context.Context, opts ToolCallOptions) ([]ToolCallRow, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRecentToolCallLimit
	}
	where, args := timeRange("started_at", opts.Since, opts.Until)

	const maxRequestWindow = 1 << 16
	requestWindow := limit
	var requests []toolCallRequest
	for {
		var err error
		requests, err = r.candidateRequestsForToolCalls(ctx, where, args, requestWindow)
		if err != nil {
			return nil, err
		}
		out, err := r.joinRequestsToToolCalls(ctx, requests, limit)
		if err != nil {
			return nil, err
		}
		if len(out) >= limit || len(requests) < requestWindow || requestWindow >= maxRequestWindow {
			return out, nil
		}
		requestWindow *= 2
	}
}

type toolCallRequest struct {
	ID        string
	Source    string
	StartedAt time.Time
	Provider  string
	Client    string
	Model     string
}

func (r *Reporter) candidateRequestsForToolCalls(ctx context.Context, where string, whereArgs []any, limit int) ([]toolCallRequest, error) {
	args := append(slices.Clone(whereArgs), limit)
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT id, COALESCE(source, 'unknown'), started_at, `+providerSQL("path")+`, `+clientSQL()+`,
  COALESCE(model_reported, model_requested, 'unknown')
FROM requests
`+where+`
ORDER BY started_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("query candidate requests for tool calls: %w", err)
	}
	defer rows.Close()
	out := make([]toolCallRequest, 0, limit)
	for rows.Next() {
		var req toolCallRequest
		if err := rows.Scan(&req.ID, &req.Source, &req.StartedAt, &req.Provider, &req.Client, &req.Model); err != nil {
			return nil, fmt.Errorf("scan candidate request for tool calls: %w", err)
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate candidate requests for tool calls: %w", err)
	}
	return out, nil
}

func (r *Reporter) joinRequestsToToolCalls(ctx context.Context, requests []toolCallRequest, limit int) ([]ToolCallRow, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	byID := make(map[string]toolCallRequest, len(requests))
	ids := make([]any, len(requests))
	placeholders := make([]string, len(requests))
	for i, req := range requests {
		byID[req.ID] = req
		ids[i] = req.ID
		placeholders[i] = "?"
	}
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT request_id, ordinal, COALESCE(tool_call_id, ''), name, COALESCE(command, ''), COALESCE(description, '')
FROM tool_calls WHERE request_id IN (`+strings.Join(placeholders, ", ")+`)`, ids...)
	if err != nil {
		return nil, fmt.Errorf("query tool calls for candidate requests: %w", err)
	}
	defer rows.Close()
	byRequest := make(map[string][]ToolCallRow, len(requests))
	for rows.Next() {
		var (
			requestID string
			row       ToolCallRow
		)
		if err := rows.Scan(&requestID, &row.Ordinal, &row.ID, &row.Name, &row.Command, &row.Description); err != nil {
			return nil, fmt.Errorf("scan tool call for candidate requests: %w", err)
		}
		req := byID[requestID]
		row.RequestID = requestID
		row.Source = req.Source
		row.StartedAt = req.StartedAt
		row.Provider = req.Provider
		row.Client = req.Client
		row.Model = req.Model
		byRequest[requestID] = append(byRequest[requestID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool calls for candidate requests: %w", err)
	}

	out := make([]ToolCallRow, 0, limit)
	for _, req := range requests {
		calls := byRequest[req.ID]
		sort.Slice(calls, func(i, j int) bool { return calls[i].Ordinal < calls[j].Ordinal })
		out = append(out, calls...)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// WebRequests returns provider-reported web activity within the requested
// interval, newest request first and event order preserved within a request.
func (r *Reporter) WebRequests(ctx context.Context, opts ToolCallOptions) ([]WebRequestRow, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRecentToolCallLimit
	}
	where, args := timeRange("started_at", opts.Since, opts.Until)

	// Quack cannot stream-scan requests and web_requests in one query, so use
	// the same expanding two-scan strategy as ToolCalls and join the rows in Go.
	const maxRequestWindow = 1 << 16
	requestWindow := limit
	for {
		requests, err := r.candidateRequestsForToolCalls(ctx, where, args, requestWindow)
		if err != nil {
			return nil, err
		}
		out, err := r.joinRequestsToWebRequests(ctx, requests, limit)
		if err != nil {
			return nil, err
		}
		if len(out) >= limit || len(requests) < requestWindow || requestWindow >= maxRequestWindow {
			return out, nil
		}
		requestWindow *= 2
	}
}

func (r *Reporter) joinRequestsToWebRequests(ctx context.Context, requests []toolCallRequest, limit int) ([]WebRequestRow, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	byID := make(map[string]toolCallRequest, len(requests))
	ids := make([]any, len(requests))
	placeholders := make([]string, len(requests))
	for i, req := range requests {
		byID[req.ID] = req
		ids[i] = req.ID
		placeholders[i] = "?"
	}
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT request_id, ordinal, COALESCE(web_request_id, ''), name,
  COALESCE(query, ''), COALESCE(url, ''), COALESCE(domain, ''), COALESCE(arguments_json, '')
FROM web_requests WHERE request_id IN (`+strings.Join(placeholders, ", ")+`)`, ids...)
	if err != nil {
		return nil, fmt.Errorf("query web requests for candidate requests: %w", err)
	}
	defer rows.Close()
	byRequest := make(map[string][]WebRequestRow, len(requests))
	for rows.Next() {
		var (
			requestID string
			row       WebRequestRow
		)
		if err := rows.Scan(&requestID, &row.Ordinal, &row.ID, &row.Name, &row.Query, &row.URL, &row.Domain, &row.ArgumentsJSON); err != nil {
			return nil, fmt.Errorf("scan web request for candidate requests: %w", err)
		}
		if !queue.IsWebToolName(row.Name) {
			continue
		}
		req := byID[requestID]
		row.RequestID = requestID
		row.StartedAt = req.StartedAt
		row.Provider = req.Provider
		row.Client = req.Client
		row.Model = req.Model
		byRequest[requestID] = append(byRequest[requestID], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web requests for candidate requests: %w", err)
	}

	out := make([]WebRequestRow, 0, limit)
	for _, req := range requests {
		webRequests := byRequest[req.ID]
		sort.Slice(webRequests, func(i, j int) bool { return webRequests[i].Ordinal < webRequests[j].Ordinal })
		out = append(out, webRequests...)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Reporter) queryInspectRows(ctx context.Context, whereClause, orderBy string, limit int, args ...any) ([]InspectRow, error) {
	rows, err := r.store.DB().QueryContext(ctx, buildInspectQuery(whereClause, orderBy), append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query inspect: %w", err)
	}
	defer rows.Close()
	var out []InspectRow
	for rows.Next() {
		var row InspectRow
		if err := rows.Scan(
			&row.ID, &row.ResponseID, &row.Source, &row.Host, &row.StartedAt, &row.CompletedAt,
			&row.DurationMS, &row.Method, &row.Path, &row.Provider, &row.UpstreamURL,
			&row.ModelRequested, &row.ModelReported, &row.Stream, &row.HTTPStatus,
			&row.UpstreamRequestID, &row.UserAgent, &row.Originator, &row.Client, &row.CodexSessionID,
			&row.Directory, &row.GitBranch,
			&row.ErrorType, &row.ErrorMessage, &row.Input, &row.Cached, &row.CacheWrite,
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
		requests, err := r.webRequests(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].WebRequests = requests
	}
	return out, nil
}

func (r *Reporter) toolCalls(ctx context.Context, requestID string) ([]ToolCall, error) {
	rows, err := r.store.DB().QueryContext(ctx, `SELECT ordinal, COALESCE(tool_call_id, ''), name,
 COALESCE(command, ''), COALESCE(description, ''), COALESCE(arguments_json, '')
FROM tool_calls WHERE request_id = ? ORDER BY ordinal`, requestID)
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
	return out, rows.Err()
}

func (r *Reporter) webRequests(ctx context.Context, requestID string) ([]WebRequest, error) {
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT ordinal, COALESCE(web_request_id, ''), name,
  COALESCE(query, ''), COALESCE(url, ''), COALESCE(domain, ''), COALESCE(arguments_json, '')
FROM web_requests
WHERE request_id = ?
ORDER BY ordinal`, requestID)
	if err != nil {
		return nil, fmt.Errorf("query web requests for request %s: %w", requestID, err)
	}
	defer rows.Close()

	var out []WebRequest
	for rows.Next() {
		var request WebRequest
		if err := rows.Scan(&request.Ordinal, &request.ID, &request.Name, &request.Query,
			&request.URL, &request.Domain, &request.ArgumentsJSON); err != nil {
			return nil, fmt.Errorf("scan web request for request %s: %w", requestID, err)
		}
		if !queue.IsWebToolName(request.Name) {
			continue
		}
		out = append(out, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web requests for request %s: %w", requestID, err)
	}
	return out, nil
}

func summaryGrouping(groupBy string) (selectGroup, groupExpr, orderBy string, err error) {
	dayExpr := "CAST(CAST(date_trunc('day', started_at) AS DATE) AS VARCHAR)"
	modelExpr := "COALESCE(model_reported, model_requested, 'unknown')"
	providerExpr := providerSQL("path")
	clientExpr := clientSQL()
	switch groupBy {
	case "model":
		return groupSelect("''", providerExpr, clientExpr, modelExpr, "''", "''", "''"),
			joinSQLExprs(providerExpr, clientExpr, modelExpr), "provider, client, model", nil
	case "provider":
		return groupSelect("''", providerExpr, "''", "''", "''", "''", "''"), providerExpr, "provider", nil
	case "day":
		return groupSelect(dayExpr, providerExpr, clientExpr, modelExpr, "''", "''", "''"),
			joinSQLExprs(dayExpr, providerExpr, clientExpr, modelExpr), "day, provider, client, model", nil
	case "source":
		sourceExpr := "COALESCE(source, 'unknown')"
		return groupSelect("''", "''", "''", "''", sourceExpr, "''", "''"), sourceExpr, "source", nil
	case "directory":
		directoryExpr := "COALESCE(directory, 'unknown')"
		return groupSelect("''", "''", "''", "''", "''", directoryExpr, "''"), directoryExpr, "directory", nil
	case "git_branch":
		branchExpr := "COALESCE(git_branch, 'unknown')"
		return groupSelect("''", "''", "''", "''", "''", "''", branchExpr), branchExpr, "git_branch", nil
	default:
		return "", "", "", fmt.Errorf("unsupported group-by %q (want model, provider, day, source, directory, or git_branch)", groupBy)
	}
}

func buildSummaryQuery(selectGroup, whereSQL, groupExpr, orderBy string) string {
	return `SELECT ` + selectGroup + `,
 COUNT(*) AS requests,
 SUM(CASE WHEN error_type IS NULL THEN 0 ELSE 1 END) AS errors,
	SUM(COALESCE(input_tokens, 0)) AS input_tokens,
 SUM(GREATEST(
   COALESCE(input_tokens, 0) - COALESCE(cached_input_tokens, 0) -
   CASE WHEN ` + providerSQL("path") + ` = 'anthropic' THEN COALESCE(cache_write_tokens, 0) ELSE 0 END,
   0
 )) AS fresh_input_tokens,
 SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
 SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
 SUM(COALESCE(output_tokens, 0)) AS output_tokens,
 SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
 SUM(COALESCE(total_tokens, 0)) AS total_tokens
FROM requests
` + whereSQL + `
GROUP BY ` + groupExpr + `
ORDER BY ` + orderBy
}

func buildInspectQuery(whereClause, orderBy string) string {
	return `SELECT id, COALESCE(response_id, ''), COALESCE(source, 'unknown'), COALESCE(host, ''),
 started_at, completed_at, duration_ms, method, path, ` + providerSQL("path") + `, upstream_url,
 COALESCE(model_requested, ''), COALESCE(model_reported, ''), stream, http_status,
 COALESCE(upstream_request_id, ''), COALESCE(user_agent, ''), COALESCE(originator, ''), ` + clientSQL() + `,
 COALESCE(codex_session_id, ''), COALESCE(directory, ''), COALESCE(git_branch, ''),
 COALESCE(error_type, ''), COALESCE(error_message, ''),
 input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, reasoning_tokens, total_tokens,
 COALESCE(usage_json, '')
FROM requests
` + whereClause + `
ORDER BY ` + orderBy + ` LIMIT ?`
}

func groupSelect(day, provider, client, model, source, directory, gitBranch string) string {
	return day + " AS day, " + provider + " AS provider, " + client + " AS client, " +
		model + " AS model, " + source + " AS source, " + directory + " AS directory, " +
		gitBranch + " AS git_branch"
}

func joinSQLExprs(exprs ...string) string { return strings.Join(exprs, ", ") }

func timeRange(column string, since, until time.Time) (string, []any) {
	var where []string
	var args []any
	if !since.IsZero() {
		where = append(where, column+" >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		where = append(where, column+" < ?")
		args = append(args, until)
	}
	if len(where) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

func appendWherePredicate(where, predicate string) string {
	if where == "" {
		return "WHERE " + predicate
	}
	return where + " AND " + predicate
}

func quackReachable(address string) bool {
	hostPort := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "quack://"), "quack:")
	if !strings.Contains(hostPort, ":") {
		hostPort += ":9494"
	}
	conn, err := net.DialTimeout("tcp", hostPort, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func providerSQL(column string) string { return store.ProviderSQL(column) }
func clientSQL() string                { return store.ClientSQL() }

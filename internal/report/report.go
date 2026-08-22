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

	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// Reporter owns a Store only when created by an Open* function.
type Reporter struct {
	store *store.Store
	owned bool
}

// OpenWithHubKey first tries the configured daemon/hub through Quack, then
// falls back to a read-only file open when a standalone daemon is not running.
// A configured hub never falls back to a stale local ledger. Quack is only
// consulted when path equals defaultPath; an explicit --db override naming a
// different file always reads that file directly.
func OpenWithHubKey(path, defaultPath, hubKey string) (*Reporter, error) {
	if hub := os.Getenv("EF_HUB_ADDR"); hub != "" {
		insecure := false
		if raw := os.Getenv("EF_HUB_INSECURE"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("EF_HUB_INSECURE: %w", err)
			}
			insecure = parsed
		}
		return OpenHubRemote(hub, hubKey, insecure)
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

// OpenHubRemote authenticates to a remote EF gateway and attaches Quack using
// its short-lived session credential. Hub tokens are deliberately not accepted
// as client credentials.
func OpenHubRemote(address, keyPath string, insecure bool) (*Reporter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := hubauth.NewClient(hubauth.ClientConfig{Address: address, KeyPath: keyPath, Insecure: insecure})
	token, err := auth.Credential(ctx)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenRemote(ctx, address, token, insecure)
	if err == nil {
		return &Reporter{store: st, owned: true}, nil
	}
	// A restarted hub forgets all in-memory sessions. Discard a cached
	// credential and retry the challenge-response flow once.
	auth.Invalidate()
	token, loginErr := auth.Credential(ctx)
	if loginErr != nil {
		return nil, err
	}
	st, retryErr := store.OpenRemote(ctx, address, token, insecure)
	if retryErr != nil {
		return nil, retryErr
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

// HourlyTokenOptions controls an hourly token-activity query. Since is
// inclusive and Until is exclusive.
type HourlyTokenOptions struct {
	Since              time.Time
	Until              time.Time
	KnownProvidersOnly bool
}

// HourlyTokenRow is one hour of fresh-input and output token activity.
type HourlyTokenRow struct {
	Hour       time.Time
	FreshInput int64
	Output     int64
}

// KPIOptions controls a dashboard KPI query. Since is inclusive and Until is
// exclusive, matching the usage-reporting date range semantics.
type KPIOptions struct {
	Since              time.Time
	Until              time.Time
	KnownProvidersOnly bool
}

// AnthropicKPIStats contains provider-specific usage details that Anthropic
// currently reports only in the preserved usage JSON.
type AnthropicKPIStats struct {
	Requests                     int64
	OutputReportedRequests       int64
	ThinkingReportedRequests     int64
	ThinkingTokens               int64
	OutputTokens                 int64
	CacheWriteReportedRequests   int64
	CacheWriteTokens             int64
	CacheWrite5MTokens           int64
	CacheWrite1HTokens           int64
	CacheWriteUnclassifiedTokens int64
}

// KPIStats contains the lossless counts and latency values used to render the
// dashboard's operational KPI cards. Rates are deliberately left to callers
// so their numerators and denominators remain available for display.
type KPIStats struct {
	Requests              int64
	Errors                int64
	LatencyP50MS          *float64
	LatencyP95MS          *float64
	CacheEligibleRequests int64
	CacheHitRequests      int64
	PeakConcurrency       int64
	Anthropic             AnthropicKPIStats
}

// SessionOptions controls session lifecycle aggregation. Since is inclusive
// and Until is exclusive.
type SessionOptions struct {
	Since   time.Time
	Until   time.Time
	GroupBy string
}

// SessionRow contains distinct session lifecycle counts for one provider and
// local calendar period.
type SessionRow struct {
	Period   string
	Provider string
	Started  int64
	Used     int64
}

// RecentRequestRow is the lightweight request metadata used by the inspect
// command's recent-request picker.
type RecentRequestRow struct {
	ID         string
	ResponseID string
	StartedAt  time.Time
	Provider   string
	Client     string
	Model      string
	HTTPStatus sql.NullInt64
	Method     string
	Path       string
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
	SessionID         string
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
	// CommandsOnly excludes tool calls whose command is empty.
	CommandsOnly bool
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

// HourlyTokens returns zero-filled hourly token totals for the selected
// interval. Fresh input excludes cached reads and Anthropic cache writes.
func (r *Reporter) HourlyTokens(ctx context.Context, opts HourlyTokenOptions) ([]HourlyTokenRow, error) {
	if opts.Since.IsZero() || opts.Until.IsZero() {
		return nil, errors.New("hourly token range requires since and until")
	}
	if !opts.Since.Before(opts.Until) {
		return nil, errors.New("hourly token range must have since before until")
	}

	where, args := timeRange("started_at", opts.Since, opts.Until)
	if opts.KnownProvidersOnly {
		where = appendWherePredicate(where, providerSQL("path")+" != 'unknown'")
	}
	rows, err := r.store.DB().QueryContext(ctx, `SELECT
  date_trunc('hour', started_at) AS hour,
  SUM(`+freshInputSQL()+`) AS fresh_input_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens
FROM requests
`+where+`
GROUP BY hour
ORDER BY hour`, args...)
	if err != nil {
		return nil, fmt.Errorf("query hourly tokens: %w", err)
	}
	defer rows.Close()

	aggregated := make(map[int64]HourlyTokenRow)
	for rows.Next() {
		var row HourlyTokenRow
		if err := rows.Scan(&row.Hour, &row.FreshInput, &row.Output); err != nil {
			return nil, fmt.Errorf("scan hourly tokens: %w", err)
		}
		aggregated[row.Hour.Unix()] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hourly tokens: %w", err)
	}

	start := beginningOfHour(opts.Since)
	bucketCount := int(opts.Until.Sub(start)/time.Hour) + 1
	out := make([]HourlyTokenRow, 0, bucketCount)
	for hour := start; hour.Before(opts.Until); hour = hour.Add(time.Hour) {
		row := aggregated[hour.Unix()]
		row.Hour = hour
		out = append(out, row)
	}
	return out, nil
}

// KPIs returns operational request metrics for the selected interval. The
// aggregate and concurrency inputs are fetched in separate single-table scans
// so the method also works through a Quack-attached remote catalog.
func (r *Reporter) KPIs(ctx context.Context, opts KPIOptions) (KPIStats, error) {
	where, args := timeRange("started_at", opts.Since, opts.Until)
	if opts.KnownProvidersOnly {
		where = appendWherePredicate(where, providerSQL("path")+" != 'unknown'")
	}

	var (
		failure   = "(COALESCE(error_type, '') != '' OR COALESCE(http_status >= 400, false))"
		anthropic = "(" + providerSQL("path") + " = 'anthropic')"
		thinking  = "TRY_CAST(json_extract(TRY_CAST(usage_json AS JSON), '$.output_tokens_details.thinking_tokens') AS BIGINT)"
		cache5M   = "TRY_CAST(json_extract(TRY_CAST(usage_json AS JSON), '$.cache_creation.ephemeral_5m_input_tokens') AS BIGINT)"
		cache1H   = "TRY_CAST(json_extract(TRY_CAST(usage_json AS JSON), '$.cache_creation.ephemeral_1h_input_tokens') AS BIGINT)"
		cacheTTL  = "(COALESCE(" + cache5M + ", 0) + COALESCE(" + cache1H + ", 0))"

		stats KPIStats
		p50   sql.NullFloat64
		p95   sql.NullFloat64
	)
	err := r.store.DB().QueryRowContext(ctx, `SELECT
  COUNT(*) AS requests,
  COALESCE(COUNT_IF(`+failure+`), 0) AS errors,
  quantile_cont(duration_ms, 0.5) FILTER (
    WHERE duration_ms IS NOT NULL AND NOT `+failure+`
  ) AS latency_p50_ms,
  quantile_cont(duration_ms, 0.95) FILTER (
    WHERE duration_ms IS NOT NULL AND NOT `+failure+`
  ) AS latency_p95_ms,
  COALESCE(COUNT_IF(input_tokens IS NOT NULL), 0) AS cache_eligible_requests,
  COALESCE(COUNT_IF(input_tokens IS NOT NULL AND COALESCE(cached_input_tokens, 0) > 0), 0) AS cache_hit_requests,
  COALESCE(COUNT_IF(`+anthropic+`), 0) AS anthropic_requests,
  COALESCE(COUNT_IF(`+anthropic+` AND output_tokens IS NOT NULL), 0) AS anthropic_output_reported_requests,
  COALESCE(COUNT_IF(`+anthropic+` AND `+thinking+` IS NOT NULL), 0) AS anthropic_thinking_reported_requests,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN COALESCE(`+thinking+`, 0) ELSE 0 END), 0) AS anthropic_thinking_tokens,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN COALESCE(output_tokens, 0) ELSE 0 END), 0) AS anthropic_output_tokens,
  COALESCE(COUNT_IF(`+anthropic+` AND (cache_write_tokens IS NOT NULL OR `+cache5M+` IS NOT NULL OR `+cache1H+` IS NOT NULL)), 0) AS anthropic_cache_write_reported_requests,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN GREATEST(COALESCE(cache_write_tokens, 0), `+cacheTTL+`) ELSE 0 END), 0) AS anthropic_cache_write_tokens,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN COALESCE(`+cache5M+`, 0) ELSE 0 END), 0) AS anthropic_cache_write_5m_tokens,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN COALESCE(`+cache1H+`, 0) ELSE 0 END), 0) AS anthropic_cache_write_1h_tokens,
  COALESCE(SUM(CASE WHEN `+anthropic+` THEN GREATEST(COALESCE(cache_write_tokens, 0) - `+cacheTTL+`, 0) ELSE 0 END), 0) AS anthropic_cache_write_unclassified_tokens
FROM requests
`+where, args...).Scan(
		&stats.Requests, &stats.Errors, &p50, &p95,
		&stats.CacheEligibleRequests, &stats.CacheHitRequests,
		&stats.Anthropic.Requests, &stats.Anthropic.OutputReportedRequests,
		&stats.Anthropic.ThinkingReportedRequests, &stats.Anthropic.ThinkingTokens,
		&stats.Anthropic.OutputTokens, &stats.Anthropic.CacheWriteReportedRequests,
		&stats.Anthropic.CacheWriteTokens, &stats.Anthropic.CacheWrite5MTokens,
		&stats.Anthropic.CacheWrite1HTokens, &stats.Anthropic.CacheWriteUnclassifiedTokens,
	)
	if err != nil {
		return KPIStats{}, fmt.Errorf("query KPIs: %w", err)
	}
	if p50.Valid {
		stats.LatencyP50MS = &p50.Float64
	}
	if p95.Valid {
		stats.LatencyP95MS = &p95.Float64
	}

	peak, err := r.peakConcurrency(ctx, opts)
	if err != nil {
		return KPIStats{}, err
	}
	stats.PeakConcurrency = peak
	return stats, nil
}

// Sessions returns distinct session starts and activity grouped by local
// calendar day or Monday-based calendar week. Starts come from the persistent
// registry while usage comes from retained request rows.
func (r *Reporter) Sessions(ctx context.Context, opts SessionOptions) ([]SessionRow, error) {
	groupBy := opts.GroupBy
	if groupBy == "" {
		groupBy = "day"
	}
	if groupBy != "day" && groupBy != "week" {
		return nil, fmt.Errorf("unsupported session group-by %q (want day or week)", groupBy)
	}

	var (
		periodFromFirstSeen = "CAST(CAST(date_trunc('" + groupBy + "', first_seen_at) AS DATE) AS VARCHAR)"
		periodFromRequest   = "CAST(CAST(date_trunc('" + groupBy + "', started_at) AS DATE) AS VARCHAR)"
		rowsByKey           = make(map[string]SessionRow)
	)
	schema, err := r.sessionSchema(ctx)
	if err != nil {
		return nil, err
	}
	if schema.sessionExpression == "" {
		return []SessionRow{}, nil
	}
	startedWhere, startedArgs := timeRange("first_seen_at", opts.Since, opts.Until)
	startedQuery := `SELECT ` + periodFromFirstSeen + ` AS period, provider, COUNT(*)
FROM sessions
` + startedWhere + `
GROUP BY period, provider`
	if !schema.hasRegistry {
		startedQuery = `SELECT ` + periodFromFirstSeen + ` AS period, provider, COUNT(*)
FROM (
  SELECT provider, session_id, MIN(started_at) AS first_seen_at
  FROM (
    SELECT ` + providerSQL("path") + ` AS provider, ` + schema.sessionExpression + ` AS session_id, started_at
    FROM requests
  ) observations
  WHERE session_id IS NOT NULL AND session_id != '' AND provider != 'unknown'
  GROUP BY provider, session_id
) legacy_sessions
` + startedWhere + `
GROUP BY period, provider`
	}
	startedRows, err := r.store.DB().QueryContext(ctx, startedQuery, startedArgs...)
	if err != nil {
		return nil, fmt.Errorf("query sessions started: %w", err)
	}
	for startedRows.Next() {
		var row SessionRow
		if err := startedRows.Scan(&row.Period, &row.Provider, &row.Started); err != nil {
			_ = startedRows.Close()
			return nil, fmt.Errorf("scan sessions started: %w", err)
		}
		rowsByKey[sessionRowKey(row.Period, row.Provider)] = row
	}
	if err := startedRows.Err(); err != nil {
		_ = startedRows.Close()
		return nil, fmt.Errorf("iterate sessions started: %w", err)
	}
	if err := startedRows.Close(); err != nil {
		return nil, fmt.Errorf("close sessions started: %w", err)
	}

	usedWhere, usedArgs := timeRange("started_at", opts.Since, opts.Until)
	usedWhere = appendWherePredicate(usedWhere, schema.sessionExpression+" IS NOT NULL")
	usedWhere = appendWherePredicate(usedWhere, providerSQL("path")+" != 'unknown'")
	usedRows, err := r.store.DB().QueryContext(ctx, `SELECT `+periodFromRequest+` AS period, `+providerSQL("path")+` AS provider,
  COUNT(DISTINCT `+schema.sessionExpression+`)
FROM requests
`+usedWhere+`
GROUP BY period, provider`, usedArgs...)
	if err != nil {
		return nil, fmt.Errorf("query sessions used: %w", err)
	}
	defer usedRows.Close()
	for usedRows.Next() {
		var row SessionRow
		if err := usedRows.Scan(&row.Period, &row.Provider, &row.Used); err != nil {
			return nil, fmt.Errorf("scan sessions used: %w", err)
		}
		key := sessionRowKey(row.Period, row.Provider)
		if existing, ok := rowsByKey[key]; ok {
			row.Started = existing.Started
		}
		rowsByKey[key] = row
	}
	if err := usedRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions used: %w", err)
	}

	rows := make([]SessionRow, 0, len(rowsByKey))
	for _, row := range rowsByKey {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Period != rows[j].Period {
			return rows[i].Period < rows[j].Period
		}
		return rows[i].Provider < rows[j].Provider
	})
	return rows, nil
}

func sessionRowKey(period, provider string) string { return period + "\x00" + provider }

type sessionSchema struct {
	hasRegistry       bool
	sessionExpression string
}

func (r *Reporter) sessionSchema(ctx context.Context) (sessionSchema, error) {
	rows, err := r.store.DB().QueryContext(ctx, `SELECT table_name, column_name
FROM information_schema.columns
WHERE (table_name = 'requests' AND column_name IN ('session_id', 'codex_session_id'))
   OR table_name = 'sessions'`)
	if err != nil {
		return sessionSchema{}, fmt.Errorf("inspect session schema: %w", err)
	}
	defer rows.Close()
	var hasSessionID, hasCodexSessionID bool
	schema := sessionSchema{}
	for rows.Next() {
		var tableName, columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			return sessionSchema{}, fmt.Errorf("scan session schema: %w", err)
		}
		switch {
		case tableName == "sessions":
			schema.hasRegistry = true
		case columnName == "session_id":
			hasSessionID = true
		case columnName == "codex_session_id":
			hasCodexSessionID = true
		}
	}
	if err := rows.Err(); err != nil {
		return sessionSchema{}, fmt.Errorf("iterate session schema: %w", err)
	}
	switch {
	case hasSessionID && hasCodexSessionID:
		schema.sessionExpression = "COALESCE(NULLIF(session_id, ''), NULLIF(codex_session_id, ''))"
	case hasSessionID:
		schema.sessionExpression = "NULLIF(session_id, '')"
	case hasCodexSessionID:
		schema.sessionExpression = "NULLIF(codex_session_id, '')"
	}
	return schema, nil
}

func (r *Reporter) peakConcurrency(ctx context.Context, opts KPIOptions) (int64, error) {
	where := ""
	var args []any
	if !opts.Since.IsZero() {
		where = appendWherePredicate(where, "completed_at > ?")
		args = append(args, opts.Since)
	}
	if !opts.Until.IsZero() {
		where = appendWherePredicate(where, "started_at < ?")
		args = append(args, opts.Until)
	}
	where = appendWherePredicate(where, "completed_at IS NOT NULL")
	if opts.KnownProvidersOnly {
		where = appendWherePredicate(where, providerSQL("path")+" != 'unknown'")
	}

	rows, err := r.store.DB().QueryContext(ctx, `SELECT started_at, completed_at
FROM requests
`+where, args...)
	if err != nil {
		return 0, fmt.Errorf("query request intervals for peak concurrency: %w", err)
	}
	defer rows.Close()

	deltas := make(map[time.Time]int64)
	for rows.Next() {
		var start, end time.Time
		if err := rows.Scan(&start, &end); err != nil {
			return 0, fmt.Errorf("scan request interval for peak concurrency: %w", err)
		}
		if !opts.Since.IsZero() && start.Before(opts.Since) {
			start = opts.Since
		}
		if !opts.Until.IsZero() && end.After(opts.Until) {
			end = opts.Until
		}
		if !start.Before(end) {
			continue
		}
		// TIMESTAMPTZ values can arrive with different Location pointers even
		// when they represent the same instant. Normalize before using them as
		// map keys so touching intervals share one endpoint delta.
		start = start.UTC()
		end = end.UTC()
		deltas[start]++
		deltas[end]--
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate request intervals for peak concurrency: %w", err)
	}

	times := make([]time.Time, 0, len(deltas))
	for timestamp := range deltas {
		times = append(times, timestamp)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	var active, peak int64
	for _, timestamp := range times {
		active += deltas[timestamp]
		peak = max(peak, active)
	}
	return peak, nil
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
const DefaultRecentRequestLimit = 50
const DefaultRecentToolCallLimit = 50

// RecentRequests returns lightweight metadata for the newest recorded
// requests without loading their tool calls or web requests.
func (r *Reporter) RecentRequests(ctx context.Context, limit int) ([]RecentRequestRow, error) {
	if limit <= 0 {
		limit = DefaultRecentRequestLimit
	}
	rows, err := r.store.DB().QueryContext(ctx, `
SELECT id, COALESCE(response_id, ''), started_at, `+providerSQL("path")+`, `+clientSQL()+`,
  COALESCE(model_reported, model_requested, ''), http_status, method, path
FROM requests
ORDER BY started_at DESC, id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent requests: %w", err)
	}
	defer rows.Close()

	out := make([]RecentRequestRow, 0, limit)
	for rows.Next() {
		var row RecentRequestRow
		if err := rows.Scan(&row.ID, &row.ResponseID, &row.StartedAt, &row.Provider, &row.Client,
			&row.Model, &row.HTTPStatus, &row.Method, &row.Path); err != nil {
			return nil, fmt.Errorf("scan recent request: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent requests: %w", err)
	}
	return out, nil
}

func (r *Reporter) Inspect(ctx context.Context, id string, limit int) ([]InspectRow, error) {
	if limit <= 0 {
		limit = DefaultInspectLimit
	}
	return r.queryInspectRows(ctx, "WHERE id = ? OR response_id = ?", "started_at DESC", limit, id, id)
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
		out, err := r.joinRequestsToToolCalls(ctx, requests, limit, opts.CommandsOnly)
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

func (r *Reporter) joinRequestsToToolCalls(ctx context.Context, requests []toolCallRequest, limit int, commandsOnly bool) ([]ToolCallRow, error) {
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
	query := `
SELECT request_id, ordinal, COALESCE(tool_call_id, ''), name, COALESCE(command, ''), COALESCE(description, '')
FROM tool_calls WHERE request_id IN (` + strings.Join(placeholders, ", ") + `)`
	if commandsOnly {
		query += ` AND COALESCE(command, '') <> ''`
	}
	rows, err := r.store.DB().QueryContext(ctx, query, ids...)
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
	schema, err := r.sessionSchema(ctx)
	if err != nil {
		return nil, err
	}
	sessionExpression := "''"
	if schema.sessionExpression != "" {
		sessionExpression = "COALESCE(" + schema.sessionExpression + ", '')"
	}
	rows, err := r.store.DB().QueryContext(ctx, buildInspectQuery(whereClause, orderBy, sessionExpression), append(args, limit)...)
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
			&row.UpstreamRequestID, &row.UserAgent, &row.Originator, &row.Client, &row.SessionID,
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
 SUM(` + freshInputSQL() + `) AS fresh_input_tokens,
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

func freshInputSQL() string {
	return `GREATEST(
   COALESCE(input_tokens, 0) - COALESCE(cached_input_tokens, 0) -
   CASE WHEN ` + providerSQL("path") + ` = 'anthropic' THEN COALESCE(cache_write_tokens, 0) ELSE 0 END,
   0
 )`
}

func buildInspectQuery(whereClause, orderBy, sessionExpression string) string {
	return `SELECT id, COALESCE(response_id, ''), COALESCE(source, 'unknown'), COALESCE(host, ''),
 started_at, completed_at, duration_ms, method, path, ` + providerSQL("path") + `, upstream_url,
 COALESCE(model_requested, ''), COALESCE(model_reported, ''), stream, http_status,
 COALESCE(upstream_request_id, ''), COALESCE(user_agent, ''), COALESCE(originator, ''), ` + clientSQL() + `,
 ` + sessionExpression + `, COALESCE(directory, ''), COALESCE(git_branch, ''),
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

func beginningOfHour(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, t.Hour(), 0, 0, 0, t.Location())
}

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

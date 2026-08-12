// Package store persists usage events and derived reporting aggregates to
// SQLite. Request rows are the source of truth; aggregate tables can be
// refreshed at any time by the daemon.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// Store is a SQLite-backed usage ledger.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies
// pragmas for WAL mode and a busy timeout, pins the connection pool to a
// single connection, and ensures the schema exists.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := openDB(path)
	if err != nil {
		return nil, err
	}

	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema: %w", err)
	}
	if err := validateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validate schema: %w", err)
	}

	return &Store{db: db}, nil
}

// OpenExisting opens a ledger that must already exist, for reporting commands.
// Unlike Open it neither creates the file nor runs schema DDL, so pointing a
// report at the wrong path fails loudly instead of conjuring an empty database,
// and a read-only command never takes a write lock on a live daemon's file.
//
// The connection is still read-write: SQLite cannot open a WAL database in
// mode=ro without being able to create or write the -shm file, which fails
// exactly when the daemon is not running. Not writing is enforced by this
// package exposing no writes beyond InsertBatch, which reporting never calls.
func OpenExisting(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no usage database at %s", path)
		}
		return nil, fmt.Errorf("stat usage database %s: %w", path, err)
	}

	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := validateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validate schema: %w", err)
	}
	return &Store{db: db}, nil
}

func openDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection serializes every caller through one SQLite handle,
	// avoiding cross-goroutine lock contention. Both the queue's writer
	// goroutine (InsertBatch) and the daemon's aggregate scheduler
	// (RefreshUsageAggregates) share it, so a refresh briefly blocks usage
	// ingestion for its duration — an accepted tradeoff, since the queue
	// buffers events in memory and its writes are best-effort.
	db.SetMaxOpenConns(1)
	return db, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying connection for reporting queries inside this
// module. Request-row writes still go through InsertBatch via the queue.
func (s *Store) DB() *sql.DB {
	return s.db
}

// DeleteRequestsStartedBefore removes usage rows older than cutoff.
func (s *Store) DeleteRequestsStartedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cutoffText := cutoff.Format(timeLayout)
	if _, err := tx.ExecContext(ctx, `
DELETE FROM tool_calls
WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)`, cutoffText); err != nil {
		return 0, fmt.Errorf("delete old tool calls: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM web_requests
WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)`, cutoffText); err != nil {
		return 0, fmt.Errorf("delete old web requests: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM requests WHERE started_at < ?`, cutoffText)
	if err != nil {
		return 0, fmt.Errorf("delete old requests: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit retention cleanup: %w", err)
	}
	return n, nil
}

func ensureSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  response_id TEXT,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  duration_ms INTEGER,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  upstream_url TEXT NOT NULL,
  model_requested TEXT,
  model_reported TEXT,
  stream INTEGER NOT NULL DEFAULT 0,
  http_status INTEGER,
  upstream_request_id TEXT,
  user_agent TEXT,
  originator TEXT,
  client_name TEXT,
  codex_session_id TEXT,
  error_type TEXT,
  error_message TEXT,
  input_tokens INTEGER,
  cached_input_tokens INTEGER,
  cache_write_tokens INTEGER,
  output_tokens INTEGER,
  reasoning_tokens INTEGER,
  total_tokens INTEGER,
  usage_json TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at);
CREATE INDEX IF NOT EXISTS idx_requests_model_requested ON requests(model_requested);
CREATE INDEX IF NOT EXISTS idx_requests_response_id ON requests(response_id);

CREATE TABLE IF NOT EXISTS tool_calls (
  request_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  tool_call_id TEXT,
  name TEXT NOT NULL,
  command TEXT,
  description TEXT,
  arguments_json TEXT,
  PRIMARY KEY (request_id, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_request_id ON tool_calls(request_id);

CREATE TABLE IF NOT EXISTS web_requests (
  request_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  web_request_id TEXT,
  name TEXT NOT NULL,
  query TEXT,
  url TEXT,
  domain TEXT,
  arguments_json TEXT,
  PRIMARY KEY (request_id, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_web_requests_request_id ON web_requests(request_id);

CREATE TABLE IF NOT EXISTS usage_daily_model (
  day TEXT NOT NULL,
  provider TEXT NOT NULL,
  client TEXT NOT NULL,
  model TEXT NOT NULL,
  request_count INTEGER NOT NULL,
  error_count INTEGER NOT NULL,
  input_tokens INTEGER NOT NULL,
  cached_input_tokens INTEGER NOT NULL,
  cache_write_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  reasoning_tokens INTEGER NOT NULL,
  total_tokens INTEGER NOT NULL,
  refreshed_at TEXT NOT NULL,
  PRIMARY KEY (day, provider, client, model)
);

CREATE TABLE IF NOT EXISTS usage_model_histogram (
  provider TEXT NOT NULL,
  client TEXT NOT NULL,
  model TEXT NOT NULL,
  request_count INTEGER NOT NULL,
  error_count INTEGER NOT NULL,
  input_tokens INTEGER NOT NULL,
  cached_input_tokens INTEGER NOT NULL,
  cache_write_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  reasoning_tokens INTEGER NOT NULL,
  total_tokens INTEGER NOT NULL,
  refreshed_at TEXT NOT NULL,
  PRIMARY KEY (provider, client, model)
);

CREATE VIEW IF NOT EXISTS usage_by_day_model AS
SELECT
  substr(started_at, 1, 10) AS day,
  COALESCE(model_reported, model_requested, 'unknown') AS model,
  COUNT(*) AS request_count,
  SUM(COALESCE(input_tokens, 0)) AS input_tokens,
  SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
  SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens,
  SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
  SUM(COALESCE(total_tokens, 0)) AS total_tokens
FROM requests
GROUP BY day, model;`)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if err := addColumnIfMissing(db, "requests", "originator", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "requests", "client_name", "TEXT"); err != nil {
		return err
	}
	return nil
}

func validateSchema(db *sql.DB) error {
	requiredColumns := []string{
		"id", "response_id", "started_at", "completed_at", "duration_ms",
		"method", "path", "upstream_url", "model_requested", "model_reported",
		"stream", "http_status", "upstream_request_id", "user_agent",
		"originator", "client_name", "codex_session_id", "error_type", "error_message", "input_tokens",
		"cached_input_tokens", "cache_write_tokens", "output_tokens",
		"reasoning_tokens", "total_tokens", "usage_json", "created_at",
	}
	cols, err := tableColumns(db, "requests")
	if err != nil {
		return err
	}
	for _, col := range requiredColumns {
		if !cols[col] {
			return fmt.Errorf("requests missing column %q", col)
		}
	}

	for _, name := range []string{
		"idx_requests_started_at",
		"idx_requests_model_requested",
		"idx_requests_response_id",
		"tool_calls",
		"idx_tool_calls_request_id",
		"web_requests",
		"idx_web_requests_request_id",
		"usage_by_day_model",
		"usage_daily_model",
		"usage_model_histogram",
	} {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, name).Scan(&got)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("missing schema object %q", name)
			}
			return fmt.Errorf("validate schema object %q: %w", name, err)
		}
	}

	return nil
}

// RefreshUsageAggregates refreshes materialized reporting tables used by the
// long-running daemon for historical dashboard data. It is idempotent and keeps
// request ingestion as the source of truth.
//
// The rebuild runs as one transaction: a full scan of requests, then DELETE +
// INSERT of both aggregate tables. Because the Store uses a single SQLite
// connection (see openDB), it serializes with InsertBatch and briefly pauses
// usage ingestion while it runs. Its cost therefore scales with the requests
// table, so keep the refresh interval comfortably larger than one refresh and
// prune history with retention-days.
func (s *Store) RefreshUsageAggregates(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin aggregate refresh: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_daily_model`); err != nil {
		return fmt.Errorf("clear daily usage aggregate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_model_histogram`); err != nil {
		return fmt.Errorf("clear model usage aggregate: %w", err)
	}

	refreshedAt := time.Now().Format(timeLayout)
	if _, err := tx.ExecContext(ctx, insertDailyAggregateSQL, refreshedAt); err != nil {
		return fmt.Errorf("refresh daily usage aggregate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insertModelHistogramSQL, refreshedAt); err != nil {
		return fmt.Errorf("refresh model usage aggregate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit aggregate refresh: %w", err)
	}
	return nil
}

var insertDailyAggregateSQL = `INSERT INTO usage_daily_model (
  day, provider, client, model, request_count, error_count,
  input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, refreshed_at
)
SELECT
  substr(started_at, 1, 10) AS day,
  ` + ProviderSQL("path") + ` AS provider,
  ` + ClientSQL() + ` AS client,
  COALESCE(model_reported, model_requested, 'unknown') AS model,
  COUNT(*) AS request_count,
  SUM(CASE WHEN error_type IS NULL THEN 0 ELSE 1 END) AS error_count,
  SUM(COALESCE(input_tokens, 0)) AS input_tokens,
  SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
  SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens,
  SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
  SUM(COALESCE(total_tokens, 0)) AS total_tokens,
  ? AS refreshed_at
FROM requests
GROUP BY day, provider, client, model`

var insertModelHistogramSQL = `INSERT INTO usage_model_histogram (
  provider, client, model, request_count, error_count,
  input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, refreshed_at
)
SELECT
  ` + ProviderSQL("path") + ` AS provider,
  ` + ClientSQL() + ` AS client,
  COALESCE(model_reported, model_requested, 'unknown') AS model,
  COUNT(*) AS request_count,
  SUM(CASE WHEN error_type IS NULL THEN 0 ELSE 1 END) AS error_count,
  SUM(COALESCE(input_tokens, 0)) AS input_tokens,
  SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
  SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens,
  SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
  SUM(COALESCE(total_tokens, 0)) AS total_tokens,
  ? AS refreshed_at
FROM requests
GROUP BY provider, client, model`

// ProviderSQL returns a SQL CASE expression that classifies a request's provider
// ("anthropic", "openai", or "unknown") from the given path column. It is the
// single source of truth shared by live reporting queries and the materialized
// aggregate refresh, so the two cannot drift.
func ProviderSQL(column string) string {
	return "CASE " +
		"WHEN " + column + " LIKE '%/messages%' THEN 'anthropic' " +
		"WHEN " + column + " LIKE '%/responses%' OR " + column + " LIKE '%/chat/completions%' THEN 'openai' " +
		"ELSE 'unknown' END"
}

// ClientSQL returns a SQL CASE expression that derives a display-ready client
// label from the client_name/user_agent/originator columns. Like ProviderSQL it
// is shared between live reporting and the aggregate refresh.
func ClientSQL() string {
	return "CASE " +
		"WHEN client_name IS NOT NULL AND client_name != '' THEN client_name " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%zed%' OR lower(COALESCE(originator, '')) LIKE '%zed%' THEN 'Zed' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%codex-tui%' THEN 'Codex CLI' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%codex%' OR lower(COALESCE(originator, '')) LIKE '%codex%' THEN 'Codex' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%claude-cli%' THEN 'Claude Code' " +
		"WHEN originator IS NOT NULL AND originator != '' THEN originator " +
		"WHEN user_agent IS NOT NULL AND user_agent != '' THEN user_agent " +
		"ELSE 'Unknown' END"
}

func addColumnIfMissing(db *sql.DB, table, column, columnType string) error {
	cols, err := tableColumns(db, table)
	if err != nil {
		return err
	}
	if cols[strings.ToLower(column)] {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + columnType)
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("read table info %s: %w", table, err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("scan table info %s: %w", table, err)
		}
		cols[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table info %s: %w", table, err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %q does not exist", table)
	}
	return cols, nil
}

const insertRequestSQL = `INSERT INTO requests (
	id, response_id, started_at, completed_at, duration_ms,
	method, path, upstream_url,
	model_requested, model_reported, stream, http_status,
	upstream_request_id, user_agent, originator, client_name, codex_session_id,
	error_type, error_message,
	input_tokens, cached_input_tokens, cache_write_tokens,
	output_tokens, reasoning_tokens, total_tokens, usage_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertToolCallSQL = `INSERT INTO tool_calls (
	request_id, ordinal, tool_call_id, name, command, description, arguments_json
) VALUES (?, ?, ?, ?, ?, ?, ?)`

const insertWebRequestSQL = `INSERT INTO web_requests (
	request_id, ordinal, web_request_id, name, query, url, domain, arguments_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// InsertBatch writes events in a single transaction. Correctness does not
// depend on batching: a batch of one event is just as correct as fifty.
func (s *Store) InsertBatch(ctx context.Context, events []queue.UsageEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	stmt, err := tx.PrepareContext(ctx, insertRequestSQL)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	toolStmt, err := tx.PrepareContext(ctx, insertToolCallSQL)
	if err != nil {
		return fmt.Errorf("prepare tool call insert: %w", err)
	}
	defer toolStmt.Close()

	webStmt, err := tx.PrepareContext(ctx, insertWebRequestSQL)
	if err != nil {
		return fmt.Errorf("prepare web request insert: %w", err)
	}
	defer webStmt.Close()

	for _, ev := range events {
		var completedAt any
		var durationMs any
		if !ev.CompletedAt.IsZero() {
			completedAt = ev.CompletedAt.Format(timeLayout)
			durationMs = ev.CompletedAt.Sub(ev.StartedAt).Milliseconds()
		}

		var usageJSON any
		if len(ev.UsageJSON) > 0 {
			usageJSON = string(ev.UsageJSON)
		}

		_, err := stmt.ExecContext(ctx,
			ev.RequestID, nullableString(ev.ResponseID), ev.StartedAt.Format(timeLayout), completedAt, durationMs,
			ev.Method, ev.Path, ev.UpstreamURL,
			nullableString(ev.ModelRequested), nullableString(ev.ModelReported), ev.Stream, ev.HTTPStatus,
			nullableString(ev.UpstreamRequestID), nullableString(ev.UserAgent), nullableString(ev.Originator),
			nullableString(ev.ClientName), nullableString(ev.CodexSessionID),
			nullableString(ev.ErrorType), nullableString(ev.ErrorMessage),
			ev.Usage.InputTokens, ev.Usage.CachedInputTokens, ev.Usage.CacheWriteTokens,
			ev.Usage.OutputTokens, ev.Usage.ReasoningTokens, ev.Usage.TotalTokens, usageJSON,
		)
		if err != nil {
			return fmt.Errorf("insert request %s: %w", ev.RequestID, err)
		}
		for ordinal, call := range ev.ToolCalls {
			if call.Name == "" {
				continue
			}
			if _, err := toolStmt.ExecContext(ctx,
				ev.RequestID, ordinal, nullableString(call.ID), call.Name,
				nullableString(call.Command), nullableString(call.Description), nullableString(call.ArgumentsJSON),
			); err != nil {
				return fmt.Errorf("insert tool call for request %s: %w", ev.RequestID, err)
			}
		}
		for ordinal, request := range ev.WebRequests {
			// The activity ledger records provider web-tool calls from both
			// providers (Codex's web_search_call and Claude Code's client-side
			// WebSearch/WebFetch). Generic forward-proxy traffic is not a
			// web-tool call, so IsWebToolName keeps it out.
			if !queue.IsWebToolName(request.Name) {
				continue
			}
			if _, err := webStmt.ExecContext(ctx,
				ev.RequestID, ordinal, nullableString(request.ID), request.Name,
				nullableString(request.Query), nullableString(request.URL), nullableString(request.Domain),
				nullableString(request.ArgumentsJSON),
			); err != nil {
				return fmt.Errorf("insert web request for request %s: %w", ev.RequestID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// nullableString converts an empty string to NULL so optional text columns
// don't store misleading empty-string values.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

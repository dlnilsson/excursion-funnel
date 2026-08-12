// Package store persists usage events in a DuckDB analytical ledger.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// LocalQuackToken protects the localhost-only server used by reporting
// commands. It is intentionally an application token, not a network secret.
const LocalQuackToken = "excursion-funnel-local"

// Store is a DuckDB-backed usage ledger. A remote Store is a small in-memory
// DuckDB client with a Quack catalog attached as its current database.
type Store struct {
	db            *sql.DB
	quackConn     *sql.Conn
	quackURI      string
	remoteCatalog string
}

// QuackServer describes a running Quack listener.
type QuackServer struct {
	URI   string
	URL   string
	Token string
}

// Open opens or creates a DuckDB ledger and ensures its live schema exists.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := openDuckDB(path, false)
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

// OpenExisting opens an existing DuckDB ledger read-only. DuckDB permits this
// fallback only while no other process has the file open for writing.
func OpenExisting(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no usage database at %s", path)
		}
		return nil, fmt.Errorf("stat usage database %s: %w", path, err)
	}
	db, err := openDuckDB(path, true)
	if err != nil {
		return nil, err
	}
	if err := validateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenRemote attaches a Quack server to an in-memory DuckDB instance. The
// returned Store supports the same reporting and InsertBatch paths as a local
// Store. Quack is downloaded from DuckDB's extension repository on first use.
func OpenRemote(ctx context.Context, address, token string, disableSSL bool) (*Store, error) {
	uri := QuackURI(address)
	connector, err := duckdb.NewConnector(":memory:", func(execer driver.ExecerContext) error {
		for _, query := range []string{"INSTALL quack", "LOAD quack"} {
			if _, err := execer.ExecContext(ctx, query, nil); err != nil {
				return fmt.Errorf("%s: %w", strings.ToLower(query), err)
			}
		}
		attach := "ATTACH " + quoteLiteral(uri) + " AS quack_remote (TOKEN " + quoteLiteral(token)
		if disableSSL {
			attach += ", DISABLE_SSL true"
		}
		attach += ")"
		if _, err := execer.ExecContext(ctx, attach, nil); err != nil {
			return fmt.Errorf("attach Quack ledger %s: %w", uri, err)
		}
		if _, err := execer.ExecContext(ctx, "USE quack_remote", nil); err != nil {
			return fmt.Errorf("select Quack ledger: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create Quack client: %w", err)
	}
	db := sql.OpenDB(connector)
	// The attachment is initialized once and transactions must stay on that
	// connection. Multi-writer concurrency lives at the Quack server.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to Quack ledger %s: %w", uri, err)
	}
	return &Store{db: db, remoteCatalog: "quack_remote"}, nil
}

func openDuckDB(path string, readOnly bool) (*sql.DB, error) {
	dsn := path
	if readOnly {
		dsn += "?access_mode=read_only"
	}
	connector, err := duckdb.NewConnector(dsn, func(execer driver.ExecerContext) error {
		zone := configuredTimeZone()
		if zone == "" || strings.EqualFold(zone, "Local") {
			return nil // DuckDB defaults to the operating system's local zone.
		}
		_, err := execer.ExecContext(context.Background(), "SET TimeZone = "+quoteLiteral(zone), nil)
		if err != nil {
			return fmt.Errorf("set DuckDB timezone %q: %w", zone, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("open DuckDB: %w", err)
	}
	db := sql.OpenDB(connector)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open DuckDB: %w", err)
	}
	return db, nil
}

func configuredTimeZone() string {
	if zone := os.Getenv("TZ"); zone != "" {
		return zone
	}
	return time.Local.String()
}

// StartQuack starts a server in this DuckDB instance. The connection remains
// reserved until StopQuack or Close so the serving session stays alive.
func (s *Store) StartQuack(ctx context.Context, address, token string, allowOtherHostname bool) (QuackServer, error) {
	if s.quackConn != nil {
		return QuackServer{}, errors.New("Quack server already started")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return QuackServer{}, fmt.Errorf("reserve Quack connection: %w", err)
	}
	closeOnError := func(err error) (QuackServer, error) {
		_ = conn.Close()
		return QuackServer{}, err
	}
	for _, query := range []string{"INSTALL quack", "LOAD quack"} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return closeOnError(fmt.Errorf("%s: %w (first use requires network access)", strings.ToLower(query), err))
		}
	}
	uri := QuackURI(address)
	rows, err := conn.QueryContext(ctx,
		"CALL quack_serve(?, token := ?, allow_other_hostname := ?)",
		uri, token, allowOtherHostname,
	)
	if err != nil {
		return closeOnError(fmt.Errorf("start Quack server %s: %w", uri, err))
	}
	defer rows.Close()
	var server QuackServer
	if rows.Next() {
		if err := rows.Scan(&server.URI, &server.URL, &server.Token); err != nil {
			return closeOnError(fmt.Errorf("read Quack server result: %w", err))
		}
	}
	if err := rows.Err(); err != nil {
		return closeOnError(fmt.Errorf("start Quack server: %w", err))
	}
	server.URI = firstNonEmpty(server.URI, uri)
	server.Token = firstNonEmpty(server.Token, token)
	s.quackConn = conn
	s.quackURI = uri
	return server, nil
}

// StopQuack stops the listener, if one is running.
func (s *Store) StopQuack(ctx context.Context) error {
	if s.quackConn == nil {
		return nil
	}
	_, err := s.quackConn.ExecContext(ctx, "CALL quack_stop(?)", s.quackURI)
	closeErr := s.quackConn.Close()
	s.quackConn = nil
	s.quackURI = ""
	if err != nil {
		return fmt.Errorf("stop Quack server: %w", err)
	}
	return closeErr
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s.quackConn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.StopQuack(ctx)
		cancel()
	}
	return s.db.Close()
}

// DB exposes the underlying connection for reporting queries.
func (s *Store) DB() *sql.DB { return s.db }

// DeleteRequestsStartedBefore removes usage rows older than cutoff.
func (s *Store) DeleteRequestsStartedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tool_calls
WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)`, cutoff); err != nil {
		return 0, fmt.Errorf("delete old tool calls: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM web_requests
WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)`, cutoff); err != nil {
		return 0, fmt.Errorf("delete old web requests: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM requests WHERE started_at < ?`, cutoff)
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
  id VARCHAR PRIMARY KEY,
  response_id VARCHAR,
  source VARCHAR NOT NULL DEFAULT 'unknown',
  host VARCHAR,
  started_at TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ,
  duration_ms BIGINT,
  method VARCHAR NOT NULL,
  path VARCHAR NOT NULL,
  upstream_url VARCHAR NOT NULL,
  model_requested VARCHAR,
  model_reported VARCHAR,
  stream BOOLEAN NOT NULL DEFAULT false,
  http_status INTEGER,
  upstream_request_id VARCHAR,
  user_agent VARCHAR,
  originator VARCHAR,
  client_name VARCHAR,
  codex_session_id VARCHAR,
  error_type VARCHAR,
  error_message VARCHAR,
  input_tokens BIGINT,
  cached_input_tokens BIGINT,
  cache_write_tokens BIGINT,
  output_tokens BIGINT,
  reasoning_tokens BIGINT,
  total_tokens BIGINT,
  usage_json VARCHAR,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at);
CREATE INDEX IF NOT EXISTS idx_requests_model_requested ON requests(model_requested);
CREATE INDEX IF NOT EXISTS idx_requests_response_id ON requests(response_id);
CREATE INDEX IF NOT EXISTS idx_requests_source ON requests(source);
CREATE TABLE IF NOT EXISTS tool_calls (
  request_id VARCHAR NOT NULL,
  ordinal INTEGER NOT NULL,
  tool_call_id VARCHAR,
  name VARCHAR NOT NULL,
  command VARCHAR,
  description VARCHAR,
  arguments_json VARCHAR,
  PRIMARY KEY (request_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_request_id ON tool_calls(request_id);

CREATE TABLE IF NOT EXISTS web_requests (
  request_id VARCHAR NOT NULL,
  ordinal INTEGER NOT NULL,
  web_request_id VARCHAR,
  name VARCHAR NOT NULL,
  query VARCHAR,
  url VARCHAR,
  domain VARCHAR,
  arguments_json VARCHAR,
  PRIMARY KEY (request_id, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_web_requests_request_id ON web_requests(request_id);`)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	for _, col := range []struct{ name, typ string }{
		{"originator", "VARCHAR"}, {"client_name", "VARCHAR"},
		{"source", "VARCHAR DEFAULT 'unknown'"}, {"host", "VARCHAR"},
	} {
		if err := addColumnIfMissing(db, "requests", col.name, col.typ); err != nil {
			return err
		}
	}
	// Quack 1.5.x cannot reconstruct attached catalogs when a column default
	// contains a bound expression such as current_timestamp. Supply the value
	// explicitly on INSERT and remove the old default from upgraded ledgers.
	if _, err := db.Exec(`ALTER TABLE requests ALTER COLUMN created_at DROP DEFAULT`); err != nil {
		return fmt.Errorf("remove requests.created_at default: %w", err)
	}
	_, err = db.Exec(`CREATE OR REPLACE VIEW usage_by_day_model AS
SELECT
  CAST(CAST(date_trunc('day', started_at) AS DATE) AS VARCHAR) AS day,
  COALESCE(model_reported, model_requested, 'unknown') AS model,
  COUNT(*) AS request_count,
  SUM(COALESCE(input_tokens, 0)) AS input_tokens,
  SUM(COALESCE(cached_input_tokens, 0)) AS cached_input_tokens,
  SUM(COALESCE(cache_write_tokens, 0)) AS cache_write_tokens,
  SUM(COALESCE(output_tokens, 0)) AS output_tokens,
  SUM(COALESCE(reasoning_tokens, 0)) AS reasoning_tokens,
  SUM(COALESCE(total_tokens, 0)) AS total_tokens
FROM requests
GROUP BY day, model`)
	if err != nil {
		return fmt.Errorf("create live usage view: %w", err)
	}
	return nil
}

func validateSchema(db *sql.DB) error {
	requiredColumns := []string{
		"id", "response_id", "source", "host", "started_at", "completed_at", "duration_ms",
		"method", "path", "upstream_url", "model_requested", "model_reported",
		"stream", "http_status", "upstream_request_id", "user_agent", "originator",
		"client_name", "codex_session_id", "error_type", "error_message", "input_tokens",
		"cached_input_tokens", "cache_write_tokens", "output_tokens", "reasoning_tokens",
		"total_tokens", "usage_json", "created_at",
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
	if _, err := tableColumns(db, "tool_calls"); err != nil {
		return err
	}
	if _, err := tableColumns(db, "web_requests"); err != nil {
		return err
	}
	var viewCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.views WHERE table_name = 'usage_by_day_model'`).Scan(&viewCount); err != nil {
		return fmt.Errorf("validate live usage view: %w", err)
	}
	if viewCount != 1 {
		return errors.New("missing schema object \"usage_by_day_model\"")
	}
	return nil
}

// ProviderSQL returns the shared request-provider classification expression.
func ProviderSQL(column string) string {
	return "CASE " +
		"WHEN " + column + " LIKE '%/messages%' THEN 'anthropic' " +
		"WHEN " + column + " LIKE '%/responses%' OR " + column + " LIKE '%/chat/completions%' THEN 'openai' " +
		"ELSE 'unknown' END"
}

// ClientSQL returns the shared display-client classification expression.
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
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + columnType); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("read table info %s: %w", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
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

// created_at is explicit because Quack 1.5.x cannot attach catalogs that
// contain expression-backed column defaults.
const insertRequestSQL = `INSERT INTO requests (
  id, response_id, source, host, started_at, completed_at, duration_ms,
  method, path, upstream_url, model_requested, model_reported, stream, http_status,
  upstream_request_id, user_agent, originator, client_name, codex_session_id,
  error_type, error_message, input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, usage_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, current_timestamp)
ON CONFLICT (id) DO NOTHING`

const insertToolCallSQL = `INSERT INTO tool_calls (
  request_id, ordinal, tool_call_id, name, command, description, arguments_json
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (request_id, ordinal) DO NOTHING`

const insertWebRequestSQL = `INSERT INTO web_requests (
	request_id, ordinal, web_request_id, name, query, url, domain, arguments_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (request_id, ordinal) DO NOTHING`

// InsertBatch writes events idempotently in one transaction.
func (s *Store) InsertBatch(ctx context.Context, events []queue.UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	if s.remoteCatalog != "" {
		return s.insertRemoteBatch(ctx, events)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
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
		var completedAt, durationMS any
		if !ev.CompletedAt.IsZero() {
			completedAt = ev.CompletedAt
			durationMS = ev.CompletedAt.Sub(ev.StartedAt).Milliseconds()
		}
		var usageJSON any
		if len(ev.UsageJSON) > 0 {
			usageJSON = string(ev.UsageJSON)
		}
		source := ev.Source
		if source == "" {
			source = "unknown"
		}
		if _, err := stmt.ExecContext(ctx,
			ev.RequestID, nullableString(ev.ResponseID), source, nullableString(ev.Host), ev.StartedAt, completedAt, durationMS,
			ev.Method, ev.Path, ev.UpstreamURL, nullableString(ev.ModelRequested), nullableString(ev.ModelReported), ev.Stream, ev.HTTPStatus,
			nullableString(ev.UpstreamRequestID), nullableString(ev.UserAgent), nullableString(ev.Originator), nullableString(ev.ClientName), nullableString(ev.CodexSessionID),
			nullableString(ev.ErrorType), nullableString(ev.ErrorMessage), ev.Usage.InputTokens, ev.Usage.CachedInputTokens, ev.Usage.CacheWriteTokens,
			ev.Usage.OutputTokens, ev.Usage.ReasoningTokens, ev.Usage.TotalTokens, usageJSON,
		); err != nil {
			return fmt.Errorf("insert request %s: %w", ev.RequestID, err)
		}
		for ordinal, call := range ev.ToolCalls {
			if call.Name == "" {
				continue
			}
			if _, err := toolStmt.ExecContext(ctx, ev.RequestID, ordinal, nullableString(call.ID), call.Name,
				nullableString(call.Command), nullableString(call.Description), nullableString(call.ArgumentsJSON)); err != nil {
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

// insertRemoteBatch sends server-side INSERT statements through Quack's query
// macro. Binding ON CONFLICT against an attached remote table currently asks
// the Quack storage adapter for unsupported local storage metadata, while the
// same statement executes normally on the server. A replay after either query
// is safe because both target keys use ON CONFLICT DO NOTHING.
func (s *Store) insertRemoteBatch(ctx context.Context, events []queue.UsageEvent) error {
	requestRows := make([]string, 0, len(events))
	toolRows := make([]string, 0)
	webRows := make([]string, 0)
	for _, ev := range events {
		var completedAt, durationMS string
		if ev.CompletedAt.IsZero() {
			completedAt, durationMS = "NULL", "NULL"
		} else {
			completedAt = sqlTime(ev.CompletedAt)
			durationMS = strconv.FormatInt(ev.CompletedAt.Sub(ev.StartedAt).Milliseconds(), 10)
		}
		source := ev.Source
		if source == "" {
			source = "unknown"
		}
		requestRows = append(requestRows, "("+strings.Join([]string{
			sqlString(ev.RequestID), sqlNullableString(ev.ResponseID), sqlString(source), sqlNullableString(ev.Host),
			sqlTime(ev.StartedAt), completedAt, durationMS, sqlString(ev.Method), sqlString(ev.Path), sqlString(ev.UpstreamURL),
			sqlNullableString(ev.ModelRequested), sqlNullableString(ev.ModelReported), strconv.FormatBool(ev.Stream), strconv.Itoa(ev.HTTPStatus),
			sqlNullableString(ev.UpstreamRequestID), sqlNullableString(ev.UserAgent), sqlNullableString(ev.Originator), sqlNullableString(ev.ClientName),
			sqlNullableString(ev.CodexSessionID), sqlNullableString(ev.ErrorType), sqlNullableString(ev.ErrorMessage),
			sqlNullableInt64(ev.Usage.InputTokens), sqlNullableInt64(ev.Usage.CachedInputTokens), sqlNullableInt64(ev.Usage.CacheWriteTokens),
			sqlNullableInt64(ev.Usage.OutputTokens), sqlNullableInt64(ev.Usage.ReasoningTokens), sqlNullableInt64(ev.Usage.TotalTokens),
			sqlNullableString(string(ev.UsageJSON)), "current_timestamp",
		}, ", ")+")")
		for ordinal, call := range ev.ToolCalls {
			if call.Name == "" {
				continue
			}
			toolRows = append(toolRows, "("+strings.Join([]string{
				sqlString(ev.RequestID), strconv.Itoa(ordinal), sqlNullableString(call.ID), sqlString(call.Name),
				sqlNullableString(call.Command), sqlNullableString(call.Description), sqlNullableString(call.ArgumentsJSON),
			}, ", ")+")")
		}
		for ordinal, request := range ev.WebRequests {
			if !queue.IsWebToolName(request.Name) {
				continue
			}
			webRows = append(webRows, "("+strings.Join([]string{
				sqlString(ev.RequestID), strconv.Itoa(ordinal), sqlNullableString(request.ID), sqlString(request.Name),
				sqlNullableString(request.Query), sqlNullableString(request.URL), sqlNullableString(request.Domain), sqlNullableString(request.ArgumentsJSON),
			}, ", ")+")")
		}
	}
	requestQuery := `INSERT INTO requests (
  id, response_id, source, host, started_at, completed_at, duration_ms,
  method, path, upstream_url, model_requested, model_reported, stream, http_status,
  upstream_request_id, user_agent, originator, client_name, codex_session_id,
  error_type, error_message, input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, usage_json, created_at
) VALUES ` + strings.Join(requestRows, ", ") + ` ON CONFLICT (id) DO NOTHING`
	if err := s.execRemoteQuery(ctx, requestQuery); err != nil {
		return fmt.Errorf("insert remote requests: %w", err)
	}
	if len(toolRows) > 0 {
		toolQuery := `INSERT INTO tool_calls (
  request_id, ordinal, tool_call_id, name, command, description, arguments_json
) VALUES ` + strings.Join(toolRows, ", ") + ` ON CONFLICT (request_id, ordinal) DO NOTHING`
		if err := s.execRemoteQuery(ctx, toolQuery); err != nil {
			return fmt.Errorf("insert remote tool calls: %w", err)
		}
	}
	if len(webRows) > 0 {
		webQuery := `INSERT INTO web_requests (
  request_id, ordinal, web_request_id, name, query, url, domain, arguments_json
) VALUES ` + strings.Join(webRows, ", ") + ` ON CONFLICT (request_id, ordinal) DO NOTHING`
		if err := s.execRemoteQuery(ctx, webQuery); err != nil {
			return fmt.Errorf("insert remote web requests: %w", err)
		}
	}
	return nil
}

func (s *Store) execRemoteQuery(ctx context.Context, query string) error {
	rows, err := s.db.QueryContext(ctx, `FROM `+s.remoteCatalog+`.query(?)`, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// MigrateSQLite imports a legacy excursion-funnel SQLite ledger into DuckDB.
func MigrateSQLite(ctx context.Context, duckPath, sqlitePath string) error {
	st, err := Open(duckPath)
	if err != nil {
		return err
	}
	defer st.Close()
	conn, err := st.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()
	for _, query := range []string{"INSTALL sqlite", "LOAD sqlite"} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("%s: %w (first use requires network access)", strings.ToLower(query), err)
		}
	}
	if _, err := conn.ExecContext(ctx, "ATTACH "+quoteLiteral(sqlitePath)+" AS old (TYPE sqlite)"); err != nil {
		return fmt.Errorf("attach legacy SQLite ledger: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "DETACH old") }()
	requestCols, err := catalogTableColumns(ctx, conn, "old", "requests")
	if err != nil {
		return fmt.Errorf("inspect legacy requests: %w", err)
	}
	for _, required := range []string{"id", "started_at", "method", "path", "upstream_url"} {
		if !requestCols[required] {
			return fmt.Errorf("legacy requests missing required column %q", required)
		}
	}
	completedAt := "NULL"
	if requestCols["completed_at"] {
		completedAt = "CAST(completed_at AS TIMESTAMPTZ)"
	}
	createdAt := "CAST(started_at AS TIMESTAMPTZ)"
	if requestCols["created_at"] {
		createdAt = "CAST(created_at AS TIMESTAMPTZ)"
	}
	stream := "false"
	if requestCols["stream"] {
		stream = "stream <> 0"
	}
	requestSelect := []string{
		"id", legacyColumn(requestCols, "response_id", "NULL"), "'legacy'", "NULL",
		"CAST(started_at AS TIMESTAMPTZ)", completedAt, legacyColumn(requestCols, "duration_ms", "NULL"),
		"method", "path", "upstream_url", legacyColumn(requestCols, "model_requested", "NULL"), legacyColumn(requestCols, "model_reported", "NULL"),
		stream, legacyColumn(requestCols, "http_status", "NULL"), legacyColumn(requestCols, "upstream_request_id", "NULL"),
		legacyColumn(requestCols, "user_agent", "NULL"), legacyColumn(requestCols, "originator", "NULL"), legacyColumn(requestCols, "client_name", "NULL"),
		legacyColumn(requestCols, "codex_session_id", "NULL"), legacyColumn(requestCols, "error_type", "NULL"), legacyColumn(requestCols, "error_message", "NULL"),
		legacyColumn(requestCols, "input_tokens", "NULL"), legacyColumn(requestCols, "cached_input_tokens", "NULL"), legacyColumn(requestCols, "cache_write_tokens", "NULL"),
		legacyColumn(requestCols, "output_tokens", "NULL"), legacyColumn(requestCols, "reasoning_tokens", "NULL"), legacyColumn(requestCols, "total_tokens", "NULL"),
		legacyColumn(requestCols, "usage_json", "NULL"), createdAt,
	}
	toolCols, err := catalogTableColumns(ctx, conn, "old", "tool_calls")
	if err != nil {
		return fmt.Errorf("inspect legacy tool calls: %w", err)
	}
	webCols, err := catalogTableColumns(ctx, conn, "old", "web_requests")
	if err != nil {
		return fmt.Errorf("inspect legacy web requests: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO requests (
 id, response_id, source, host, started_at, completed_at, duration_ms,
 method, path, upstream_url, model_requested, model_reported, stream, http_status,
 upstream_request_id, user_agent, originator, client_name, codex_session_id,
 error_type, error_message, input_tokens, cached_input_tokens, cache_write_tokens,
 output_tokens, reasoning_tokens, total_tokens, usage_json, created_at)
SELECT `+strings.Join(requestSelect, ", ")+`
FROM old.requests ON CONFLICT (id) DO NOTHING`); err != nil {
		return fmt.Errorf("migrate requests: %w", err)
	}
	if len(toolCols) > 0 {
		for _, required := range []string{"request_id", "ordinal", "name"} {
			if !toolCols[required] {
				return fmt.Errorf("legacy tool_calls missing required column %q", required)
			}
		}
		toolSelect := []string{
			"request_id", "ordinal", legacyColumn(toolCols, "tool_call_id", "NULL"), "name",
			legacyColumn(toolCols, "command", "NULL"), legacyColumn(toolCols, "description", "NULL"), legacyColumn(toolCols, "arguments_json", "NULL"),
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_calls (
 request_id, ordinal, tool_call_id, name, command, description, arguments_json)
SELECT `+strings.Join(toolSelect, ", ")+`
FROM old.tool_calls ON CONFLICT (request_id, ordinal) DO NOTHING`); err != nil {
			return fmt.Errorf("migrate tool calls: %w", err)
		}
	}
	if len(webCols) > 0 {
		for _, required := range []string{"request_id", "ordinal", "name"} {
			if !webCols[required] {
				return fmt.Errorf("legacy web_requests missing required column %q", required)
			}
		}
		webSelect := []string{
			"request_id", "ordinal", legacyColumn(webCols, "web_request_id", "NULL"), "name",
			legacyColumn(webCols, "query", "NULL"), legacyColumn(webCols, "url", "NULL"),
			legacyColumn(webCols, "domain", "NULL"), legacyColumn(webCols, "arguments_json", "NULL"),
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO web_requests (
 request_id, ordinal, web_request_id, name, query, url, domain, arguments_json)
SELECT `+strings.Join(webSelect, ", ")+`
FROM old.web_requests ON CONFLICT (request_id, ordinal) DO NOTHING`); err != nil {
			return fmt.Errorf("migrate web requests: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func catalogTableColumns(ctx context.Context, conn *sql.Conn, catalog, table string) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT column_name
FROM information_schema.columns
WHERE table_catalog = ? AND table_name = ?`, catalog, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}

func legacyColumn(columns map[string]bool, name, fallback string) string {
	if columns[name] {
		return name
	}
	return fallback
}

// QuackURI normalizes host:port and quack:// inputs for DuckDB's extension.
func QuackURI(address string) string {
	address = strings.TrimSpace(address)
	if after, ok := strings.CutPrefix(address, "quack://"); ok {
		return "quack:" + after
	}
	if strings.HasPrefix(address, "quack:") {
		return address
	}
	return "quack:" + address
}

func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func sqlString(value string) string { return quoteLiteral(value) }

func sqlNullableString(value string) string {
	if value == "" {
		return "NULL"
	}
	return sqlString(value)
}

func sqlTime(value time.Time) string {
	return quoteLiteral(value.Format(time.RFC3339Nano)) + "::TIMESTAMPTZ"
}

func sqlNullableInt64(value *int64) string {
	if value == nil {
		return "NULL"
	}
	return strconv.FormatInt(*value, 10)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

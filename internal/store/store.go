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
	"slices"
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
	db                 *sql.DB
	quackConn          *sql.Conn
	quackURI           string
	remoteCatalog      string
	remoteHasSessionID bool
	remoteHasSessions  bool
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
	hasSessionID, hasSessions, err := inspectRemoteSessionSchema(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("inspect Quack ledger schema %s: %w", uri, err)
	}
	return &Store{
		db: db, remoteCatalog: "quack_remote",
		remoteHasSessionID: hasSessionID, remoteHasSessions: hasSessions,
	}, nil
}

func inspectRemoteSessionSchema(ctx context.Context, db *sql.DB) (bool, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_name, column_name
FROM information_schema.columns
WHERE (table_name = 'requests' AND column_name = 'session_id')
   OR table_name = 'sessions'`)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	var hasSessionID, hasSessions bool
	for rows.Next() {
		var tableName, columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			return false, false, err
		}
		if tableName == "sessions" {
			hasSessions = true
		}
		if tableName == "requests" && columnName == "session_id" {
			hasSessionID = true
		}
	}
	return hasSessionID, hasSessions, rows.Err()
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
	return s.startQuack(ctx, address, token, allowOtherHostname, nil)
}

// StartQuackAuthenticated installs a session-only Quack authentication macro
// before starting the listener. serverToken remains a server configuration
// value, but is intentionally ignored by the callback.
func (s *Store) StartQuackAuthenticated(ctx context.Context, address, serverToken string, validate func(string) bool) (QuackServer, error) {
	return s.startQuack(ctx, address, serverToken, false, validate)
}

func (s *Store) startQuack(ctx context.Context, address, token string, allowOtherHostname bool, validate func(string) bool) (QuackServer, error) {
	if s.quackConn != nil {
		return QuackServer{}, errors.New("quack server already started")
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
	if validate != nil {
		if err := duckdb.RegisterScalarUDF(conn, "ef_hub_validate_session", &sessionValidator{validate: validate}); err != nil {
			return closeOnError(fmt.Errorf("register hub session validator: %w", err))
		}
		for _, query := range []string{
			"CREATE OR REPLACE MACRO ef_hub_authenticate(server_token, client_token, client_info) AS ef_hub_validate_session(client_token)",
			"SET quack_authentication_function = 'ef_hub_authenticate'",
		} {
			if _, err := conn.ExecContext(ctx, query); err != nil {
				return closeOnError(fmt.Errorf("configure hub Quack authentication: %w", err))
			}
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

type sessionValidator struct{ validate func(string) bool }

func (*sessionValidator) Config() duckdb.ScalarFuncConfig {
	input, _ := duckdb.NewTypeInfo(duckdb.TYPE_VARCHAR)
	result, _ := duckdb.NewTypeInfo(duckdb.TYPE_BOOLEAN)
	return duckdb.ScalarFuncConfig{InputTypeInfos: []duckdb.TypeInfo{input}, ResultTypeInfo: result, Volatile: true}
}
func (v *sessionValidator) Executor() duckdb.ScalarFuncExecutor {
	return duckdb.ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) {
		if len(values) != 1 {
			return false, nil
		}
		token, _ := values[0].(string)
		return v.validate(token), nil
	}}
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
  session_id VARCHAR,
  codex_session_id VARCHAR,
  directory VARCHAR,
  git_branch VARCHAR,
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
CREATE TABLE IF NOT EXISTS sessions (
  provider VARCHAR NOT NULL,
  session_id VARCHAR NOT NULL,
  first_seen_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (provider, session_id)
);
CREATE INDEX IF NOT EXISTS idx_sessions_first_seen_at ON sessions(first_seen_at);
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
		{"session_id", "VARCHAR"},
		{"directory", "VARCHAR"}, {"git_branch", "VARCHAR"},
		{"source", "VARCHAR DEFAULT 'unknown'"}, {"host", "VARCHAR"},
	} {
		if err := addColumnIfMissing(db, "requests", col.name, col.typ); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_requests_directory ON requests(directory);
CREATE INDEX IF NOT EXISTS idx_requests_git_branch ON requests(git_branch);
CREATE INDEX IF NOT EXISTS idx_requests_session_id ON requests(session_id);`); err != nil {
		return fmt.Errorf("create project context indexes: %w", err)
	}
	if _, err := db.Exec(`UPDATE requests
SET session_id = codex_session_id
WHERE (session_id IS NULL OR session_id = '')
  AND codex_session_id IS NOT NULL AND codex_session_id != '';
INSERT INTO sessions (provider, session_id, first_seen_at)
SELECT provider, session_id, MIN(started_at) AS first_seen_at
FROM (
  SELECT ` + ProviderSQL("path") + ` AS provider,
    COALESCE(NULLIF(session_id, ''), NULLIF(codex_session_id, '')) AS session_id,
    started_at
  FROM requests
) observations
WHERE session_id IS NOT NULL AND provider != 'unknown'
GROUP BY provider, session_id
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at);`); err != nil {
		return fmt.Errorf("backfill sessions: %w", err)
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
		"client_name", "session_id", "codex_session_id", "directory", "git_branch", "error_type", "error_message", "input_tokens",
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
	if _, err := tableColumns(db, "sessions"); err != nil {
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

func providerForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, "/responses"), strings.Contains(path, "/chat/completions"):
		return "openai"
	default:
		return "unknown"
	}
}

func eventSessionID(event queue.UsageEvent) string {
	if sessionID := strings.TrimSpace(event.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(event.CodexSessionID)
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
  upstream_request_id, user_agent, originator, client_name, session_id, codex_session_id,
  directory, git_branch, error_type, error_message, input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, usage_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, current_timestamp)
ON CONFLICT (id) DO NOTHING`

const upsertSessionSQL = `INSERT INTO sessions (provider, session_id, first_seen_at)
VALUES (?, ?, ?)
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at)`

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
	sessionStmt, err := tx.PrepareContext(ctx, upsertSessionSQL)
	if err != nil {
		return fmt.Errorf("prepare session upsert: %w", err)
	}
	defer sessionStmt.Close()
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
		sessionID := eventSessionID(ev)
		if _, err := stmt.ExecContext(ctx,
			ev.RequestID, nullableString(ev.ResponseID), source, nullableString(ev.Host), ev.StartedAt, completedAt, durationMS,
			ev.Method, ev.Path, ev.UpstreamURL, nullableString(ev.ModelRequested), nullableString(ev.ModelReported), ev.Stream, ev.HTTPStatus,
			nullableString(ev.UpstreamRequestID), nullableString(ev.UserAgent), nullableString(ev.Originator), nullableString(ev.ClientName), nullableString(sessionID), nullableString(ev.CodexSessionID),
			nullableString(ev.Directory), nullableString(ev.GitBranch),
			nullableString(ev.ErrorType), nullableString(ev.ErrorMessage), ev.Usage.InputTokens, ev.Usage.CachedInputTokens, ev.Usage.CacheWriteTokens,
			ev.Usage.OutputTokens, ev.Usage.ReasoningTokens, ev.Usage.TotalTokens, usageJSON,
		); err != nil {
			return fmt.Errorf("insert request %s: %w", ev.RequestID, err)
		}
		provider := providerForPath(ev.Path)
		if sessionID != "" && provider != "unknown" {
			if _, err := sessionStmt.ExecContext(ctx, provider, sessionID, ev.StartedAt); err != nil {
				return fmt.Errorf("upsert session for request %s: %w", ev.RequestID, err)
			}
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
	sessionColumns := "session_id, codex_session_id"
	if !s.remoteHasSessionID {
		sessionColumns = "codex_session_id"
	}
	requestRows := make([]string, 0, len(events))
	sessionFirstSeen := make(map[string]time.Time)
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
		sessionID := eventSessionID(ev)
		requestValues := []string{
			sqlString(ev.RequestID), sqlNullableString(ev.ResponseID), sqlString(source), sqlNullableString(ev.Host),
			sqlTime(ev.StartedAt), completedAt, durationMS, sqlString(ev.Method), sqlString(ev.Path), sqlString(ev.UpstreamURL),
			sqlNullableString(ev.ModelRequested), sqlNullableString(ev.ModelReported), strconv.FormatBool(ev.Stream), strconv.Itoa(ev.HTTPStatus),
			sqlNullableString(ev.UpstreamRequestID), sqlNullableString(ev.UserAgent), sqlNullableString(ev.Originator), sqlNullableString(ev.ClientName),
		}
		if s.remoteHasSessionID {
			requestValues = append(requestValues, sqlNullableString(sessionID), sqlNullableString(ev.CodexSessionID))
		} else {
			// Legacy hubs only have codex_session_id. Use it as the transport
			// column for both providers; the hub's later migration classifies
			// the session from the request path when it backfills session_id.
			requestValues = append(requestValues, sqlNullableString(sessionID))
		}
		requestValues = append(requestValues,
			sqlNullableString(ev.Directory), sqlNullableString(ev.GitBranch),
			sqlNullableString(ev.ErrorType), sqlNullableString(ev.ErrorMessage),
			sqlNullableInt64(ev.Usage.InputTokens), sqlNullableInt64(ev.Usage.CachedInputTokens), sqlNullableInt64(ev.Usage.CacheWriteTokens),
			sqlNullableInt64(ev.Usage.OutputTokens), sqlNullableInt64(ev.Usage.ReasoningTokens), sqlNullableInt64(ev.Usage.TotalTokens),
			sqlNullableString(string(ev.UsageJSON)), "current_timestamp",
		)
		requestRows = append(requestRows, "("+strings.Join(requestValues, ", ")+")")
		provider := providerForPath(ev.Path)
		if s.remoteHasSessions && sessionID != "" && provider != "unknown" {
			key := provider + "\x00" + sessionID
			if firstSeen, ok := sessionFirstSeen[key]; !ok || ev.StartedAt.Before(firstSeen) {
				sessionFirstSeen[key] = ev.StartedAt
			}
		}
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
  upstream_request_id, user_agent, originator, client_name, ` + sessionColumns + `,
  directory, git_branch, error_type, error_message, input_tokens, cached_input_tokens, cache_write_tokens,
  output_tokens, reasoning_tokens, total_tokens, usage_json, created_at
) VALUES ` + strings.Join(requestRows, ", ") + ` ON CONFLICT (id) DO NOTHING`
	if err := s.execRemoteQuery(ctx, requestQuery); err != nil {
		return fmt.Errorf("insert remote requests: %w", err)
	}
	if s.remoteHasSessions && len(sessionFirstSeen) > 0 {
		sessionRows := make([]string, 0, len(sessionFirstSeen))
		for key, firstSeen := range sessionFirstSeen {
			provider, sessionID, _ := strings.Cut(key, "\x00")
			sessionRows = append(sessionRows, "("+strings.Join([]string{
				sqlString(provider), sqlString(sessionID), sqlTime(firstSeen),
			}, ", ")+")")
		}
		slices.Sort(sessionRows)
		sessionQuery := `INSERT INTO sessions (provider, session_id, first_seen_at)
VALUES ` + strings.Join(sessionRows, ", ") + `
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at)`
		if err := s.execRemoteQuery(ctx, sessionQuery); err != nil {
			return fmt.Errorf("upsert remote sessions: %w", err)
		}
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

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
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"github.com/dlnilsson/excursion-funnel/internal/pick"
	"github.com/dlnilsson/excursion-funnel/internal/provider"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// LocalQuackToken protects the localhost-only server used by reporting
// commands. It is intentionally an application token, not a network secret.
const LocalQuackToken = "excursion-funnel-local"

// Store is a DuckDB-backed usage ledger. A remote Store is a small in-memory
// DuckDB client with a Quack catalog attached as its current database.
type Store struct {
	db               *sql.DB
	quackConn        *sql.Conn
	quackURI         string
	remoteCatalog    string
	remoteHasStaging bool
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

// OpenHub opens or creates the shared hub ledger. The hub keeps the fully
// constrained and indexed schema for reads and local merges, and additionally
// exposes constraint-free staging tables that remote clients write into: Quack
// 1.5.5 crashes fatally while replaying an ART index insert for a remote
// write, invalidating the whole database, so remote writes must never touch a
// constrained table. Staged rows are folded into the indexed tables by
// MergeStaging. Any ledger table that lost its primary key during the earlier
// constraint-free experiment is rebuilt with its constraints on open.
func OpenHub(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := openDuckDB(path, false)
	if err != nil {
		return nil, err
	}
	if err := ensureHubSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure hub schema: %w", err)
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
	hasStaging, err := remoteHasStagingTables(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("inspect Quack ledger schema %s: %w", uri, err)
	}
	return &Store{db: db, remoteCatalog: "quack_remote", remoteHasStaging: hasStaging}, nil
}

func remoteHasStagingTables(ctx context.Context, db *sql.DB) (bool, error) {
	// Query information_schema.columns rather than .tables: attached Quack
	// catalogs populate the column view but not the table view.
	rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
WHERE table_name = 'staging_requests'`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns := make(map[string]bool, len(requestsTable.columns))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, name := range requestsTable.names() {
		if !columns[strings.ToLower(name)] {
			return false, nil
		}
	}
	return true, nil
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
	server.URI = pick.First(server.URI, uri)
	server.Token = pick.First(server.Token, token)
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

func ensureSchema(db *sql.DB) error { return ensureLedgerSchema(db) }

// ensureHubSchema prepares the shared hub ledger. The hub keeps the fully
// constrained and indexed schema for reads and local merges, and additionally
// exposes constraint-free staging tables that remote clients write into: Quack
// 1.5.5 crashes fatally while replaying an ART index insert for a remote write,
// so remote writes must never touch a constrained table. Staged rows are folded
// in locally by MergeStaging. Any ledger table that lost its primary key during
// the earlier constraint-free experiment is rebuilt with its constraints first.
func ensureHubSchema(db *sql.DB) error {
	if err := repairLedgerPrimaryKeys(db); err != nil {
		return err
	}
	if err := ensureLedgerSchema(db); err != nil {
		return err
	}
	return ensureStagingSchema(db)
}

func ensureLedgerSchema(db *sql.DB) error {
	create := strings.Join([]string{
		requestsTable.createSQL(),
		sessionsTable.createSQL(),
		toolCallsTable.createSQL(),
		webRequestsTable.createSQL(),
	}, "\n")
	if _, err := db.Exec(create); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	for _, col := range []struct{ name, typ string }{
		{"originator", "VARCHAR"}, {"client_name", "VARCHAR"},
		{"session_id", "VARCHAR"},
		{"directory", "VARCHAR"}, {"git_branch", "VARCHAR"}, {"effort", "VARCHAR"},
		{"source", "VARCHAR DEFAULT 'unknown'"}, {"host", "VARCHAR"},
	} {
		if err := addColumnIfMissing(db, "requests", col.name, col.typ); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at);
CREATE INDEX IF NOT EXISTS idx_requests_model_requested ON requests(model_requested);
CREATE INDEX IF NOT EXISTS idx_requests_response_id ON requests(response_id);
CREATE INDEX IF NOT EXISTS idx_requests_source ON requests(source);
CREATE INDEX IF NOT EXISTS idx_sessions_first_seen_at ON sessions(first_seen_at);
CREATE INDEX IF NOT EXISTS idx_tool_calls_request_id ON tool_calls(request_id);
CREATE INDEX IF NOT EXISTS idx_web_requests_request_id ON web_requests(request_id);
CREATE INDEX IF NOT EXISTS idx_requests_directory ON requests(directory);
CREATE INDEX IF NOT EXISTS idx_requests_git_branch ON requests(git_branch);
CREATE INDEX IF NOT EXISTS idx_requests_session_id ON requests(session_id);`); err != nil {
		return fmt.Errorf("create schema indexes: %w", err)
	}
	if err := backfillSessions(db); err != nil {
		return err
	}
	// Quack 1.5.x cannot reconstruct attached catalogs when a column default
	// contains a bound expression such as current_timestamp. Supply the value
	// explicitly on INSERT and remove the old default from upgraded ledgers.
	if _, err := db.Exec(`ALTER TABLE requests ALTER COLUMN created_at DROP DEFAULT`); err != nil {
		return fmt.Errorf("remove requests.created_at default: %w", err)
	}
	if _, err := db.Exec(`CREATE OR REPLACE VIEW usage_by_day_model AS
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
GROUP BY day, model`); err != nil {
		return fmt.Errorf("create live usage view: %w", err)
	}
	return nil
}

// ensureStagingSchema creates the constraint-free, index-free staging tables
// that remote clients write into. Their absence of an ART index is exactly what
// keeps Quack's remote-write path from crashing.
func ensureStagingSchema(db *sql.DB) error {
	create := strings.Join([]string{
		requestsTable.createStagingSQL(),
		sessionsTable.createStagingSQL(),
		toolCallsTable.createStagingSQL(),
		webRequestsTable.createStagingSQL(),
	}, "\n")
	if _, err := db.Exec(create); err != nil {
		return fmt.Errorf("create staging schema: %w", err)
	}
	return addColumnIfMissing(db, "staging_requests", "effort", "VARCHAR")
}

func backfillSessions(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE requests
SET session_id = codex_session_id
WHERE (session_id IS NULL OR session_id = '')
  AND codex_session_id IS NOT NULL AND codex_session_id != ''`); err != nil {
		return fmt.Errorf("backfill request session ids: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (provider, session_id, first_seen_at)
SELECT provider, session_id, MIN(started_at) AS first_seen_at
FROM (
  SELECT ` + provider.SQLForPath("path") + ` AS provider,
    COALESCE(NULLIF(session_id, ''), NULLIF(codex_session_id, '')) AS session_id,
    started_at
  FROM requests
) observations
WHERE session_id IS NOT NULL AND provider != 'unknown'
GROUP BY provider, session_id
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at)`); err != nil {
		return fmt.Errorf("backfill sessions: %w", err)
	}
	return nil
}

// repairLedgerPrimaryKeys restores the primary keys on any ledger table that
// lost them during the earlier constraint-free experiment (or a manual rebuild).
// DuckDB cannot add a primary key to a populated table in place, so the table is
// copied into a fresh constrained one. Tables that do not yet exist are left for
// ensureLedgerSchema to create constrained.
func repairLedgerPrimaryKeys(db *sql.DB) error {
	for _, t := range []struct{ name, columnsDDL, primaryKey string }{
		{requestsTable.name, requestsTable.columnsDDL(), requestsTable.primaryKey},
		{sessionsTable.name, sessionsTable.columnsDDL(), sessionsTable.primaryKey},
		{toolCallsTable.name, toolCallsTable.columnsDDL(), toolCallsTable.primaryKey},
		{webRequestsTable.name, webRequestsTable.columnsDDL(), webRequestsTable.primaryKey},
	} {
		exists, err := tableExists(db, t.name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		hasPK, err := tableHasPrimaryKey(db, t.name)
		if err != nil {
			return err
		}
		if hasPK {
			continue
		}
		if err := rebuildWithPrimaryKey(db, t.name, t.columnsDDL, t.primaryKey); err != nil {
			return err
		}
	}
	return nil
}

func rebuildWithPrimaryKey(db *sql.DB, table, columnsDDL, primaryKey string) error {
	cols, err := orderedColumns(db, table)
	if err != nil {
		return err
	}
	colList := strings.Join(cols, ", ")
	if err := withTransaction(db, func(tx *sql.Tx) error {
		for _, stmt := range primaryKeyRebuildStatements(table, columnsDDL, primaryKey, colList) {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("restore primary key on %s: %w", table, err)
	}
	return nil
}

// withTransaction commits run's work only when it completes successfully.
// The deferred rollback is a no-op after commit and restores every prior DDL
// statement when a migration fails midway through.
func withTransaction(db *sql.DB, run func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := run(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func primaryKeyRebuildStatements(table, columnsDDL, primaryKey, columns string) []string {
	tmp := table + "_ef_rebuild"
	return []string{
		"DROP TABLE IF EXISTS " + tmp,
		"CREATE TABLE " + tmp + " (\n  " + columnsDDL + ",\n  PRIMARY KEY (" + primaryKey + ")\n)",
		fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", tmp, columns, columns, table),
		"DROP TABLE " + table,
		"ALTER TABLE " + tmp + " RENAME TO " + table,
	}
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	return count > 0, nil
}

func tableHasPrimaryKey(db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM duckdb_constraints()
WHERE table_name = ? AND constraint_type = 'PRIMARY KEY'`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect primary key for %s: %w", table, err)
	}
	return count > 0, nil
}

func orderedColumns(db *sql.DB, table string) ([]string, error) {
	cols, err := scanStrings(db, `SELECT column_name FROM information_schema.columns
WHERE table_name = ? ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", table, err)
	}
	return cols, nil
}

// scanStrings runs a single-column query and collects every value. The schema
// probes in this file all have that shape.
func scanStrings(db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validateSchema(db *sql.DB) error {
	requiredColumns := requestsTable.names()
	cols, err := tableColumns(db, requestsTable.name)
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

func eventSessionID(event queue.UsageEvent) string {
	if sessionID := strings.TrimSpace(event.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(event.CodexSessionID)
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
	names, err := scanStrings(db, `SELECT column_name FROM information_schema.columns WHERE table_name = ?`, table)
	if err != nil {
		return nil, fmt.Errorf("read table info %s: %w", table, err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("table %q does not exist", table)
	}
	cols := make(map[string]bool, len(names))
	for _, name := range names {
		cols[strings.ToLower(name)] = true
	}
	return cols, nil
}

// created_at is supplied explicitly on every insert because Quack 1.5.x cannot
// attach catalogs that contain expression-backed column defaults.
var (
	insertRequestSQL    = requestsTable.insertSQL()
	insertToolCallSQL   = toolCallsTable.insertSQL()
	insertWebRequestSQL = webRequestsTable.insertSQL()
)

// The session registry upserts rather than ignoring conflicts, so it does not
// use the generated insert.
const upsertSessionSQL = `INSERT INTO sessions (provider, session_id, first_seen_at)
VALUES (?, ?, ?)
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at)`

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
		row := newRequestRow(ev)
		if _, err := stmt.ExecContext(ctx, requestsTable.args(row)...); err != nil {
			return fmt.Errorf("insert request %s: %w", ev.RequestID, err)
		}
		requestProvider := provider.ForPath(ev.Path)
		if row.sessionID != "" && requestProvider != provider.Unknown {
			if _, err := sessionStmt.ExecContext(ctx, requestProvider, row.sessionID, ev.StartedAt); err != nil {
				return fmt.Errorf("upsert session for request %s: %w", ev.RequestID, err)
			}
		}
		for _, child := range toolCallRows(ev) {
			if _, err := toolStmt.ExecContext(ctx, toolCallsTable.args(child)...); err != nil {
				return fmt.Errorf("insert tool call for request %s: %w", ev.RequestID, err)
			}
		}
		for _, child := range webRequestRows(ev) {
			if _, err := webStmt.ExecContext(ctx, webRequestsTable.args(child)...); err != nil {
				return fmt.Errorf("insert web request for request %s: %w", ev.RequestID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// insertRemoteBatch appends a batch into the hub's constraint-free staging
// tables via Quack's query macro. Staging tables have no ART index, so these
// remote writes never hit Quack 1.5.5's crashing index-replay path. The hub's
// MergeStaging job later folds staged rows into the indexed ledger tables
// locally, where DuckDB maintains the ART indexes safely. Deduplication happens
// at merge time (ON CONFLICT against the real tables), so duplicate staged rows
// are harmless.
func (s *Store) insertRemoteBatch(ctx context.Context, events []queue.UsageEvent) error {
	if !s.remoteHasStaging {
		return errors.New("hub is missing staging tables; upgrade the hub to a build with staging support")
	}
	var (
		requestTuples    = make([]string, 0, len(events))
		toolTuples       []string
		webTuples        []string
		sessionFirstSeen = make(map[string]time.Time)
	)
	for _, ev := range events {
		row := newRequestRow(ev)
		requestTuples = append(requestTuples, requestsTable.valuesTuple(row))
		requestProvider := provider.ForPath(ev.Path)
		if row.sessionID != "" && requestProvider != provider.Unknown {
			key := requestProvider + "\x00" + row.sessionID
			if firstSeen, ok := sessionFirstSeen[key]; !ok || ev.StartedAt.Before(firstSeen) {
				sessionFirstSeen[key] = ev.StartedAt
			}
		}
		for _, child := range toolCallRows(ev) {
			toolTuples = append(toolTuples, toolCallsTable.valuesTuple(child))
		}
		for _, child := range webRequestRows(ev) {
			webTuples = append(webTuples, webRequestsTable.valuesTuple(child))
		}
	}

	if err := s.stageRemote(ctx, requestsTable.name, requestsTable.columnNames(), requestTuples); err != nil {
		return fmt.Errorf("stage remote requests: %w", err)
	}
	if len(sessionFirstSeen) > 0 {
		sessionTuples := make([]string, 0, len(sessionFirstSeen))
		for key, firstSeen := range sessionFirstSeen {
			providerName, sessionID, _ := strings.Cut(key, "\x00")
			sessionTuples = append(sessionTuples, sessionsTable.valuesTuple(sessionRow{
				provider: providerName, sessionID: sessionID, firstSeen: firstSeen,
			}))
		}
		// Map iteration order is random; sort so a retried batch produces
		// byte-identical SQL.
		slices.Sort(sessionTuples)
		if err := s.stageRemote(ctx, sessionsTable.name, sessionsTable.columnNames(), sessionTuples); err != nil {
			return fmt.Errorf("stage remote sessions: %w", err)
		}
	}
	if err := s.stageRemote(ctx, toolCallsTable.name, toolCallsTable.columnNames(), toolTuples); err != nil {
		return fmt.Errorf("stage remote tool calls: %w", err)
	}
	if err := s.stageRemote(ctx, webRequestsTable.name, webRequestsTable.columnNames(), webTuples); err != nil {
		return fmt.Errorf("stage remote web requests: %w", err)
	}
	return nil
}

// stageRemote appends value tuples to a staging table. An empty batch is a
// no-op rather than an INSERT with no VALUES.
func (s *Store) stageRemote(ctx context.Context, table, columns string, tuples []string) error {
	if len(tuples) == 0 {
		return nil
	}
	return s.execRemoteQuery(ctx, "INSERT INTO staging_"+table+" ("+columns+") VALUES "+strings.Join(tuples, ", "))
}

// MergeStaging folds the constraint-free staging tables that remote clients
// write into the indexed ledger tables. It runs on the hub's local connection,
// where DuckDB maintains the ART indexes safely — unlike Quack's remote-write
// replay, which crashes on those same indexes. The merge and the staging
// cleanup share one transaction: under DuckDB's snapshot isolation the DELETE
// only removes rows visible when the transaction began, so rows a client stages
// concurrently survive to the next merge. Returns the number of request rows
// newly inserted into the ledger.
func (s *Store) MergeStaging(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin merge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, requestsTable.mergeStagingSQL())
	if err != nil {
		return 0, fmt.Errorf("merge staged requests: %w", err)
	}
	merged, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("merge staged requests: %w", err)
	}
	// Sessions merge with an upsert rather than ON CONFLICT DO NOTHING: the
	// earliest first_seen_at across every staged duplicate has to win.
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (provider, session_id, first_seen_at)
SELECT provider, session_id, MIN(first_seen_at) AS first_seen_at FROM staging_sessions
GROUP BY provider, session_id
ON CONFLICT (provider, session_id) DO UPDATE
SET first_seen_at = LEAST(sessions.first_seen_at, excluded.first_seen_at)`); err != nil {
		return 0, fmt.Errorf("merge staged sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, toolCallsTable.mergeStagingSQL()); err != nil {
		return 0, fmt.Errorf("merge staged tool calls: %w", err)
	}
	if _, err := tx.ExecContext(ctx, webRequestsTable.mergeStagingSQL()); err != nil {
		return 0, fmt.Errorf("merge staged web requests: %w", err)
	}
	for _, staging := range []string{"staging_requests", "staging_sessions", "staging_tool_calls", "staging_web_requests"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+staging); err != nil {
			return 0, fmt.Errorf("clear %s: %w", staging, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit merge: %w", err)
	}
	return merged, nil
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

func sqlTime(value time.Time) string {
	return quoteLiteral(value.Format(time.RFC3339Nano)) + "::TIMESTAMPTZ"
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

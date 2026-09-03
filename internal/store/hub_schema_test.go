package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

func hubTables() []string { return []string{"requests", "sessions", "tool_calls", "web_requests"} }

func stagingTables() []string {
	return []string{"staging_requests", "staging_sessions", "staging_tool_calls", "staging_web_requests"}
}

func TestOpenHubKeepsConstraintsAndAddsStaging(t *testing.T) {
	st, err := OpenHub(filepath.Join(t.TempDir(), "hub.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, table := range hubTables() {
		hasPK, err := tableHasPrimaryKey(st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if !hasPK {
			t.Fatalf("ledger table %s lost its primary key", table)
		}
	}
	for _, table := range stagingTables() {
		exists, err := tableExists(st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("staging table %s was not created", table)
		}
		hasPK, err := tableHasPrimaryKey(st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if hasPK {
			t.Fatalf("staging table %s must not carry a primary key", table)
		}
	}
}

func TestOpenHubUpgradesEffortColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.duckdb")
	hub, err := OpenHub(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openDuckDB(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP VIEW usage_by_day_model",
		"DROP INDEX idx_requests_started_at",
		"DROP INDEX idx_requests_model_requested",
		"DROP INDEX idx_requests_response_id",
		"DROP INDEX idx_requests_source",
		"DROP INDEX idx_requests_session_id",
		"DROP INDEX idx_requests_directory",
		"DROP INDEX idx_requests_git_branch",
	} {
		if _, err := db.Exec(query); err != nil {
			_ = db.Close()
			t.Fatalf("%s: %v", query, err)
		}
	}
	for _, table := range []string{"requests", "staging_requests"} {
		if _, err := db.Exec("ALTER TABLE " + table + " DROP COLUMN effort"); err != nil {
			_ = db.Close()
			t.Fatalf("drop %s.effort: %v", table, err)
		}
	}
	if hasStaging, err := remoteHasStagingTables(t.Context(), db); err != nil || hasStaging {
		_ = db.Close()
		t.Fatalf("remoteHasStagingTables() = %t, %v; want false, nil", hasStaging, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenHub(path)
	if err != nil {
		t.Fatalf("OpenHub() upgrade error = %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	for _, table := range []string{"requests", "staging_requests"} {
		columns, err := tableColumns(upgraded.db, table)
		if err != nil {
			t.Fatal(err)
		}
		if !columns["effort"] {
			t.Fatalf("%s missing effort after upgrade", table)
		}
	}
	if hasStaging, err := remoteHasStagingTables(t.Context(), upgraded.db); err != nil || !hasStaging {
		t.Fatalf("remoteHasStagingTables() = %t, %v; want true, nil", hasStaging, err)
	}
}

func TestOpenHubRestoresDroppedPrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.duckdb")
	seed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	if err := seed.InsertBatch(t.Context(), []queue.UsageEvent{{
		RequestID: "req-1", Source: "test", StartedAt: started,
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate the manual rung-1 rebuild that left requests without a PK.
	raw, err := openDuckDB(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP VIEW IF EXISTS usage_by_day_model",
		"CREATE TABLE requests_stripped AS SELECT * FROM requests",
		"DROP TABLE requests",
		"ALTER TABLE requests_stripped RENAME TO requests",
	} {
		if _, err := raw.Exec(query); err != nil {
			_ = raw.Close()
			t.Fatalf("%s: %v", query, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	hub, err := OpenHub(path)
	if err != nil {
		t.Fatalf("OpenHub() after PK loss = %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	hasPK, err := tableHasPrimaryKey(hub.db, "requests")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPK {
		t.Fatal("requests primary key was not restored")
	}
	var count int
	if err := hub.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE id = 'req-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("row lost during PK repair: count = %d", count)
	}
}

func TestPrimaryKeyRebuildRollsBackAfterDroppingOriginalTable(t *testing.T) {
	db, err := openDuckDB(filepath.Join(t.TempDir(), "rollback.duckdb"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE repair_test (id VARCHAR, value VARCHAR);
INSERT INTO repair_test VALUES ('row-1', 'kept')`); err != nil {
		t.Fatal(err)
	}

	interrupted := errors.New("injected interruption after dropping original table")
	err = withTransaction(db, func(tx *sql.Tx) error {
		statements := primaryKeyRebuildStatements("repair_test", "id VARCHAR, value VARCHAR", "id", "id, value")
		for index, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
			if index == 3 {
				return interrupted
			}
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("withTransaction() error = %v, want injected interruption", err)
	}

	exists, err := tableExists(db, "repair_test")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("original table was not restored after rollback")
	}
	hasPK, err := tableHasPrimaryKey(db, "repair_test")
	if err != nil {
		t.Fatal(err)
	}
	if hasPK {
		t.Fatal("rollback left the rebuilt primary key in place")
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM repair_test WHERE id = 'row-1'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "kept" {
		t.Fatalf("restored row value = %q, want kept", value)
	}
	tmpExists, err := tableExists(db, "repair_test_ef_rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if tmpExists {
		t.Fatal("rollback left the rebuild table in place")
	}
}

func TestMergeStagingFoldsStagedRows(t *testing.T) {
	st, err := OpenHub(filepath.Join(t.TempDir(), "hub.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	started := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	stage := func(id string) {
		t.Helper()
		if _, err := st.db.ExecContext(t.Context(), `INSERT INTO staging_requests
(id, source, started_at, method, path, upstream_url, created_at)
VALUES (?, 'test', ?, 'POST', '/v1/responses', '/', ?)`, id, started, started); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(t.Context(), `INSERT INTO staging_sessions
(provider, session_id, first_seen_at) VALUES ('openai', ?, ?)`, "sess-"+id, started); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(t.Context(), `INSERT INTO staging_tool_calls
(request_id, ordinal, name) VALUES (?, 0, 'shell')`, id); err != nil {
			t.Fatal(err)
		}
	}

	stage("req-1")
	merged, err := st.MergeStaging(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if merged != 1 {
		t.Fatalf("merged = %d, want 1", merged)
	}
	assertLedgerCounts(t, st, 1, 1, 1)
	assertStagingEmpty(t, st)

	// A duplicate re-stage of an already-merged id is a no-op via ON CONFLICT.
	stage("req-1")
	merged, err = st.MergeStaging(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if merged != 0 {
		t.Fatalf("re-merge merged = %d, want 0", merged)
	}
	assertLedgerCounts(t, st, 1, 1, 1)
	assertStagingEmpty(t, st)
}

func assertLedgerCounts(t *testing.T, st *Store, requests, sessions, tools int) {
	t.Helper()
	var gotRequests, gotSessions, gotTools int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&gotRequests); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&gotSessions); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&gotTools); err != nil {
		t.Fatal(err)
	}
	if gotRequests != requests || gotSessions != sessions || gotTools != tools {
		t.Fatalf("ledger counts requests=%d sessions=%d tools=%d, want %d/%d/%d",
			gotRequests, gotSessions, gotTools, requests, sessions, tools)
	}
}

func assertStagingEmpty(t *testing.T, st *Store) {
	t.Helper()
	for _, table := range stagingTables() {
		var count int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s not drained: count = %d", table, count)
		}
	}
}

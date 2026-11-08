package report

import (
	"context"
	"database/sql"
	"strconv"
)

// Status describes the outcome without treating missing telemetry as success.
func (r ToolCallRow) Status() string {
	if r.Failed == nil {
		return "unknown"
	}
	status := "ok"
	if *r.Failed {
		status = "FAILED"
	}
	if r.ExitCode != nil {
		status += " (exit " + strconv.FormatInt(*r.ExitCode, 10) + ")"
	}
	return status
}

type toolResultKey struct {
	source, host, provider, session, id string
}

func (r *Reporter) attachToolResults(ctx context.Context, calls []ToolCallRow) error {
	if len(calls) == 0 {
		return nil
	}
	// Read-only reports can open ledgers written by older versions.
	var columns int
	if err := queryRow(ctx, r.store, "tool result schema", `SELECT COUNT(*) FROM information_schema.columns
WHERE table_name = 'tool_results' AND column_name = 'tool_call_id'`, nil,
		func(rows *sql.Rows) error { return rows.Scan(&columns) }); err != nil {
		return err
	}
	if columns == 0 {
		return nil
	}
	var (
		ids  = make([]any, 0, len(calls))
		seen = make(map[string]bool, len(calls))
	)
	for _, call := range calls {
		if call.ID != "" && !seen[call.ID] {
			ids = append(ids, call.ID)
			seen[call.ID] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// Use an independent scan: Quack cannot stream a SQL join of ledger tables.
	rows, err := queryRows(ctx, r.store, "tool results", `SELECT source, host, provider, session_id,
tool_call_id, exit_code, failed FROM tool_results WHERE tool_call_id IN (`+placeholders(len(ids))+`)
QUALIFY row_number() OVER (PARTITION BY source, host, provider, session_id, tool_call_id
ORDER BY observed_at DESC, request_id DESC, ordinal DESC) = 1`, ids,
		func(rows *sql.Rows, result *toolResultOutcome) error {
			return rows.Scan(&result.key.source, &result.key.host, &result.key.provider, &result.key.session,
				&result.key.id, &result.exitCode, &result.failed)
		})
	if err != nil {
		return err
	}
	byID := make(map[toolResultKey]toolResultOutcome, len(rows))
	for _, row := range rows {
		byID[row.key] = row
	}
	for i := range calls {
		call := &calls[i]
		if result, ok := byID[toolResultKey{call.Source, call.Host, call.Provider, call.SessionID, call.ID}]; ok {
			call.Failed = &result.failed
			if result.exitCode.Valid {
				call.ExitCode = &result.exitCode.Int64
			}
		}
	}
	return nil
}

type toolResultOutcome struct {
	key      toolResultKey
	exitCode sql.NullInt64
	failed   bool
}

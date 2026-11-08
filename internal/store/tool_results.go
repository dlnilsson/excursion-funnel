package store

import (
	"github.com/dlnilsson/excursion-funnel/internal/provider"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

type toolResultRow struct {
	event   queue.UsageEvent
	ordinal int
	result  queue.ToolResult
}

// Outcome observations live separately from invocations: a later request may
// arrive first at a hub, and histories can replay the same result many times.
var toolResultsTable = table[toolResultRow]{
	name:       "tool_results",
	primaryKey: "request_id, ordinal",
	columns: []column[toolResultRow]{
		{name: "request_id", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return r.event.RequestID }},
		{name: "ordinal", ddl: "INTEGER NOT NULL", value: func(r toolResultRow) any { return r.ordinal }},
		{name: "tool_call_id", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return r.result.ID }},
		{name: "source", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return newRequestRow(r.event).source }},
		{name: "host", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return r.event.Host }},
		{name: "provider", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return provider.ForPath(r.event.Path) }},
		{name: "session_id", ddl: "VARCHAR NOT NULL", value: func(r toolResultRow) any { return eventSessionID(r.event) }},
		{name: "observed_at", ddl: "TIMESTAMPTZ NOT NULL", value: func(r toolResultRow) any { return r.event.StartedAt }},
		{name: "exit_code", ddl: "BIGINT", value: func(r toolResultRow) any { return r.result.ExitCode }},
		{name: "failed", ddl: "BOOLEAN NOT NULL", value: func(r toolResultRow) any { return r.result.Failed }},
	},
}

func toolResultRows(ev queue.UsageEvent) []toolResultRow {
	rows := make([]toolResultRow, 0, len(ev.ToolResults))
	for ordinal, result := range ev.ToolResults {
		if result.ID != "" {
			rows = append(rows, toolResultRow{event: ev, ordinal: ordinal, result: result})
		}
	}
	return rows
}

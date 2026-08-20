package inspect

import (
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

func printRows(out io.Writer, rows []report.InspectRow) {
	for index, row := range rows {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "id: %s\n", row.ID)
		if row.ResponseID != "" {
			fmt.Fprintf(out, "response_id: %s\n", row.ResponseID)
		}
		fmt.Fprintf(out, "source: %s\n", row.Source)
		if row.Host != "" {
			fmt.Fprintf(out, "host: %s\n", row.Host)
		}
		fmt.Fprintf(out, "directory: %s\n", reporting.EmptyAsDash(row.Directory))
		fmt.Fprintf(out, "git_branch: %s\n", reporting.EmptyAsDash(row.GitBranch))
		fmt.Fprintf(out, "started_at: %s\n", reporting.LocalTimestamp(row.StartedAt))
		if row.CompletedAt.Valid {
			fmt.Fprintf(out, "completed_at: %s\n", reporting.LocalTimestamp(row.CompletedAt.Time))
		}
		if row.DurationMS.Valid {
			fmt.Fprintf(out, "duration_ms: %d\n", row.DurationMS.Int64)
		}
		fmt.Fprintf(out, "provider: %s\n", row.Provider)
		fmt.Fprintf(out, "client: %s\n", reporting.EmptyAsDash(row.Client))
		fmt.Fprintf(out, "request: %s %s\n", row.Method, row.Path)
		fmt.Fprintf(out, "upstream_url: %s\n", row.UpstreamURL)
		fmt.Fprintf(out, "model_requested: %s\n", reporting.EmptyAsDash(row.ModelRequested))
		fmt.Fprintf(out, "model_reported: %s\n", reporting.EmptyAsDash(row.ModelReported))
		fmt.Fprintf(out, "stream: %t\n", row.Stream)
		if row.HTTPStatus.Valid {
			fmt.Fprintf(out, "http_status: %d\n", row.HTTPStatus.Int64)
		}
		if row.UpstreamRequestID != "" {
			fmt.Fprintf(out, "upstream_request_id: %s\n", row.UpstreamRequestID)
		}
		if row.UserAgent != "" {
			fmt.Fprintf(out, "user_agent: %s\n", row.UserAgent)
		}
		if row.Originator != "" {
			fmt.Fprintf(out, "originator: %s\n", row.Originator)
		}
		if row.CodexSessionID != "" {
			fmt.Fprintf(out, "codex_session_id: %s\n", row.CodexSessionID)
		}
		if row.ErrorType != "" {
			fmt.Fprintf(out, "error: %s %s\n", row.ErrorType, row.ErrorMessage)
		}
		fmt.Fprintf(out, "tokens: input=%s cached=%s cache_write=%s output=%s reasoning=%s total=%s\n",
			freshInputCell(row.Provider, row.Input, row.Cached, row.CacheWrite), nullInt(row.Cached), nullInt(row.CacheWrite), nullInt(row.Output), nullInt(row.Reasoning), nullInt(row.Total))
		if strings.TrimSpace(row.UsageJSON) != "" {
			fmt.Fprintf(out, "usage_json: %s\n", row.UsageJSON)
		}
		for _, call := range row.ToolCalls {
			fmt.Fprintf(out, "tool_call[%d]: %s\n", call.Ordinal, call.Name)
			if call.ID != "" {
				fmt.Fprintf(out, "  id: %s\n", call.ID)
			}
			if call.Description != "" {
				fmt.Fprintf(out, "  description: %s\n", call.Description)
			}
			if call.Command != "" {
				fmt.Fprintf(out, "  command: %s\n", call.Command)
			}
			if call.ArgumentsJSON != "" {
				fmt.Fprintf(out, "  arguments_json: %s\n", call.ArgumentsJSON)
			}
		}
		for _, request := range row.WebRequests {
			fmt.Fprintf(out, "web_request[%d]: %s\n", request.Ordinal, request.Name)
			if request.ID != "" {
				fmt.Fprintf(out, "  id: %s\n", request.ID)
			}
			if request.Query != "" {
				fmt.Fprintf(out, "  query: %s\n", request.Query)
			}
			if request.URL != "" {
				fmt.Fprintf(out, "  url: %s\n", request.URL)
			}
			if request.Domain != "" {
				fmt.Fprintf(out, "  domain: %s\n", request.Domain)
			}
			if request.ArgumentsJSON != "" {
				fmt.Fprintf(out, "  arguments_json: %s\n", request.ArgumentsJSON)
			}
		}
	}
}

func nullInt(value sql.NullInt64) string {
	if !value.Valid {
		return "-"
	}
	return strconv.FormatInt(value.Int64, 10)
}

func freshInput(provider string, input, cached, cacheWrite int64) int64 {
	fresh := input - cached
	if provider == "anthropic" {
		fresh -= cacheWrite
	}
	if fresh < 0 {
		return 0
	}
	return fresh
}

func freshInputCell(provider string, input, cached, cacheWrite sql.NullInt64) string {
	if !input.Valid {
		return "-"
	}
	return strconv.FormatInt(freshInput(provider, input.Int64, cached.Int64, cacheWrite.Int64), 10)
}

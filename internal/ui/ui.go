// Package ui serves a minimal, read-only local dashboard summarizing usage
// telemetry recorded by the proxy. It never mutates the usage ledger.
package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

//go:embed assets/index.html assets/vendor/*
var assets embed.FS

const recentErrorsLimit = 20

const recentToolCallsLimit = 50

const recentWebRequestsLimit = 50

const hourlyHistoryBuckets = 24

// New builds the dashboard handler, rooted at /ui/.
func New(rep *report.Reporter, log *slog.Logger) http.Handler {
	staticAssets, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("ui: open embedded assets: " + err.Error())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/", handleIndex)
	mux.Handle("GET /ui/assets/", http.StripPrefix("/ui/assets/", http.FileServerFS(staticAssets)))
	mux.HandleFunc("GET /ui/api/kpis", handleKPIs(rep, log))
	mux.HandleFunc("GET /ui/api/sessions", handleSessions(rep, log))
	mux.HandleFunc("GET /ui/api/summary", handleSummary(rep, log))
	mux.HandleFunc("GET /ui/api/sources", handleSources(rep, log))
	mux.HandleFunc("GET /ui/api/directories", handleDirectories(rep, log))
	mux.HandleFunc("GET /ui/api/history", handleHistory(rep, log))
	mux.HandleFunc("GET /ui/api/history/models", handleModelHistory(rep, log))
	mux.HandleFunc("GET /ui/api/history/hourly", handleHourlyHistory(rep, log))
	mux.HandleFunc("GET /ui/api/errors", handleErrors(rep, log))
	mux.HandleFunc("GET /ui/api/tools", handleToolCalls(rep, log))
	mux.HandleFunc("GET /ui/api/web-requests", handleWebRequests(rep, log))
	return mux
}

type sessionCounts struct {
	Started int64
	Used    int64
}

type sessionDashboard struct {
	Today    sessionCounts
	ThisWeek sessionCounts
	Daily    []report.SessionRow
	Weekly   []report.SessionRow
}

func handleSessions(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		daily, err := rep.Sessions(r.Context(), report.SessionOptions{GroupBy: "day"})
		if err != nil {
			log.Error("ui: query daily sessions", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		weekly, err := rep.Sessions(r.Context(), report.SessionOptions{GroupBy: "week"})
		if err != nil {
			log.Error("ui: query weekly sessions", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if daily == nil {
			daily = []report.SessionRow{}
		}
		if weekly == nil {
			weekly = []report.SessionRow{}
		}
		now := time.Now()
		response := sessionDashboard{Daily: daily, Weekly: weekly,
			Today:    sessionCountsForPeriod(daily, beginningOfDay(now).Format("2006-01-02")),
			ThisWeek: sessionCountsForPeriod(weekly, beginningOfWeek(now).Format("2006-01-02"))}
		writeJSON(w, response)
	}
}

func sessionCountsForPeriod(rows []report.SessionRow, period string) sessionCounts {
	var counts sessionCounts
	for _, row := range rows {
		if row.Period != period {
			continue
		}
		counts.Started += row.Started
		counts.Used += row.Used
	}
	return counts
}

func handleKPIs(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		stats, err := rep.KPIs(r.Context(), report.KPIOptions{
			Since:              since,
			Until:              since.AddDate(0, 0, 1),
			KnownProvidersOnly: true,
		})
		if err != nil {
			log.Error("ui: query KPIs", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, stats)
	}
}

func handleDirectories(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.Summary(r.Context(), report.SummaryOptions{
			Since:              since,
			Until:              since.AddDate(0, 0, 1),
			GroupBy:            "directory",
			KnownProvidersOnly: true,
		})
		if err != nil {
			log.Error("ui: query directories", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, rows)
	}
}

func handleSources(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.Summary(r.Context(), report.SummaryOptions{
			Since:              since,
			Until:              since.AddDate(0, 0, 1),
			GroupBy:            "source",
			KnownProvidersOnly: true,
		})
		if err != nil {
			log.Error("ui: query sources", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, rows)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, assets, "assets/index.html")
}

func handleSummary(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.Summary(r.Context(), report.SummaryOptions{
			Since:              since,
			Until:              since.AddDate(0, 0, 1),
			GroupBy:            "model",
			KnownProvidersOnly: true,
		})
		if err != nil {
			log.Error("ui: query summary", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, knownProviderRows(rows))
	}
}

// handleHistory serves live all-time per-day usage totals from DuckDB.
func handleHistory(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := rep.HistoricalByDay(r.Context())
		if err != nil {
			log.Error("ui: query history", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, knownProviderRows(rows))
	}
}

func handleModelHistory(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := rep.HistoricalByModel(r.Context())
		if err != nil {
			log.Error("ui: query model history", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, knownProviderRows(rows))
	}
}

func handleHourlyHistory(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentHour := beginningOfHour(time.Now())
		rows, err := rep.HourlyTokens(r.Context(), report.HourlyTokenOptions{
			Since:              currentHour.Add(-(hourlyHistoryBuckets - 1) * time.Hour),
			Until:              currentHour.Add(time.Hour),
			KnownProvidersOnly: true,
		})
		if err != nil {
			log.Error("ui: query hourly history", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, rows)
	}
}

func handleErrors(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.RecentErrorsWithin(r.Context(), since, since.AddDate(0, 0, 1), recentErrorsLimit)
		if err != nil {
			log.Error("ui: query recent errors", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.InspectRow{}
		}
		writeJSON(w, rows)
	}
}

func handleToolCalls(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.ToolCalls(r.Context(), report.ToolCallOptions{
			Since: since,
			Until: since.AddDate(0, 0, 1),
			Limit: recentToolCallsLimit,
		})
		if err != nil {
			log.Error("ui: query recent tool calls", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.ToolCallRow{}
		}
		writeJSON(w, rows)
	}
}

func handleWebRequests(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.WebRequests(r.Context(), report.ToolCallOptions{
			Since: since,
			Until: since.AddDate(0, 0, 1),
			Limit: recentWebRequestsLimit,
		})
		if err != nil {
			log.Error("ui: query web requests", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.WebRequestRow{}
		}
		writeJSON(w, rows)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// knownProviderRows keeps non-model requests (for example, curl health
// checks) out of usage views. They have no provider or token accounting and
// would otherwise appear as an "Unknown" usage card and model row.
func knownProviderRows(rows []report.SummaryRow) []report.SummaryRow {
	filtered := make([]report.SummaryRow, 0, len(rows))
	for _, row := range rows {
		if row.Provider == "unknown" {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func beginningOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func beginningOfHour(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, t.Hour(), 0, 0, 0, t.Location())
}

func beginningOfWeek(t time.Time) time.Time {
	day := beginningOfDay(t)
	daysSinceMonday := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}

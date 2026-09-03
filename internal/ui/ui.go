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
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
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
	mux.Handle("GET /ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
	mux.HandleFunc("GET /ui/", handleIndex)
	mux.Handle("GET /ui/assets/", http.StripPrefix("/ui/assets/", http.FileServerFS(staticAssets)))
	mux.HandleFunc("GET /ui/api/kpis", handleKPIs(rep, log))
	mux.HandleFunc("GET /ui/api/sessions", handleSessions(rep, log))
	mux.HandleFunc("GET /ui/api/summary", handleSummaryFor(rep, log, "summary", "source_model"))
	mux.HandleFunc("GET /ui/api/directories", handleSummaryFor(rep, log, "directories", "directory"))
	mux.HandleFunc("GET /ui/api/history", rowsHandler(log, "history", func(r *http.Request) ([]report.SummaryRow, error) {
		rows, err := rep.HistoricalByDay(r.Context())
		return knownProviderRows(rows), err
	}))
	mux.HandleFunc("GET /ui/api/history/models", rowsHandler(log, "model history", func(r *http.Request) ([]report.SummaryRow, error) {
		rows, err := rep.HistoricalByModel(r.Context())
		return knownProviderRows(rows), err
	}))
	mux.HandleFunc("GET /ui/api/history/hourly", handleHourlyHistory(rep, log))
	mux.HandleFunc("GET /ui/api/errors", rowsHandler(log, "recent errors", func(r *http.Request) ([]report.InspectRow, error) {
		since, until := today()
		return rep.RecentErrorsWithin(r.Context(), since, until, recentErrorsLimit)
	}))
	mux.HandleFunc("GET /ui/api/tools", rowsHandler(log, "recent tool calls", func(r *http.Request) ([]report.ToolCallRow, error) {
		since, until := today()
		return rep.ToolCalls(r.Context(), report.ToolCallOptions{
			Since: since, Until: until, Limit: recentToolCallsLimit,
		})
	}))
	mux.HandleFunc("GET /ui/api/web-requests", rowsHandler(log, "web requests", func(r *http.Request) ([]report.WebRequestRow, error) {
		since, until := today()
		return rep.WebRequests(r.Context(), report.ToolCallOptions{
			Since: since, Until: until, Limit: recentWebRequestsLimit,
		})
	}))
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
			Today:    sessionCountsForPeriod(daily, reporting.BeginningOfDay(now).Format("2006-01-02")),
			ThisWeek: sessionCountsForPeriod(weekly, reporting.BeginningOfWeek(now).Format("2006-01-02"))}
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
		since, until := today()
		stats, err := rep.KPIs(r.Context(), report.KPIOptions{
			Since:              since,
			Until:              until,
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

func handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, assets, "assets/index.html")
}

func rowsHandler[T any](log *slog.Logger, label string, query func(*http.Request) ([]T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := query(r)
		if err != nil {
			log.Error("ui: query "+label, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []T{}
		}
		writeJSON(w, rows)
	}
}

func today() (since, until time.Time) {
	since = reporting.BeginningOfDay(time.Now())
	return since, since.AddDate(0, 0, 1)
}

func handleSummaryFor(rep *report.Reporter, log *slog.Logger, label, groupBy string) http.HandlerFunc {
	return rowsHandler(log, label, func(r *http.Request) ([]report.SummaryRow, error) {
		since, until := today()
		rows, err := rep.Summary(r.Context(), report.SummaryOptions{
			Since:              since,
			Until:              until,
			GroupBy:            groupBy,
			KnownProvidersOnly: true,
		})
		if err != nil {
			return nil, err
		}
		return knownProviderRows(rows), nil
	})
}

func handleHourlyHistory(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentHour := reporting.BeginningOfHour(time.Now())
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

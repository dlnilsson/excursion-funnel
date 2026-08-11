// Package ui serves a minimal, read-only local dashboard summarizing usage
// telemetry recorded by the proxy. It never mutates the usage ledger.
package ui

import (
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

//go:embed assets/index.html
var assets embed.FS

const recentErrorsLimit = 20

const recentToolCallsLimit = 50

// New builds the dashboard handler, rooted at /ui/.
func New(rep *report.Reporter, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/", handleIndex)
	mux.HandleFunc("GET /ui/api/summary", handleSummary(rep, log))
	mux.HandleFunc("GET /ui/api/history", handleHistory(rep, log))
	mux.HandleFunc("GET /ui/api/history/models", handleModelHistory(rep, log))
	mux.HandleFunc("GET /ui/api/errors", handleErrors(rep, log))
	mux.HandleFunc("GET /ui/api/tools", handleToolCalls(rep, log))
	return mux
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, assets, "assets/index.html")
}

func handleSummary(rep *report.Reporter, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := beginningOfDay(time.Now())
		rows, err := rep.Summary(r.Context(), report.SummaryOptions{
			Since:   since,
			Until:   since.AddDate(0, 0, 1),
			GroupBy: "model",
		})
		if err != nil {
			log.Error("ui: query summary", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []report.SummaryRow{}
		}
		writeJSON(w, rows)
	}
}

// handleHistory serves all-time per-day usage totals from the materialized
// aggregate tables. Unlike summary it reads the pre-rolled tables rather than
// scanning requests, so it is only as fresh as the last aggregate refresh.
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
		writeJSON(w, rows)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func beginningOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

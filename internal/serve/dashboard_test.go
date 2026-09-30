package serve

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
	"github.com/dlnilsson/excursion-funnel/internal/ui"
)

// countingDashboard returns a hubDashboard whose Reporter comes from a fresh
// local store on each open, and pointers to the open count and latest store.
func countingDashboard(t *testing.T, maxAge time.Duration) (*hubDashboard, *int, **store.Store) {
	t.Helper()
	var (
		opens  int
		latest *store.Store
		log    = slog.New(slog.DiscardHandler)
	)
	d := &hubDashboard{
		open: func() (*report.Reporter, error) {
			opens++
			st, err := store.Open(filepath.Join(t.TempDir(), "usage.duckdb"))
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = st.Close() })
			latest = st
			return report.New(st), nil
		},
		maxAge: maxAge,
		log:    log,
		static: ui.New(nil, log),
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, &opens, &latest
}

func getStatus(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec.Code
}

func TestHubDashboardReusesReporter(t *testing.T) {
	d, opens, _ := countingDashboard(t, time.Hour)
	for _, path := range []string{"/ui/", "/ui/api/kpis", "/ui/api/summary", "/ui/api/history"} {
		if got := getStatus(t, d, path); got != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, got)
		}
	}
	if *opens != 1 {
		t.Fatalf("opens = %d, want 1", *opens)
	}
}

func TestHubDashboardReopensAfterServerError(t *testing.T) {
	d, opens, latest := countingDashboard(t, time.Hour)
	if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusOK {
		t.Fatalf("first status = %d, want 200", got)
	}
	// A closed store fails its queries, as a restarted hub does.
	_ = (*latest).Close()
	if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusInternalServerError {
		t.Fatalf("broken status = %d, want 500", got)
	}
	if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusOK {
		t.Fatalf("recovered status = %d, want 200", got)
	}
	if *opens != 2 {
		t.Fatalf("opens = %d, want 2", *opens)
	}
}

func TestHubDashboardReopensAfterMaxAge(t *testing.T) {
	d, opens, _ := countingDashboard(t, time.Hour)
	if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusOK {
		t.Fatalf("first status = %d, want 200", got)
	}

	d.mu.Lock()
	d.opened = time.Now().Add(-2 * time.Hour)
	d.mu.Unlock()

	if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusOK {
		t.Fatalf("reopened status = %d, want 200", got)
	}
	if *opens != 2 {
		t.Fatalf("opens = %d, want 2", *opens)
	}
}

func TestHubDashboardUsesImmediatelyExpiredReporter(t *testing.T) {
	d, opens, _ := countingDashboard(t, 0)
	open := d.open
	d.open = func() (*report.Reporter, error) {
		if *opens >= 2 {
			return nil, errors.New("reporter opened more than expected")
		}
		return open()
	}
	for i := range 2 {
		if got := getStatus(t, d, "/ui/api/kpis"); got != http.StatusOK {
			t.Fatalf("status = %d, want 200", got)
		}
		if *opens != i+1 {
			t.Fatalf("opens = %d, want %d", *opens, i+1)
		}
	}
}

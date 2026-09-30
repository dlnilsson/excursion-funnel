package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/testutil"
)

func TestRunHTTPServerStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := runHTTPServer(ctx, "127.0.0.1:0", handler, time.Second, log, "test server"); err != nil {
		t.Fatal(err)
	}
	testutil.AssertNoGoroutineLeaks(t, "internal/serve.runHTTPServer.func")
}

func TestRemoteDashboardServesStaticRoutesWithoutHub(t *testing.T) {
	cfg := config.Config{
		HubAddr: "127.0.0.1:1",
		HubKey:  filepath.Join(t.TempDir(), "missing_key"),
	}
	handler := newHubDashboard(cfg, slog.New(slog.DiscardHandler))

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/ui/", http.StatusOK},
		{"/ui/assets/favicon.svg", http.StatusOK},
		{"/ui/api/version", http.StatusOK},
		{"/ui/api/kpis", http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("GET %s status = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

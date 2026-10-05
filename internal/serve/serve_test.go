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

func TestRemoteDashboardRedirectsWithoutHub(t *testing.T) {
	for _, tc := range []struct {
		name     string
		address  string
		insecure bool
		want     string
	}{
		{"tls", "homebox.tail588fb8.ts.net:9494", false, "https://homebox.tail588fb8.ts.net:8788/ui/"},
		{"insecure", "127.0.0.1:9494", true, "http://127.0.0.1:8788/ui/"},
		{"quack URI", "quack://hub.example.test:9494", false, "https://hub.example.test:8788/ui/"},
		{"quack address", " quack:hub.example.test:9494 ", false, "https://hub.example.test:8788/ui/"},
		{"ipv6", "[::1]:9494", true, "http://[::1]:8788/ui/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				HubAddr: tc.address, HubInsecure: tc.insecure,
				HubKey: filepath.Join(t.TempDir(), "missing_key"),
			}
			handler, err := newHubDashboard(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/ui", "/ui/", "/ui/api/kpis"} {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				if rec.Code != http.StatusFound {
					t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusFound)
				}
				if got := rec.Header().Get("Location"); got != tc.want {
					t.Errorf("GET %s Location = %q, want %q", path, got, tc.want)
				}
			}
		})
	}
}

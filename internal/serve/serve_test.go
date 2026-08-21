package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

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

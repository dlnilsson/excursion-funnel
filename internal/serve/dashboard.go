package serve

import (
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/ui"
)

// hubReporterMaxAge bounds how long one hub attachment is reused. Quack
// authenticates only at ATTACH, so without a bound an attachment would outlive
// the hub session that opened it (and any revocation of the client's key).
// It matches the hub's session lifetime.
const hubReporterMaxAge = 5 * time.Minute

// hubDashboard serves the dashboard from one hub Reporter shared across
// requests, instead of attaching to the hub for every request. The Reporter
// is reopened once it reaches maxAge or after a request fails with a server
// error, since a restarted hub leaves existing attachments unusable.
type hubDashboard struct {
	open   func() (*report.Reporter, error)
	maxAge time.Duration
	log    *slog.Logger
	static http.Handler

	// Requests hold mu for their duration: a read lock on the fast path,
	// or a write lock after checking whether the Reporter needs replacing.
	mu       sync.RWMutex
	reporter *report.Reporter
	handler  http.Handler
	opened   time.Time
	stale    atomic.Bool
}

func newHubDashboard(cfg config.Config, log *slog.Logger) *hubDashboard {
	return &hubDashboard{
		open: func() (*report.Reporter, error) {
			return report.OpenHubRemote(cfg.HubAddr, cfg.HubKey, cfg.HubInsecure)
		},
		maxAge: hubReporterMaxAge,
		log:    log,
		static: ui.New(nil, log),
	}
}

func (d *hubDashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ui.NeedsReporter(r.URL.Path) {
		d.static.ServeHTTP(w, r)
		return
	}
	handler, release, err := d.acquire()
	if err != nil {
		d.log.Warn("hub dashboard unavailable", "err", err)
		http.Error(w, "hub unavailable; usage is still being spooled: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer release()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	handler.ServeHTTP(rec, r)
	if rec.status >= http.StatusInternalServerError {
		d.stale.Store(true)
	}
}

// acquire returns a locked dashboard handler and its release function, opening
// a new Reporter first when there is none or the current one is expired or
// stale. A newly opened Reporter is used without rechecking its age.
func (d *hubDashboard) acquire() (http.Handler, func(), error) {
	d.mu.RLock()
	if d.usable() {
		return d.handler, d.mu.RUnlock, nil
	}
	d.mu.RUnlock()

	d.mu.Lock()
	if !d.usable() {
		if err := d.closeReporter(); err != nil {
			d.log.Warn("close hub dashboard reporter", "err", err)
		}
		reporter, err := d.open()
		if err != nil {
			d.mu.Unlock()
			return nil, nil, err
		}
		d.reporter, d.handler, d.opened = reporter, ui.New(reporter, d.log), time.Now()
		d.stale.Store(false)
	}
	return d.handler, d.mu.Unlock, nil
}

func (d *hubDashboard) usable() bool {
	return d.handler != nil && !d.stale.Load() && time.Since(d.opened) < d.maxAge
}

// closeReporter closes the current Reporter. d.mu must be write-held.
func (d *hubDashboard) closeReporter() error {
	if d.reporter == nil {
		return nil
	}
	err := d.reporter.Close()
	d.reporter, d.handler = nil, nil
	return err
}

// Close releases the shared hub Reporter.
func (d *hubDashboard) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closeReporter()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

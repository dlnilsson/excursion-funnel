// Package serve runs the local usage-telemetry proxy service.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/daemon"
	"github.com/dlnilsson/excursion-funnel/internal/forward"
	"github.com/dlnilsson/excursion-funnel/internal/proxy"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/safelog"
	"github.com/dlnilsson/excursion-funnel/internal/store"
	"github.com/dlnilsson/excursion-funnel/internal/ui"
	"github.com/dlnilsson/excursion-funnel/internal/version"
)

// Run starts the proxy and blocks until its context is cancelled or a server
// failure occurs.
func Run(ctx context.Context, cfg config.Config, out io.Writer) error {
	log := safelog.New(out)
	version.LogStartup(log, "serve", version.Current())

	var (
		writer    queue.Writer
		dashboard http.Handler
		mode      string
	)
	if cfg.HubAddr != "" {
		mode = "distributed"
		outbox, err := store.OpenOutbox(cfg.OutboxPath)
		if err != nil {
			return fmt.Errorf("open usage outbox: %w", err)
		}
		defer func() {
			if err := outbox.Close(); err != nil {
				log.Error("close usage outbox", "err", err)
			}
		}()
		forwarder := forward.New(outbox, forward.Config{
			Address: cfg.HubAddr, KeyPath: cfg.HubKey,
			Insecure: cfg.HubInsecure, PollInterval: cfg.ForwardInterval,
		}, log)
		forwarder.Start()
		defer forwarder.Stop()
		writer = outbox
		if cfg.UIEnabled {
			dashboard = remoteDashboard(cfg, log)
		}
	} else {
		mode = "standalone"
		st, err := store.Open(cfg.DBPath)
		if err != nil {
			return fmt.Errorf("open usage store: %w", err)
		}
		defer func() {
			if err := st.Close(); err != nil {
				log.Error("close usage store", "err", err)
			}
		}()
		if err := daemon.RunRetention(ctx, st, cfg, log); err != nil {
			return err
		}
		server, err := st.StartQuack(ctx, cfg.QuackAddr, store.LocalQuackToken, false)
		if err != nil {
			return err
		}
		log.Info("local Quack endpoint ready", "uri", server.URI, "url", server.URL)
		writer = st
		if cfg.UIEnabled {
			dashboard = ui.New(report.New(st), log)
		}
	}

	usageQueue := queue.New(cfg.Queue, writer, log)
	usageQueue.Start()
	defer func() {
		if err := usageQueue.Close(cfg.QueueDrainTimeout); err != nil {
			log.Warn("usage queue drain timed out", "err", err, "dropped_total", usageQueue.Dropped())
		}
	}()

	p, err := proxy.NewWithOptions(cfg.OpenAIUpstream, cfg.AnthropicUpstream, usageQueue, log, proxy.Options{
		RequestTimeout:   cfg.RequestTimeout,
		IdleWriteTimeout: cfg.IdleTimeout,
		Source:           cfg.Source,
		Host:             cfg.Host,
		WebProxyEnabled:  cfg.WebProxyEnabled,
	})
	if err != nil {
		return err
	}

	handler := p.Handler()
	if dashboard != nil {
		proxyHandler := p.Handler()
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodConnect && !r.URL.IsAbs() && strings.HasPrefix(r.URL.Path, "/ui/") {
				dashboard.ServeHTTP(w, r)
				return
			}
			proxyHandler.ServeHTTP(w, r)
		})
	}
	log.Info("excursion-funnel configured", "mode", mode, "source", cfg.Source,
		"openai_upstream", cfg.OpenAIUpstream, "anthropic_upstream", cfg.AnthropicUpstream,
		"request_timeout", cfg.RequestTimeout, "idle_timeout", cfg.IdleTimeout, "ui_enabled", cfg.UIEnabled)
	return runHTTPServer(ctx, cfg.Addr, handler, cfg.ShutdownTimeout, log, "excursion-funnel serving")
}

func remoteDashboard(cfg config.Config, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reporter, err := report.OpenHubRemote(cfg.HubAddr, cfg.HubKey, cfg.HubInsecure)
		if err != nil {
			log.Warn("hub dashboard unavailable", "err", err)
			http.Error(w, "hub unavailable; usage is still being spooled: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer reporter.Close()
		ui.New(reporter, log).ServeHTTP(w, r)
	})
}

func runHTTPServer(ctx context.Context, addr string, handler http.Handler, shutdownTimeout time.Duration, log *slog.Logger, message string) error {
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 15 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info(message, "addr", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

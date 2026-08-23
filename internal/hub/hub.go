// Package hub runs the shared authenticated usage hub.
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/daemon"
	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/safelog"
	"github.com/dlnilsson/excursion-funnel/internal/store"
	"github.com/dlnilsson/excursion-funnel/internal/ui"
	"github.com/dlnilsson/excursion-funnel/internal/version"
	proxyproto "github.com/pires/go-proxyproto"
)

// Run starts the hub and blocks until its context is cancelled or a server
// failure occurs.
func Run(ctx context.Context, cfg config.Config, out io.Writer) error {
	if cfg.HubAddr == "" {
		return errors.New("hub-addr is required (for example --hub-addr 127.0.0.1:9494 behind a TLS proxy)")
	}
	if err := requireLoopbackAddr(cfg.HubAddr); err != nil {
		return fmt.Errorf("hub-addr: %w", err)
	}
	if cfg.HubAuthorizedKeys == "" {
		return errors.New("hub-authorized-keys is required")
	}
	if len(cfg.HubToken) < 4 {
		return errors.New("hub-token must contain at least 4 characters")
	}
	if err := requireLoopbackAddr(cfg.HubQuackAddr); err != nil {
		return fmt.Errorf("hub-quack-addr: %w", err)
	}

	log := safelog.New(out)
	version.LogStartup(log, "hub", version.Current())
	allowed, err := hubauth.LoadAuthorizedKeys(cfg.HubAuthorizedKeys)
	if err != nil {
		return err
	}
	auth := hubauth.NewHubWithLogger(allowed, log)
	defer auth.Close()
	st, err := store.OpenHub(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open hub ledger: %w", err)
	}
	defer st.Close()
	if err := daemon.RunRetention(ctx, st, cfg, log); err != nil {
		return err
	}
	stopMerge := daemon.StartStagingMerge(st, cfg.MergeInterval, log)
	defer stopMerge()
	server, err := st.StartQuackAuthenticated(ctx, cfg.HubQuackAddr, cfg.HubToken, auth.ValidateSession)
	if err != nil {
		return err
	}
	log.Info("hub internal Quack endpoint ready", "uri", server.URI, "url", server.URL)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if cfg.UIEnabled {
		mux.Handle("/ui/", ui.New(report.New(st), log))
	}
	upstream, err := url.Parse("http://" + cfg.HubQuackAddr)
	if err != nil {
		return fmt.Errorf("parse hub Quack address: %w", err)
	}
	quackProxy := httputil.NewSingleHostReverseProxy(upstream)
	authHandler := auth.Handler()
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/challenge") || (r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth") {
			authHandler.ServeHTTP(w, r)
			return
		}
		quackProxy.ServeHTTP(w, r)
	})
	return runServers(ctx, cfg.Addr, mux, cfg.HubAddr, gateway, cfg.ShutdownTimeout, log)
}

func requireLoopbackAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve address: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("does not resolve")
	}
	for _, ip := range addresses {
		if !ip.IsLoopback() {
			return errors.New("must resolve only to loopback")
		}
	}
	return nil
}

func runServers(ctx context.Context, dashboardAddr string, dashboard http.Handler, gatewayAddr string, gateway http.Handler, timeout time.Duration, log *slog.Logger) error {
	dashboardListener, err := net.Listen("tcp", dashboardAddr)
	if err != nil {
		return err
	}
	defer dashboardListener.Close()
	rawGatewayListener, err := net.Listen("tcp", gatewayAddr)
	if err != nil {
		return err
	}
	gatewayListener := newGatewayListener(rawGatewayListener)
	defer gatewayListener.Close()
	dashboardServer := &http.Server{Handler: dashboard, ReadHeaderTimeout: 15 * time.Second}
	gatewayServer := &http.Server{Handler: gateway, ReadHeaderTimeout: 15 * time.Second}
	errCh := make(chan error, 2)
	go func() {
		log.Info("excursion-funnel hub dashboard serving", "addr", dashboardListener.Addr())
		if err := dashboardServer.Serve(dashboardListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		log.Info("excursion-funnel hub gateway serving", "addr", gatewayListener.Addr(), "proxy_protocol", "required")
		if err := gatewayServer.Serve(gatewayListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err1 := dashboardServer.Shutdown(shutdownCtx)
	err2 := gatewayServer.Shutdown(shutdownCtx)
	return errors.Join(err1, err2)
}

func newGatewayListener(listener net.Listener) net.Listener {
	return &proxyproto.Listener{
		Listener: listener,
		ConnPolicy: proxyproto.TrustProxyHeaderFrom(
			net.IPv4(127, 0, 0, 1),
			net.IPv6loopback,
		),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

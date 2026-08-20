// Command excursion-funnel is a local usage-telemetry proxy for Codex.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/forward"
	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
	"github.com/dlnilsson/excursion-funnel/internal/proxy"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/store"
	"github.com/dlnilsson/excursion-funnel/internal/ui"
	"github.com/dlnilsson/excursion-funnel/internal/version"
	proxyproto "github.com/pires/go-proxyproto"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			slog.Error("serve failed", "err", err)
			os.Exit(1)
		}
	case "hub":
		if err := runHub(os.Args[2:]); err != nil {
			slog.Error("hub failed", "err", err)
			os.Exit(1)
		}
	case "version":
		if err := runVersion(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "version: %v\n", err)
			os.Exit(1)
		}
	case "migrate":
		if err := runMigrate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
	case "usage":
		if err := runUsage(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "usage: %v\n", err)
			os.Exit(1)
		}
	case "tools":
		if err := runTools(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "tools: %v\n", err)
			os.Exit(1)
		}
	case "inspect":
		if err := runInspect(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "inspect: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// errTodayWithRange keeps `usage today --since ...` from silently reporting
// today and discarding the range the caller asked for.
var errTodayWithRange = errors.New("`usage today` cannot be combined with --since/--until; drop `today` to use an explicit range")

func runUsage(args []string) error {
	return runUsageTo(args, os.Stdout)
}

func runUsageTo(args []string, out io.Writer) error {
	var today bool
	if len(args) > 0 && args[0] == "today" {
		today = true
		args = args[1:]
	}

	var (
		dbPath    = defaultReportDBPath()
		sinceS    string
		untilS    string
		groupBy   string
		directory string
		branch    string
		hubKey    string
		jsonOut   bool
	)
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&dbPath, "db", dbPath, "DuckDB ledger path")
	fs.StringVar(&sinceS, "since", "", "start date, inclusive (YYYY-MM-DD)")
	fs.StringVar(&untilS, "until", "", "end date, inclusive (YYYY-MM-DD)")
	fs.StringVar(&groupBy, "group-by", "model", "grouping: model, provider, day, source, directory, or git_branch")
	fs.StringVar(&directory, "directory", "", "only requests from this working directory")
	fs.StringVar(&branch, "branch", "", "only requests from this git branch")
	fs.StringVar(&hubKey, "hub-key", os.Getenv("EF_HUB_KEY"), "Ed25519 private key for remote hub authentication")
	fs.BoolVar(&jsonOut, "json", false, "print the summary as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	if today && (sinceS != "" || untilS != "") {
		return errTodayWithRange
	}

	now := time.Now()
	var since, until time.Time
	if today || (sinceS == "" && untilS == "") {
		since = beginningOfDay(now)
		until = since.AddDate(0, 0, 1)
	} else {
		var err error
		if sinceS != "" {
			since, err = parseReportDate(sinceS)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
		}
		if untilS != "" {
			until, err = parseReportDate(untilS)
			if err != nil {
				return fmt.Errorf("--until: %w", err)
			}
			until = until.AddDate(0, 0, 1)
		}
	}

	r, err := report.OpenWithHubKey(dbPath, defaultReportDBPath(), hubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer r.Close()

	rows, err := r.Summary(context.Background(), report.SummaryOptions{
		Since:     since,
		Until:     until,
		GroupBy:   groupBy,
		Directory: directory,
		Branch:    branch,
	})
	if err != nil {
		return err
	}

	// A single-day interval (`today`, the no-range default, or an explicit
	// one-day range) means every row belongs to that one date. Grouping by
	// model/provider otherwise leaves Day empty; stamp it so the output carries
	// the date instead of "". Day-grouped queries already fill Day per row.
	if groupBy != "day" && !since.IsZero() && until.Equal(since.AddDate(0, 0, 1)) {
		day := since.Format("2006-01-02")
		for i := range rows {
			rows[i].Day = day
		}
	}

	if jsonOut {
		return printUsageJSON(out, rows)
	}
	printUsageRows(out, rows, groupBy)
	return nil
}

func runTools(args []string) error {
	return runToolsTo(args, os.Stdout)
}

func runToolsTo(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "today" {
		return fmt.Errorf("usage: ef tools today [--db path] [--limit n] [--json]")
	}
	args = args[1:]

	var (
		dbPath  = defaultReportDBPath()
		limit   = report.DefaultRecentToolCallLimit
		hubKey  string
		jsonOut bool
	)
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&dbPath, "db", dbPath, "DuckDB ledger path")
	fs.IntVar(&limit, "limit", limit, "maximum tool calls to print")
	fs.StringVar(&hubKey, "hub-key", os.Getenv("EF_HUB_KEY"), "Ed25519 private key for remote hub authentication")
	fs.BoolVar(&jsonOut, "json", false, "print tool calls as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", limit)
	}

	now := time.Now()
	since := beginningOfDay(now)
	r, err := report.OpenWithHubKey(dbPath, defaultReportDBPath(), hubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer r.Close()

	rows, err := r.ToolCalls(context.Background(), report.ToolCallOptions{
		Since: since,
		Until: since.AddDate(0, 0, 1),
		Limit: limit,
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return printToolJSON(out, rows)
	}
	printToolRows(out, rows)
	return nil
}

func runInspect(args []string) error {
	var (
		dbPath = defaultReportDBPath()
		limit  int
		hubKey string
	)
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&dbPath, "db", dbPath, "DuckDB ledger path")
	fs.IntVar(&limit, "limit", report.DefaultInspectLimit, "maximum matching requests to print")
	fs.StringVar(&hubKey, "hub-key", os.Getenv("EF_HUB_KEY"), "Ed25519 private key for remote hub authentication")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ef inspect [--db path] [--limit n] request_or_response_id")
	}
	if limit <= 0 {
		return fmt.Errorf("--limit must be positive, got %d", limit)
	}

	r, err := report.OpenWithHubKey(dbPath, defaultReportDBPath(), hubKey)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer r.Close()

	// Ask for one extra row so a saturated result is distinguishable from an
	// exact fit, and say so rather than silently truncating.
	rows, err := r.Inspect(context.Background(), fs.Arg(0), limit+1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "no matching requests")
		return nil
	}

	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	printInspectRows(rows)
	if truncated {
		fmt.Fprintf(os.Stdout, "\nshowing the %d most recent matches; pass --limit to see more\n", limit)
	}
	return nil
}

func runServe(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	log := newLogger(os.Stdout)
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
		if err := runRetention(st, cfg, log); err != nil {
			return err
		}
		server, err := st.StartQuack(context.Background(), cfg.QuackAddr, store.LocalQuackToken, false)
		if err != nil {
			return err
		}
		log.Info("local Quack endpoint ready", "uri", server.URI, "url", server.URL)
		writer = st
		if cfg.UIEnabled {
			dashboard = ui.New(report.New(st), log)
		}
	}

	q := queue.New(cfg.Queue, writer, log)
	q.Start()
	defer func() {
		if err := q.Close(cfg.QueueDrainTimeout); err != nil {
			log.Warn("usage queue drain timed out", "err", err, "dropped_total", q.Dropped())
		}
	}()

	p, err := proxy.NewWithOptions(cfg.OpenAIUpstream, cfg.AnthropicUpstream, q, log, proxy.Options{
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
		// A ServeMux cannot receive CONNECT (authority-form target, empty path)
		// or classify absolute-form URLs, so dispatch forward-proxy traffic to
		// the proxy handler ahead of any path matching; only origin-form /ui/
		// requests go to the dashboard.
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
	return runHTTPServer(cfg.Addr, handler, cfg.ShutdownTimeout, log, "excursion-funnel serving")
}

func runHub(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
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
	log := newLogger(os.Stdout)
	version.LogStartup(log, "hub", version.Current())
	allowed, err := hubauth.LoadAuthorizedKeys(cfg.HubAuthorizedKeys)
	if err != nil {
		return err
	}
	auth := hubauth.NewHubWithLogger(allowed, log)
	defer auth.Close()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open hub ledger: %w", err)
	}
	defer st.Close()
	if err := runRetention(st, cfg, log); err != nil {
		return err
	}
	server, err := st.StartQuackAuthenticated(context.Background(), cfg.HubQuackAddr, cfg.HubToken, auth.ValidateSession)
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
	return runHubServers(cfg.Addr, mux, cfg.HubAddr, gateway, cfg.ShutdownTimeout, log)
}

func requireLoopbackAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve address: %w", err)
	}
	if len(addrs) == 0 {
		return errors.New("does not resolve")
	}
	for _, ip := range addrs {
		if !ip.IsLoopback() {
			return errors.New("must resolve only to loopback")
		}
	}
	return nil
}

func runHubServers(dashboardAddr string, dashboard http.Handler, gatewayAddr string, gateway http.Handler, timeout time.Duration, log *slog.Logger) error {
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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

func runMigrate(args []string) error {
	dbPath := defaultReportDBPath()
	from := filepath.Join(filepath.Dir(dbPath), "usage.sqlite")
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.StringVar(&dbPath, "db", dbPath, "destination DuckDB ledger path")
	fs.StringVar(&from, "from", from, "legacy SQLite ledger path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if _, err := os.Stat(from); err != nil {
		return fmt.Errorf("legacy ledger %s: %w", from, err)
	}
	if err := store.MigrateSQLite(context.Background(), dbPath, from); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "migrated %s to %s\n", from, dbPath)
	return nil
}

func runVersion(args []string) error {
	return version.Run(args, os.Stdout)
}

func runRetention(st *store.Store, cfg config.Config, log *slog.Logger) error {
	if cfg.RetentionDays <= 0 {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	deleted, err := st.DeleteRequestsStartedBefore(cleanupCtx, cutoff)
	if err != nil {
		return fmt.Errorf("retention cleanup: %w", err)
	}
	log.Info("retention cleanup complete", "retention_days", cfg.RetentionDays, "deleted", deleted)
	return nil
}

func remoteDashboard(cfg config.Config, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep, err := report.OpenHubRemote(cfg.HubAddr, cfg.HubKey, cfg.HubInsecure)
		if err != nil {
			log.Warn("hub dashboard unavailable", "err", err)
			http.Error(w, "hub unavailable; usage is still being spooled", http.StatusServiceUnavailable)
			return
		}
		defer rep.Close()
		ui.New(rep, log).ServeHTTP(w, r)
	})
}

func runHTTPServer(addr string, handler http.Handler, shutdownTimeout time.Duration, log *slog.Logger, message string) error {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 15 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Info(message, "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	return srv.Shutdown(shutdownCtx)
}

func usage() {
	fmt.Fprint(os.Stderr, `ef — local usage-telemetry proxy for Codex and Claude Code

Usage:
  ef version
  ef serve [--addr host:port] [--openai-upstream url] [--anthropic-upstream url] [--db path] [--ui-enabled]
  ef hub --hub-addr host:port --hub-authorized-keys path --hub-token token [--hub-quack-addr 127.0.0.1:9495] [--addr dashboard-host:port] [--db path]
  ef migrate [--from usage.sqlite] [--db usage.duckdb]
  ef usage today [--db path] [--group-by model|provider|day|source|directory|git_branch] [--directory path] [--branch name] [--json]
  ef usage --since YYYY-MM-DD [--until YYYY-MM-DD] [--group-by model|provider|day|source|directory|git_branch] [--directory path] [--branch name] [--db path] [--json]
  ef tools today [--db path] [--limit n] [--json]
  ef inspect [--db path] [--limit n] request_or_response_id

The usage, tools, and inspect commands query a configured hub or running local
daemon through Quack, then fall back to a read-only DuckDB file when possible.
--since/--until are inclusive dates, local time.

Point clients at the daemon:
  Codex:        base_url = "http://127.0.0.1:8787/v1"   (wire_api = "responses")
  Claude Code:  ANTHROPIC_BASE_URL = "http://127.0.0.1:8787"

Dashboard: http://<addr>/ui/ (read-only usage view; enabled by default, disable with --ui-enabled=false)

Env overrides: EF_ADDR, EF_OPENAI_UPSTREAM,
               EF_ANTHROPIC_UPSTREAM, EF_DB, EF_OUTBOX,
               EF_QUACK_ADDR, EF_HUB_ADDR, EF_HUB_KEY,
               EF_HUB_TOKEN, EF_HUB_AUTHORIZED_KEYS, EF_HUB_QUACK_ADDR,
               EF_HUB_INSECURE, EF_SOURCE,
               EF_REQUEST_TIMEOUT, EF_IDLE_TIMEOUT,
               EF_SHUTDOWN_TIMEOUT,
               EF_QUEUE_DRAIN_TIMEOUT,
               EF_FORWARD_INTERVAL,
               EF_RETENTION_DAYS,
               EF_UI_ENABLED
`)
}

func newLogger(out io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			key := strings.ToLower(a.Key)
			if secretLikeKey(key) {
				a.Value = slog.StringValue("[REDACTED]")
				return a
			}
			if a.Value.Kind() == slog.KindString {
				a.Value = slog.StringValue(redactSecretLikeValue(a.Value.String()))
			}
			if a.Value.Kind() == slog.KindAny && a.Value.Any() != nil && a.Key == "err" {
				a.Value = slog.StringValue(redactSecretLikeValue(fmt.Sprint(a.Value.Any())))
			}
			return a
		},
	}))
}

func secretLikeKey(key string) bool {
	for _, marker := range []string{"authorization", "api_key", "apikey", "x-api-key", "token", "secret", "password", "cookie"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func redactSecretLikeValue(s string) string {
	lower := strings.ToLower(s)
	for _, marker := range []string{"authorization:", "bearer ", "x-api-key", "api_key=", "apikey=", "access_token=", "token=", "secret=", "password="} {
		if strings.Contains(lower, marker) {
			return "[REDACTED]"
		}
	}
	return s
}

func defaultReportDBPath() string {
	cfg := config.Default()
	if v := os.Getenv("EF_DB"); v != "" {
		cfg.DBPath = v
	}
	return cfg.DBPath
}

func parseReportDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return t, nil
}

func beginningOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func printUsageJSON(out io.Writer, rows []report.SummaryRow) error {
	if rows == nil {
		rows = []report.SummaryRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func printUsageRows(out io.Writer, rows []report.SummaryRow, groupBy string) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no usage rows")
		return
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if groupBy == "day" {
		fmt.Fprintln(w, "DAY\tPROVIDER\tCLIENT\tMODEL\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.Day, r.Provider, r.Client, r.Model, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	} else if groupBy == "provider" {
		fmt.Fprintln(w, "PROVIDER\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.Provider, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	} else if groupBy == "source" {
		fmt.Fprintln(w, "SOURCE\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.Source, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	} else if groupBy == "directory" {
		fmt.Fprintln(w, "DIRECTORY\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.Directory, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	} else if groupBy == "git_branch" {
		fmt.Fprintln(w, "GIT_BRANCH\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.GitBranch, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	} else {
		fmt.Fprintln(w, "PROVIDER\tCLIENT\tMODEL\tREQ\tERR\tINPUT\tCACHED\tCACHE_WRITE\tOUTPUT\tREASONING\tTOTAL")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				r.Provider, r.Client, r.Model, r.Requests, r.Errors, r.FreshInput, r.Cached, r.CacheWrite, r.Output, r.Reasoning, r.Total)
		}
	}
	_ = w.Flush()
}

func printToolRows(out io.Writer, rows []report.ToolCallRow) {
	if len(rows) == 0 {
		fmt.Fprintln(out, "no tool calls recorded today")
		return
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STARTED\tCLIENT\tMODEL\tTOOL\tDESCRIPTION\tCOMMAND")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			localTimestamp(row.StartedAt), emptyAsDash(row.Client), emptyAsDash(row.Model),
			emptyAsDash(row.Name), emptyAsDash(compactToolValue(row.Description)), emptyAsDash(compactToolValue(row.Command)))
	}
	_ = w.Flush()
}

func printToolJSON(out io.Writer, rows []report.ToolCallRow) error {
	if rows == nil {
		rows = []report.ToolCallRow{}
	}
	return json.NewEncoder(out).Encode(rows)
}

func compactToolValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ↩ ")
	value = strings.ReplaceAll(value, "\n", " ↩ ")
	return strings.ReplaceAll(value, "\r", " ↩ ")
}

func printInspectRows(rows []report.InspectRow) {
	for i, r := range rows {
		if i > 0 {
			fmt.Fprintln(os.Stdout)
		}
		fmt.Fprintf(os.Stdout, "id: %s\n", r.ID)
		if r.ResponseID != "" {
			fmt.Fprintf(os.Stdout, "response_id: %s\n", r.ResponseID)
		}
		fmt.Fprintf(os.Stdout, "source: %s\n", r.Source)
		if r.Host != "" {
			fmt.Fprintf(os.Stdout, "host: %s\n", r.Host)
		}
		fmt.Fprintf(os.Stdout, "directory: %s\n", emptyAsDash(r.Directory))
		fmt.Fprintf(os.Stdout, "git_branch: %s\n", emptyAsDash(r.GitBranch))
		fmt.Fprintf(os.Stdout, "started_at: %s\n", localTimestamp(r.StartedAt))
		if r.CompletedAt.Valid {
			fmt.Fprintf(os.Stdout, "completed_at: %s\n", localTimestamp(r.CompletedAt.Time))
		}
		if r.DurationMS.Valid {
			fmt.Fprintf(os.Stdout, "duration_ms: %d\n", r.DurationMS.Int64)
		}
		fmt.Fprintf(os.Stdout, "provider: %s\n", r.Provider)
		fmt.Fprintf(os.Stdout, "client: %s\n", emptyAsDash(r.Client))
		fmt.Fprintf(os.Stdout, "request: %s %s\n", r.Method, r.Path)
		fmt.Fprintf(os.Stdout, "upstream_url: %s\n", r.UpstreamURL)
		fmt.Fprintf(os.Stdout, "model_requested: %s\n", emptyAsDash(r.ModelRequested))
		fmt.Fprintf(os.Stdout, "model_reported: %s\n", emptyAsDash(r.ModelReported))
		fmt.Fprintf(os.Stdout, "stream: %t\n", r.Stream)
		if r.HTTPStatus.Valid {
			fmt.Fprintf(os.Stdout, "http_status: %d\n", r.HTTPStatus.Int64)
		}
		if r.UpstreamRequestID != "" {
			fmt.Fprintf(os.Stdout, "upstream_request_id: %s\n", r.UpstreamRequestID)
		}
		if r.UserAgent != "" {
			fmt.Fprintf(os.Stdout, "user_agent: %s\n", r.UserAgent)
		}
		if r.Originator != "" {
			fmt.Fprintf(os.Stdout, "originator: %s\n", r.Originator)
		}
		if r.CodexSessionID != "" {
			fmt.Fprintf(os.Stdout, "codex_session_id: %s\n", r.CodexSessionID)
		}
		if r.ErrorType != "" {
			fmt.Fprintf(os.Stdout, "error: %s %s\n", r.ErrorType, r.ErrorMessage)
		}
		fmt.Fprintf(os.Stdout, "tokens: input=%s cached=%s cache_write=%s output=%s reasoning=%s total=%s\n",
			freshInputCell(r.Provider, r.Input, r.Cached, r.CacheWrite), nullInt(r.Cached), nullInt(r.CacheWrite), nullInt(r.Output), nullInt(r.Reasoning), nullInt(r.Total))
		if strings.TrimSpace(r.UsageJSON) != "" {
			fmt.Fprintf(os.Stdout, "usage_json: %s\n", r.UsageJSON)
		}
		for _, call := range r.ToolCalls {
			fmt.Fprintf(os.Stdout, "tool_call[%d]: %s\n", call.Ordinal, call.Name)
			if call.ID != "" {
				fmt.Fprintf(os.Stdout, "  id: %s\n", call.ID)
			}
			if call.Description != "" {
				fmt.Fprintf(os.Stdout, "  description: %s\n", call.Description)
			}
			if call.Command != "" {
				fmt.Fprintf(os.Stdout, "  command: %s\n", call.Command)
			}
			if call.ArgumentsJSON != "" {
				fmt.Fprintf(os.Stdout, "  arguments_json: %s\n", call.ArgumentsJSON)
			}
		}
		for _, request := range r.WebRequests {
			fmt.Fprintf(os.Stdout, "web_request[%d]: %s\n", request.Ordinal, request.Name)
			if request.ID != "" {
				fmt.Fprintf(os.Stdout, "  id: %s\n", request.ID)
			}
			if request.Query != "" {
				fmt.Fprintf(os.Stdout, "  query: %s\n", request.Query)
			}
			if request.URL != "" {
				fmt.Fprintf(os.Stdout, "  url: %s\n", request.URL)
			}
			if request.Domain != "" {
				fmt.Fprintf(os.Stdout, "  domain: %s\n", request.Domain)
			}
			if request.ArgumentsJSON != "" {
				fmt.Fprintf(os.Stdout, "  arguments_json: %s\n", request.ArgumentsJSON)
			}
		}
	}
}

func localTimestamp(value time.Time) string {
	return value.Local().Format("2006-01-02T15:04:05.000Z07:00")
}

func emptyAsDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func nullInt(v sql.NullInt64) string {
	if !v.Valid {
		return "-"
	}
	return strconv.FormatInt(v.Int64, 10)
}

// freshInput returns uncached, non-cache-write input tokens — the "input" figure
// Claude Code's /status shows. The ledger stores Input normalized differently per
// provider: Anthropic folds both cache reads and cache writes into Input, whereas
// OpenAI's input_tokens already includes cache reads but never cache writes. So
// subtract back out only what the provider actually folded in — for a non-zero
// OpenAI cache write, unconditionally subtracting it would under-report fresh
// input. Unknown providers are treated like OpenAI (don't subtract). Clamped at 0.
func freshInput(provider string, input, cached, cacheWrite int64) int64 {
	fresh := input - cached
	if provider == "anthropic" {
		fresh -= cacheWrite
	}
	if fresh < 0 {
		return 0
	}
	return fresh
}

// freshInputCell renders freshInput for the inspect view, preserving nullInt's
// "-" placeholder when a request recorded no input tokens. Missing cached or
// cache-write counts are treated as 0.
func freshInputCell(provider string, input, cached, cacheWrite sql.NullInt64) string {
	if !input.Valid {
		return "-"
	}
	return strconv.FormatInt(freshInput(provider, input.Int64, cached.Int64, cacheWrite.Int64), 10)
}

// Package config loads daemon configuration.
//
// Precedence (highest wins): CLI flags > environment variables > config file
// > built-in defaults. The TOML config-file layer is not wired up yet; when it
// lands it sits between env and defaults. See EXCURSION_FUNNEL_PLAN.md.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// Config holds the resolved runtime configuration for `serve`.
//
// The daemon is a transparent multi-provider proxy. It picks the upstream by
// the request endpoint (/v1/responses → OpenAI, /v1/messages → Anthropic). The
// Anthropic path is forwarded verbatim, so its upstream is a bare root. The
// OpenAI path has its leading /v1 stripped before joining, so its upstream root
// must carry the correct prefix itself (…/backend-api/codex or …/v1).
type Config struct {
	Addr string

	OpenAIUpstream    string // root for Codex / Responses API, e.g. https://chatgpt.com/backend-api/codex
	AnthropicUpstream string // root for Claude Code / Messages API, e.g. https://api.anthropic.com

	DBPath            string
	OutboxPath        string
	QuackAddr         string
	HubAddr           string
	HubToken          string
	HubAuthorizedKeys string
	HubQuackAddr      string
	HubKey            string
	HubInsecure       bool
	Source            string
	Host              string
	ShutdownTimeout   time.Duration
	RequestTimeout    time.Duration
	IdleTimeout       time.Duration
	RetentionDays     int
	UIEnabled         bool
	WebProxyEnabled   bool

	Queue             queue.Config
	QueueDrainTimeout time.Duration
	ForwardInterval   time.Duration
}

const (
	// defaultOpenAIUpstream targets the ChatGPT Codex backend rather than the
	// platform API, because Codex's ChatGPT-subscription auth (auth_mode =
	// chatgpt) only carries connector scopes — never api.responses.write — so
	// those tokens are accepted here and rejected by https://api.openai.com/v1.
	// The proxy strips the client's leading /v1 for OpenAI routes, so this root
	// already includes the correct path prefix. To use a platform API key
	// instead, override with https://api.openai.com/v1.
	defaultOpenAIUpstream    = "https://chatgpt.com/backend-api/codex"
	defaultAnthropicUpstream = "https://api.anthropic.com"
)

// Default returns the built-in defaults, the lowest-precedence layer.
func Default() Config {
	return Config{
		Addr:              "127.0.0.1:8787",
		OpenAIUpstream:    defaultOpenAIUpstream,
		AnthropicUpstream: defaultAnthropicUpstream,
		DBPath:            defaultDBPath(),
		OutboxPath:        defaultOutboxPath(),
		QuackAddr:         "127.0.0.1:9494",
		HubQuackAddr:      "127.0.0.1:9495",
		HubInsecure:       false,
		Source:            defaultSource(),
		Host:              defaultHost(),
		ShutdownTimeout:   5 * time.Second,
		RequestTimeout:    2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		RetentionDays:     0,
		UIEnabled:         true,
		WebProxyEnabled:   false,
		Queue:             queue.DefaultConfig(),
		QueueDrainTimeout: 5 * time.Second,
		ForwardInterval:   time.Second,
	}
}

// Load resolves configuration from defaults, then environment, then flags.
// args is everything after the `serve` subcommand (i.e. os.Args[2:]).
func Load(args []string) (Config, error) {
	cfg := Default()

	// Environment layer.
	if v := os.Getenv("EF_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("EF_OPENAI_UPSTREAM"); v != "" {
		cfg.OpenAIUpstream = v
	}
	if v := os.Getenv("EF_ANTHROPIC_UPSTREAM"); v != "" {
		cfg.AnthropicUpstream = v
	}
	if v := os.Getenv("EF_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("EF_OUTBOX"); v != "" {
		cfg.OutboxPath = v
	}
	if v := os.Getenv("EF_QUACK_ADDR"); v != "" {
		cfg.QuackAddr = v
	}
	if v := os.Getenv("EF_HUB_ADDR"); v != "" {
		cfg.HubAddr = v
	}
	if v := os.Getenv("EF_HUB_TOKEN"); v != "" {
		cfg.HubToken = v
	}
	if v := os.Getenv("EF_HUB_AUTHORIZED_KEYS"); v != "" {
		cfg.HubAuthorizedKeys = v
	}
	if v := os.Getenv("EF_HUB_QUACK_ADDR"); v != "" {
		cfg.HubQuackAddr = v
	}
	if v := os.Getenv("EF_HUB_KEY"); v != "" {
		cfg.HubKey = v
	}
	if v := os.Getenv("EF_HUB_INSECURE"); v != "" {
		insecure, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_HUB_INSECURE: %w", err)
		}
		cfg.HubInsecure = insecure
	}
	if v := os.Getenv("EF_SOURCE"); v != "" {
		cfg.Source = v
	}
	if v := os.Getenv("EF_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if v := os.Getenv("EF_REQUEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_REQUEST_TIMEOUT: %w", err)
		}
		cfg.RequestTimeout = d
	}
	if v := os.Getenv("EF_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_IDLE_TIMEOUT: %w", err)
		}
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("EF_QUEUE_DRAIN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_QUEUE_DRAIN_TIMEOUT: %w", err)
		}
		cfg.QueueDrainTimeout = d
	}
	if v := os.Getenv("EF_FORWARD_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_FORWARD_INTERVAL: %w", err)
		}
		cfg.ForwardInterval = d
	}
	if v := os.Getenv("EF_RETENTION_DAYS"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_RETENTION_DAYS: %w", err)
		}
		cfg.RetentionDays = days
	}
	if v := os.Getenv("EF_UI_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_UI_ENABLED: %w", err)
		}
		cfg.UIEnabled = enabled
	}
	if v := os.Getenv("EF_WEB_PROXY"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("EF_WEB_PROXY: %w", err)
		}
		cfg.WebProxyEnabled = enabled
	}

	// Flag layer — seeded from post-env values so a flag only wins when set,
	// preserving flags > env > defaults.
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address (host:port)")
	fs.StringVar(&cfg.OpenAIUpstream, "openai-upstream", cfg.OpenAIUpstream, "OpenAI upstream root (Codex / Responses API)")
	fs.StringVar(&cfg.AnthropicUpstream, "anthropic-upstream", cfg.AnthropicUpstream, "Anthropic upstream root (Claude Code / Messages API)")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "DuckDB ledger path")
	fs.StringVar(&cfg.OutboxPath, "outbox", cfg.OutboxPath, "distributed-mode SQLite outbox path")
	fs.StringVar(&cfg.QuackAddr, "quack-addr", cfg.QuackAddr, "standalone Quack listen address (host:port)")
	fs.StringVar(&cfg.HubAddr, "hub-addr", cfg.HubAddr, "hub gateway address (enables distributed mode for serve)")
	fs.StringVar(&cfg.HubToken, "hub-token", cfg.HubToken, "hub-internal Quack token (hub only)")
	fs.StringVar(&cfg.HubAuthorizedKeys, "hub-authorized-keys", cfg.HubAuthorizedKeys, "OpenSSH authorized_keys file for remote hub clients (hub only)")
	fs.StringVar(&cfg.HubQuackAddr, "hub-quack-addr", cfg.HubQuackAddr, "loopback Quack listener for the hub (hub only)")
	fs.StringVar(&cfg.HubKey, "hub-key", cfg.HubKey, "preferred Ed25519 private key for hub authentication")
	fs.BoolVar(&cfg.HubInsecure, "insecure", cfg.HubInsecure, "allow an unencrypted connection to the hub; TLS is required unless this is set")
	fs.StringVar(&cfg.Source, "source", cfg.Source, "developer or machine identity stamped on usage")
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful HTTP shutdown timeout")
	fs.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "upstream response-header timeout (0 disables)")
	fs.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "idle client-write timeout while proxying responses (0 disables)")
	fs.DurationVar(&cfg.QueueDrainTimeout, "queue-drain-timeout", cfg.QueueDrainTimeout, "usage queue drain timeout during shutdown")
	fs.DurationVar(&cfg.ForwardInterval, "forward-interval", cfg.ForwardInterval, "distributed outbox polling interval")
	fs.IntVar(&cfg.RetentionDays, "retention-days", cfg.RetentionDays, "delete usage rows older than this many days at startup (0 disables)")
	fs.BoolVar(&cfg.UIEnabled, "ui-enabled", cfg.UIEnabled, "serve the read-only dashboard at /ui/")
	fs.BoolVar(&cfg.WebProxyEnabled, "web-proxy", cfg.WebProxyEnabled, "act as a forward/CONNECT proxy for non-provider web traffic (opt-in; default off)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	if cfg.RetentionDays < 0 {
		return Config{}, fmt.Errorf("retention-days must be >= 0")
	}
	if cfg.ShutdownTimeout < 0 || cfg.RequestTimeout < 0 || cfg.IdleTimeout < 0 || cfg.QueueDrainTimeout < 0 || cfg.ForwardInterval < 0 {
		return Config{}, fmt.Errorf("timeouts must be >= 0")
	}
	if cfg.Source == "" {
		return Config{}, fmt.Errorf("source must not be empty")
	}

	return cfg, nil
}

// defaultDBPath mirrors the Windows-first layout from the plan:
// %LOCALAPPDATA%\excursion-funnel\usage.duckdb, with a home-dir fallback.
func defaultDBPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			base = "."
		}
	}
	return filepath.Join(base, "excursion-funnel", "usage.duckdb")
}

// DefaultDBPath returns the platform-specific default DuckDB ledger path.
func DefaultDBPath() string { return defaultDBPath() }

func defaultOutboxPath() string {
	return filepath.Join(filepath.Dir(defaultDBPath()), "outbox.sqlite")
}

func defaultHost() string {
	host, _ := os.Hostname()
	return host
}

func defaultSource() string {
	name := os.Getenv("USERNAME")
	if name == "" {
		name = os.Getenv("USER")
	}
	host := defaultHost()
	switch {
	case name != "" && host != "":
		return name + "@" + host
	case host != "":
		return host
	case name != "":
		return name
	default:
		return "unknown"
	}
}

// Package configflags binds daemon configuration to Cobra/pflag commands.
package configflags

import (
	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/spf13/pflag"
)

// Flags binds options to built-in defaults and resolves them over environment
// configuration only when a command executes. Environment-provided secrets
// therefore never become generated help defaults.
type Flags struct {
	values config.Config
}

// New creates a command flag resolver initialized from built-in defaults.
func New() *Flags { return &Flags{values: config.Default()} }

// BindServe registers only options consumed by the serve command.
func (f *Flags) BindServe(fs *pflag.FlagSet) {
	fs.StringVar(&f.values.Addr, "addr", f.values.Addr, "listen address (host:port)")
	fs.StringVar(&f.values.OpenAIUpstream, "openai-upstream", f.values.OpenAIUpstream, "OpenAI upstream root (Codex / Responses API)")
	fs.StringVar(&f.values.AnthropicUpstream, "anthropic-upstream", f.values.AnthropicUpstream, "Anthropic upstream root (Claude Code / Messages API)")
	fs.StringVar(&f.values.DBPath, "db", f.values.DBPath, "DuckDB ledger path")
	fs.StringVar(&f.values.OutboxPath, "outbox", f.values.OutboxPath, "distributed-mode SQLite outbox path")
	fs.StringVar(&f.values.QuackAddr, "quack-addr", f.values.QuackAddr, "standalone Quack listen address (host:port)")
	fs.StringVar(&f.values.HubAddr, "hub-addr", f.values.HubAddr, "hub gateway address (enables distributed mode)")
	fs.StringVar(&f.values.HubKey, "hub-key", f.values.HubKey, "preferred Ed25519 private key for hub authentication")
	fs.BoolVar(&f.values.HubInsecure, "insecure", f.values.HubInsecure, "allow an unencrypted connection to the hub; TLS is required unless this is set")
	fs.StringVar(&f.values.Source, "source", f.values.Source, "developer or machine identity stamped on usage")
	fs.DurationVar(&f.values.ShutdownTimeout, "shutdown-timeout", f.values.ShutdownTimeout, "graceful HTTP shutdown timeout")
	fs.DurationVar(&f.values.RequestTimeout, "request-timeout", f.values.RequestTimeout, "upstream response-header timeout (0 disables)")
	fs.DurationVar(&f.values.IdleTimeout, "idle-timeout", f.values.IdleTimeout, "idle client-write timeout while proxying responses (0 disables)")
	fs.DurationVar(&f.values.QueueDrainTimeout, "queue-drain-timeout", f.values.QueueDrainTimeout, "usage queue drain timeout during shutdown")
	fs.DurationVar(&f.values.ForwardInterval, "forward-interval", f.values.ForwardInterval, "distributed outbox polling interval")
	fs.IntVar(&f.values.RetentionDays, "retention-days", f.values.RetentionDays, "delete usage rows older than this many days at startup (0 disables)")
	fs.BoolVar(&f.values.UIEnabled, "ui-enabled", f.values.UIEnabled, "serve the read-only dashboard at /ui/")
	fs.BoolVar(&f.values.WebProxyEnabled, "web-proxy", f.values.WebProxyEnabled, "act as a forward/CONNECT proxy for non-provider web traffic (opt-in; default off)")
}

// BindHub registers only options consumed by the hub command.
func (f *Flags) BindHub(fs *pflag.FlagSet) {
	fs.StringVar(&f.values.Addr, "addr", f.values.Addr, "dashboard listen address (host:port)")
	fs.StringVar(&f.values.DBPath, "db", f.values.DBPath, "DuckDB ledger path")
	fs.StringVar(&f.values.HubAddr, "hub-addr", f.values.HubAddr, "loopback hub gateway address (host:port)")
	fs.StringVar(&f.values.HubToken, "hub-token", f.values.HubToken, "hub-internal Quack token")
	fs.StringVar(&f.values.HubAuthorizedKeys, "hub-authorized-keys", f.values.HubAuthorizedKeys, "OpenSSH authorized_keys file for remote hub clients")
	fs.StringVar(&f.values.HubQuackAddr, "hub-quack-addr", f.values.HubQuackAddr, "loopback Quack listener for the hub")
	fs.DurationVar(&f.values.ShutdownTimeout, "shutdown-timeout", f.values.ShutdownTimeout, "graceful HTTP shutdown timeout")
	fs.IntVar(&f.values.RetentionDays, "retention-days", f.values.RetentionDays, "delete usage rows older than this many days at startup (0 disables)")
	fs.BoolVar(&f.values.UIEnabled, "ui-enabled", f.values.UIEnabled, "serve the read-only dashboard at /ui/")
}

// Resolve applies explicitly changed flags over built-in and environment
// configuration, preserving flags > environment > defaults precedence.
func (f *Flags) Resolve(fs *pflag.FlagSet) (config.Config, error) {
	cfg, err := config.LoadEnvironment()
	if err != nil {
		return config.Config{}, err
	}
	changed := func(name string) bool {
		flag := fs.Lookup(name)
		return flag != nil && flag.Changed
	}
	if changed("addr") {
		cfg.Addr = f.values.Addr
	}
	if changed("openai-upstream") {
		cfg.OpenAIUpstream = f.values.OpenAIUpstream
	}
	if changed("anthropic-upstream") {
		cfg.AnthropicUpstream = f.values.AnthropicUpstream
	}
	if changed("db") {
		cfg.DBPath = f.values.DBPath
	}
	if changed("outbox") {
		cfg.OutboxPath = f.values.OutboxPath
	}
	if changed("quack-addr") {
		cfg.QuackAddr = f.values.QuackAddr
	}
	if changed("hub-addr") {
		cfg.HubAddr = f.values.HubAddr
	}
	if changed("hub-token") {
		cfg.HubToken = f.values.HubToken
	}
	if changed("hub-authorized-keys") {
		cfg.HubAuthorizedKeys = f.values.HubAuthorizedKeys
	}
	if changed("hub-quack-addr") {
		cfg.HubQuackAddr = f.values.HubQuackAddr
	}
	if changed("hub-key") {
		cfg.HubKey = f.values.HubKey
	}
	if changed("insecure") {
		cfg.HubInsecure = f.values.HubInsecure
	}
	if changed("source") {
		cfg.Source = f.values.Source
	}
	if changed("shutdown-timeout") {
		cfg.ShutdownTimeout = f.values.ShutdownTimeout
	}
	if changed("request-timeout") {
		cfg.RequestTimeout = f.values.RequestTimeout
	}
	if changed("idle-timeout") {
		cfg.IdleTimeout = f.values.IdleTimeout
	}
	if changed("queue-drain-timeout") {
		cfg.QueueDrainTimeout = f.values.QueueDrainTimeout
	}
	if changed("forward-interval") {
		cfg.ForwardInterval = f.values.ForwardInterval
	}
	if changed("retention-days") {
		cfg.RetentionDays = f.values.RetentionDays
	}
	if changed("ui-enabled") {
		cfg.UIEnabled = f.values.UIEnabled
	}
	if changed("web-proxy") {
		cfg.WebProxyEnabled = f.values.WebProxyEnabled
	}
	if err := config.Validate(cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

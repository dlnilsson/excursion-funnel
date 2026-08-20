package configflags

import (
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/spf13/pflag"
)

func resolveServe(t *testing.T, args ...string) (config.Config, error) {
	t.Helper()
	flags := New()
	set := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	flags.BindServe(set)
	if err := set.Parse(args); err != nil {
		return config.Config{}, err
	}
	return flags.Resolve(set)
}

func resolveHub(t *testing.T, args ...string) (config.Config, error) {
	t.Helper()
	flags := New()
	set := pflag.NewFlagSet("hub", pflag.ContinueOnError)
	flags.BindHub(set)
	if err := set.Parse(args); err != nil {
		return config.Config{}, err
	}
	return flags.Resolve(set)
}

func TestEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv("EF_REQUEST_TIMEOUT", "3s")
	t.Setenv("EF_IDLE_TIMEOUT", "4s")
	t.Setenv("EF_QUEUE_DRAIN_TIMEOUT", "5s")
	t.Setenv("EF_FORWARD_INTERVAL", "6s")
	t.Setenv("EF_RETENTION_DAYS", "9")

	cfg, err := resolveServe(t,
		"--request-timeout", "30s",
		"--idle-timeout", "40s",
		"--retention-days", "14",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestTimeout != 30*time.Second || cfg.IdleTimeout != 40*time.Second {
		t.Fatalf("explicit timeout flags not applied: %+v", cfg)
	}
	if cfg.QueueDrainTimeout != 5*time.Second || cfg.ForwardInterval != 6*time.Second {
		t.Fatalf("environment timeout values not applied: %+v", cfg)
	}
	if cfg.RetentionDays != 14 {
		t.Fatalf("RetentionDays = %d, want flag value 14", cfg.RetentionDays)
	}
}

func TestBooleanEnvironmentAndFlagPrecedence(t *testing.T) {
	t.Setenv("EF_UI_ENABLED", "false")
	t.Setenv("EF_HUB_INSECURE", "true")
	t.Setenv("EF_WEB_PROXY", "true")

	cfg, err := resolveServe(t, "--ui-enabled=true", "--insecure=false", "--web-proxy=false")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UIEnabled || cfg.HubInsecure || cfg.WebProxyEnabled {
		t.Fatalf("explicit boolean flags did not override environment: %+v", cfg)
	}
}

func TestDefaults(t *testing.T) {
	for _, key := range []string{"EF_UI_ENABLED", "EF_HUB_INSECURE", "EF_WEB_PROXY"} {
		t.Setenv(key, "")
	}
	cfg, err := resolveServe(t)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UIEnabled || cfg.HubInsecure || cfg.WebProxyEnabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestHubSettings(t *testing.T) {
	t.Setenv("EF_HUB_AUTHORIZED_KEYS", "C:/keys/authorized_keys")
	t.Setenv("EF_HUB_QUACK_ADDR", "127.0.0.1:9555")
	t.Setenv("EF_HUB_TOKEN", "environment-token")
	cfg, err := resolveHub(t, "--hub-token", "flag-token")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubAuthorizedKeys != "C:/keys/authorized_keys" || cfg.HubQuackAddr != "127.0.0.1:9555" || cfg.HubToken != "flag-token" {
		t.Fatalf("hub settings = %+v", cfg)
	}
}

func TestRejectInvalidValues(t *testing.T) {
	if _, err := resolveServe(t, "--retention-days", "-1"); err == nil {
		t.Fatal("negative retention accepted")
	}
	t.Setenv("EF_REQUEST_TIMEOUT", "invalid")
	if _, err := resolveServe(t); err == nil {
		t.Fatal("invalid environment duration accepted")
	}
}

func TestRegisterOnlyRelevantOptions(t *testing.T) {
	serveFlags := New()
	serveSet := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	serveFlags.BindServe(serveSet)
	if serveSet.Lookup("hub-token") != nil {
		t.Fatal("serve unexpectedly exposes --hub-token")
	}

	hubFlags := New()
	hubSet := pflag.NewFlagSet("hub", pflag.ContinueOnError)
	hubFlags.BindHub(hubSet)
	if hubSet.Lookup("openai-upstream") != nil {
		t.Fatal("hub unexpectedly exposes --openai-upstream")
	}
}

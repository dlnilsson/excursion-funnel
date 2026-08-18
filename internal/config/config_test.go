package config

import (
	"testing"
	"time"
)

func TestLoad_HardeningEnvAndFlags(t *testing.T) {
	t.Setenv("EF_REQUEST_TIMEOUT", "3s")
	t.Setenv("EF_IDLE_TIMEOUT", "4s")
	t.Setenv("EF_QUEUE_DRAIN_TIMEOUT", "5s")
	t.Setenv("EF_FORWARD_INTERVAL", "6s")
	t.Setenv("EF_RETENTION_DAYS", "9")

	cfg, err := Load([]string{
		"--request-timeout", "30s",
		"--idle-timeout", "40s",
		"--retention-days", "14",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.RequestTimeout != 30*time.Second {
		t.Fatalf("RequestTimeout = %v, want 30s", cfg.RequestTimeout)
	}
	if cfg.IdleTimeout != 40*time.Second {
		t.Fatalf("IdleTimeout = %v, want 40s", cfg.IdleTimeout)
	}
	if cfg.QueueDrainTimeout != 5*time.Second {
		t.Fatalf("QueueDrainTimeout = %v, want env 5s", cfg.QueueDrainTimeout)
	}
	if cfg.ForwardInterval != 6*time.Second {
		t.Fatalf("ForwardInterval = %v, want env 6s", cfg.ForwardInterval)
	}
	if cfg.RetentionDays != 14 {
		t.Fatalf("RetentionDays = %d, want flag 14", cfg.RetentionDays)
	}
}

func TestLoad_RejectsNegativeRetention(t *testing.T) {
	_, err := Load([]string{"--retention-days", "-1"})
	if err == nil {
		t.Fatalf("Load() error = nil, want validation error")
	}
}

func TestLoad_UIEnabledDefaultsToTrue(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.UIEnabled {
		t.Fatalf("UIEnabled = false, want true by default")
	}
}

func TestLoad_UIEnabledEnvAndFlagPrecedence(t *testing.T) {
	t.Setenv("EF_UI_ENABLED", "false")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UIEnabled {
		t.Fatalf("UIEnabled = true, want env override false")
	}

	cfg, err = Load([]string{"--ui-enabled=true"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.UIEnabled {
		t.Fatalf("UIEnabled = false, want flag override true")
	}
}

func TestLoad_HubInsecureDefaultsToFalse(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HubInsecure {
		t.Fatalf("HubInsecure = true, want false by default (TLS required unless opted out)")
	}
}

func TestLoad_HubInsecureEnvAndFlagPrecedence(t *testing.T) {
	t.Setenv("EF_HUB_INSECURE", "true")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.HubInsecure {
		t.Fatalf("HubInsecure = false, want env override true")
	}

	cfg, err = Load([]string{"--insecure=false"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HubInsecure {
		t.Fatalf("HubInsecure = true, want flag override false")
	}
}

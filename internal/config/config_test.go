package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	var (
		home = t.TempDir()
		data = t.TempDir()
	)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", "")

	for _, tt := range []struct {
		name string
		xdg  string
	}{
		{name: "empty"},
		{name: "absolute", xdg: data},
		{name: "relative", xdg: "relative/data"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", tt.xdg)
			var (
				base = filepath.Join(home, ".local", "state")
				cfg  = Default()
			)
			if runtime.GOOS == "windows" {
				base = home
			} else if tt.name == "absolute" {
				base = data
			}
			if want := filepath.Join(base, "excursion-funnel", "usage.duckdb"); cfg.DBPath != want || DefaultDBPath() != want {
				t.Errorf("database paths = %q, %q; want %q", cfg.DBPath, DefaultDBPath(), want)
			}
			if want := filepath.Join(base, "excursion-funnel", "outbox.sqlite"); cfg.OutboxPath != want {
				t.Errorf("outbox path = %q; want %q", cfg.OutboxPath, want)
			}
		})
	}

	t.Run("local app data", func(t *testing.T) {
		t.Setenv("LOCALAPPDATA", data)
		t.Setenv("XDG_DATA_HOME", "")
		base := filepath.Join(home, ".local", "state")
		if runtime.GOOS == "windows" {
			base = data
		}
		if want := filepath.Join(base, "excursion-funnel", "usage.duckdb"); DefaultDBPath() != want {
			t.Errorf("database path = %q; want %q", DefaultDBPath(), want)
		}
	})
}

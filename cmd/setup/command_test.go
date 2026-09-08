package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupClaudeUsesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, want := range []string{"Claude Code configured", "Claude Code already configured"} {
		var (
			cmd = New()
			out bytes.Buffer
		)
		cmd.SetArgs([]string{"claude"})
		cmd.SetOut(&out)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
	var (
		path      = filepath.Join(home, ".claude", "settings.json")
		collision = []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://sentinel-secret.example.com"}}`)
		cmd       = New()
		out       bytes.Buffer
	)
	if err := os.WriteFile(path, collision, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd.SetArgs([]string{"claude"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "configuration collision") {
		t.Fatalf("expected collision, got %v", err)
	}
	if strings.Contains(out.String()+err.Error(), "sentinel-secret") {
		t.Fatal("collision exposed existing setting")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, collision) {
		t.Fatalf("collision changed settings: %q, %v", data, err)
	}
}

func TestSetupArguments(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr bool
		writes  bool
	}{
		{args: nil},
		{args: []string{"--help"}},
		{args: []string{"claude", "--help"}},
		{args: []string{"codex"}, writes: true},
		{args: []string{"unknown"}, wantErr: true},
		{args: []string{"claude", "extra"}, wantErr: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var (
				cmd  = New()
				out  bytes.Buffer
				home = t.TempDir()
			)
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			cmd.SetArgs(tc.args)
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.ExecuteContext(t.Context()); (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error=%v", err, tc.wantErr)
			}
			if !tc.writes {
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatalf("help or invalid arguments changed home: %v, %v", entries, err)
				}
			}
		})
	}
}

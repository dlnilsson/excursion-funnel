package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaude(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		missing bool
		changed bool
		wantErr string
	}{
		{name: "missing file and directory", missing: true, changed: true},
		{name: "missing env", input: `{}`, changed: true},
		{name: "missing key", input: `{"env":{}}`, changed: true},
		{name: "empty string", input: `{"env":{"ANTHROPIC_BASE_URL":""}}`, changed: true},
		{name: "matching", input: "{\n\t\"env\": {\"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\"}\n}"},
		{name: "collision", input: `{"env":{"ANTHROPIC_BASE_URL":"https://user:sentinel-secret@example.com"}}`, wantErr: "configuration collision"},
		{name: "trailing slash differs", input: `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787/"}}`, wantErr: "configuration collision"},
		{name: "empty file", input: "", wantErr: "valid JSON object"},
		{name: "malformed", input: `{"sentinel-secret":`, wantErr: "valid JSON object"},
		{name: "multiple documents", input: `{} {}`, wantErr: "valid JSON object"},
		{name: "null root", input: `null`, wantErr: "valid JSON object"},
		{name: "array root", input: `[]`, wantErr: "valid JSON object"},
		{name: "null env", input: `{"env":null}`, wantErr: "env must be a JSON object"},
		{name: "array env", input: `{"env":[]}`, wantErr: "env must be a JSON object"},
		{name: "string env", input: `{"env":"sentinel-secret"}`, wantErr: "env must be a JSON object"},
		{name: "null URL", input: `{"env":{"ANTHROPIC_BASE_URL":null}}`, wantErr: "must be a string"},
		{name: "numeric URL", input: `{"env":{"ANTHROPIC_BASE_URL":123}}`, wantErr: "must be a string"},
		{name: "boolean URL", input: `{"env":{"ANTHROPIC_BASE_URL":false}}`, wantErr: "must be a string"},
		{name: "object URL", input: `{"env":{"ANTHROPIC_BASE_URL":{}}}`, wantErr: "must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				path   = filepath.Join(t.TempDir(), ".claude", "settings.json")
				before os.FileInfo
			)
			if !tc.missing {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.input), 0o640); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			changed, err := Claude(path)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatalf("error exposed a setting: %v", err)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				if string(after) != tc.input || !os.SameFile(before, info) || !before.ModTime().Equal(info.ModTime()) {
					t.Fatal("no-op or rejected input modified the file")
				}
			} else {
				var settings struct {
					Env map[string]string `json:"env"`
				}
				if err := json.Unmarshal(after, &settings); err != nil {
					t.Fatal(err)
				}
				if settings.Env["ANTHROPIC_BASE_URL"] != claudeBaseURL {
					t.Fatalf("wrong URL: %s", after)
				}
				if changed, err := Claude(path); changed || err != nil {
					t.Fatalf("second run: changed=%v, err=%v", changed, err)
				}
				if runtime.GOOS != "windows" {
					wantMode := os.FileMode(0o600)
					if before != nil {
						wantMode = before.Mode().Perm()
					}
					if info.Mode().Perm() != wantMode {
						t.Fatalf("permissions = %o, want %o", info.Mode().Perm(), wantMode)
					}
				}
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("unexpected directory contents: %v, %v", entries, err)
			}
		})
	}
}

func TestClaudePreservesOtherSettings(t *testing.T) {
	var (
		path                = filepath.Join(t.TempDir(), "settings.json")
		input               = []byte(`{"permissions":{"allow":["Read"]},"large":9007199254740993,"env":{"TOKEN":"sentinel-secret","OTHER":"keep"}}`)
		before, after       map[string]json.RawMessage
		beforeEnv, afterEnv map[string]json.RawMessage
	)
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := Claude(path); !changed || err != nil {
		t.Fatalf("changed=%v, err=%v", changed, err)
	}
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		data []byte
		dest *map[string]json.RawMessage
	}{{input, &before}, {output, &after}} {
		if err := json.Unmarshal(pair.data, pair.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(before["env"], &beforeEnv); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after["env"], &afterEnv); err != nil {
		t.Fatal(err)
	}
	delete(before, "env")
	delete(after, "env")
	delete(afterEnv, "ANTHROPIC_BASE_URL")
	for _, pair := range [][2]map[string]json.RawMessage{{before, after}, {beforeEnv, afterEnv}} {
		var compact [2]bytes.Buffer
		for i := range pair {
			encoded, err := json.Marshal(pair[i])
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Compact(&compact[i], encoded); err != nil {
				t.Fatal(err)
			}
		}
		if compact[0].String() != compact[1].String() {
			t.Fatal("unrelated settings changed")
		}
	}
}

func TestClaudeInvalidDestination(t *testing.T) {
	var (
		dir     = t.TempDir()
		blocker = filepath.Join(dir, "blocker")
	)
	if err := os.WriteFile(blocker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(blocker, "settings.json")} {
		if changed, err := Claude(path); changed || err == nil {
			t.Fatalf("%s: changed=%v, err=%v", path, changed, err)
		}
	}
	data, err := os.ReadFile(blocker)
	if err != nil || string(data) != "keep" {
		t.Fatalf("blocker changed: %q, %v", data, err)
	}
}

func TestClaudeWriteFailurePreservesSettings(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions without root privileges")
	}
	var (
		dir   = t.TempDir()
		path  = filepath.Join(dir, "settings.json")
		input = []byte(`{"env":{"OTHER":"keep"}}`)
	)
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	if changed, err := Claude(path); changed || err == nil {
		t.Fatalf("expected write failure, got changed=%v, err=%v", changed, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, input) {
		t.Fatalf("write failure modified settings: %q, %v", data, err)
	}
}

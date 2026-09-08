package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCodex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		missing bool
		changed bool
		wantErr string
	}{
		{name: "missing file and directory", missing: true, changed: true},
		{name: "empty file", input: "", changed: true},
		{name: "existing settings", input: "model = \"gpt-5\"\n\n[sandbox_workspace_write]\nnetwork_access = false\n", changed: true},
		{name: "matching", input: codexConfig},
		{name: "other default provider", input: `model_provider = "other"`, wantErr: "model_provider is already set"},
		{name: "partial provider", input: "[model_providers.\"excursion-funnel\"]\nname = \"old\"\n", wantErr: "model_providers.excursion-funnel is already set"},
		{name: "different provider", input: strings.Replace(codexConfig, "http://127.0.0.1:8787/v1", "https://other.example.com/v1", 1), wantErr: "model_providers.excursion-funnel is already set"},
		{name: "invalid TOML", input: "model_provider = ", wantErr: "expected valid TOML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".codex", "config.toml")
			if !tc.missing {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.input), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := Codex(path)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !changed {
				if string(data) != tc.input {
					t.Fatal("no-op or rejected input modified the file")
				}
				return
			}
			var settings map[string]any
			if err := toml.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			if settings["model_provider"] != "excursion-funnel" {
				t.Fatalf("model_provider = %v", settings["model_provider"])
			}
			providers := settings["model_providers"].(map[string]any)
			if !matchesCodexProvider(providers["excursion-funnel"]) {
				t.Fatalf("provider = %#v", providers["excursion-funnel"])
			}
			if changed, err := Codex(path); changed || err != nil {
				t.Fatalf("second run: changed=%v, err=%v", changed, err)
			}
		})
	}
}

func TestCodexPreservesOtherSettings(t *testing.T) {
	var (
		path  = filepath.Join(t.TempDir(), "config.toml")
		input = []byte("# keep this comment\nmodel = \"gpt-5\"\n\n[features]\nweb_search_request = true\n")
	)
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := Codex(path); !changed || err != nil {
		t.Fatalf("changed=%v, err=%v", changed, err)
	}
	output, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, input) {
		t.Fatalf("other settings were not preserved:\n%s", output)
	}
}

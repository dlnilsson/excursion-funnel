package version

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.25.1",
		Settings:  []debug.BuildSetting{{Key: "vcs.revision", Value: "1234567890abcdef"}},
		Deps:      []*debug.Module{{Path: duckDBDriverModule, Version: "v2.10505.0"}},
	}

	got := Resolve("release1", info, "v1.5.0")
	if want := (Metadata{Version: "release1", GoVersion: "go1.25.1", DuckDBVersion: "v1.5.0", DuckDBDriverVersion: "v2.10505.0"}); got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}

	got = Resolve("", info, "")
	if want := (Metadata{Version: "1234567", GoVersion: "go1.25.1", DuckDBVersion: "unknown", DuckDBDriverVersion: "v2.10505.0"}); got != want {
		t.Fatalf("Resolve() fallback = %+v, want %+v", got, want)
	}

	got = Resolve("", nil, "v1.5.0")
	if got.Version != "dev" || got.GoVersion != "unknown" || got.DuckDBDriverVersion != "unknown" {
		t.Fatalf("Resolve() development fallback = %+v", got)
	}

	info = &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260819194858-fed33091a2ff"}}
	got = Resolve("", info, "v1.5.0")
	if got.Version != "fed3309" {
		t.Fatalf("Resolve() pseudo-version fallback = %+v", got)
	}
}

func TestRunTo(t *testing.T) {
	metadata := Metadata{Version: "fed3309", GoVersion: "go1.25.1", DuckDBVersion: "v1.5.0", DuckDBDriverVersion: "v2.10505.0"}
	var out bytes.Buffer
	if err := RunTo(nil, &out, metadata, false); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "ef fed3309 (go1.25.1; duckdb v1.5.0; driver v2.10505.0)\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if err := RunTo([]string{"extra"}, &out, metadata, false); err == nil {
		t.Fatal("RunTo() accepted an unexpected argument")
	}
}

func TestRunToJSON(t *testing.T) {
	metadata := Metadata{Version: "fed3309", GoVersion: "go1.25.1", DuckDBVersion: "v1.5.0", DuckDBDriverVersion: "v2.10505.0"}
	var out bytes.Buffer
	if err := RunTo(nil, &out, metadata, true); err != nil {
		t.Fatal(err)
	}
	var got Metadata
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("version --json output is not valid JSON: %v: %s", err, out.String())
	}
	if got != metadata {
		t.Fatalf("version --json = %+v, want %+v", got, metadata)
	}
}

func TestLogStartupIncludesMetadata(t *testing.T) {
	for _, command := range []string{"serve", "hub"} {
		var out bytes.Buffer
		log := slog.New(slog.NewTextHandler(&out, nil))
		LogStartup(log, command, Metadata{
			Version: "fed3309", GoVersion: "go1.25.1", DuckDBVersion: "v1.5.0", DuckDBDriverVersion: "v2.10505.0",
		})
		got := out.String()
		for _, field := range []string{"command=" + command, "version=fed3309", "go_version=go1.25.1", "duckdb_version=v1.5.0", "duckdb_driver_version=v2.10505.0"} {
			if !strings.Contains(got, field) {
				t.Fatalf("%s startup log missing %q: %s", command, field, got)
			}
		}
	}
}

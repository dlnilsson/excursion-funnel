// Package version reports build and DuckDB version information for ef.
package version

import (
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/duckdb/duckdb-go/v2/mapping"
)

const duckDBDriverModule = "github.com/duckdb/duckdb-go/v2"

// Build is set in release and scripted builds with:
//
//	-ldflags "-X github.com/dlnilsson/excursion-funnel/internal/version.Build=<short-git-sha>"
var Build string

// Metadata identifies the ef build and its DuckDB dependencies.
type Metadata struct {
	Version             string
	DuckDBVersion       string
	DuckDBDriverVersion string
}

// Run writes build information to out.
func Run(args []string, out io.Writer) error {
	return RunTo(args, out, Current())
}

// RunTo writes metadata to out. It exists to make command output testable.
func RunTo(args []string, out io.Writer, metadata Metadata) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected argument %q", args[0])
	}
	fmt.Fprintf(out, "ef %s (duckdb %s; driver %s)\n",
		metadata.Version, metadata.DuckDBVersion, metadata.DuckDBDriverVersion)
	return nil
}

// Current returns metadata for the running binary.
func Current() Metadata {
	info, _ := debug.ReadBuildInfo()
	return Resolve(Build, info, mapping.LibraryVersion())
}

// Resolve derives metadata from an injected build version and Go build info.
func Resolve(injectedVersion string, info *debug.BuildInfo, duckDBVersion string) Metadata {
	metadata := Metadata{
		Version:             strings.TrimSpace(injectedVersion),
		DuckDBVersion:       strings.TrimSpace(duckDBVersion),
		DuckDBDriverVersion: "unknown",
	}
	if metadata.DuckDBVersion == "" {
		metadata.DuckDBVersion = "unknown"
	}
	if info != nil {
		if metadata.Version == "" {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					metadata.Version = shortGitSHA(setting.Value)
					break
				}
			}
			if metadata.Version == "" {
				metadata.Version = pseudoVersionGitSHA(info.Main.Version)
			}
		}
		for _, dependency := range info.Deps {
			if dependency.Path != duckDBDriverModule {
				continue
			}
			if dependency.Replace != nil {
				dependency = dependency.Replace
			}
			if dependency.Version != "" {
				metadata.DuckDBDriverVersion = dependency.Version
			}
			break
		}
	}
	if metadata.Version == "" {
		metadata.Version = "dev"
	}
	return metadata
}

func shortGitSHA(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}

// go install module@version builds outside a Git checkout, so it may omit the
// vcs.revision setting. A pseudo-version still ends in the source commit SHA.
func pseudoVersionGitSHA(moduleVersion string) string {
	parts := strings.Split(moduleVersion, "-")
	if len(parts) < 3 {
		return ""
	}
	timestamp := parts[len(parts)-2]
	revision := parts[len(parts)-1]
	if len(timestamp) != 14 || len(revision) < 7 || !allCharacters(timestamp, "0123456789") || !allCharacters(strings.ToLower(revision), "0123456789abcdef") {
		return ""
	}
	return shortGitSHA(revision)
}

func allCharacters(value, allowed string) bool {
	for _, character := range value {
		if !strings.ContainsRune(allowed, character) {
			return false
		}
	}
	return true
}

// LogStartup writes the build metadata as fields on the startup log entry.
func LogStartup(log *slog.Logger, command string, metadata Metadata) {
	log.Info("excursion-funnel starting", "command", command,
		"version", metadata.Version,
		"duckdb_version", metadata.DuckDBVersion,
		"duckdb_driver_version", metadata.DuckDBDriverVersion)
}

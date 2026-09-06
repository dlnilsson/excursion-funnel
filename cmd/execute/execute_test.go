package execute_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dlnilsson/excursion-funnel/cmd/execute"
	"github.com/dlnilsson/excursion-funnel/cmd/root"
	appversion "github.com/dlnilsson/excursion-funnel/internal/version"
	"github.com/spf13/cobra"
)

func run(t *testing.T, command *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	t.Setenv("__FANG_TEST_WIDTH", "100")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := execute.Execute(t.Context(), command)
	return stdout.String(), stderr.String(), err
}

func newCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "ef",
		Short: "test command",
	}
	command.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "test subcommand",
	})
	return command
}

func TestExecuteAppliesFangHelp(t *testing.T) {
	stdout, stderr, err := run(t, newCommand(), "--help")
	if err != nil {
		t.Fatalf("help returned %v: %s", err, stderr)
	}
	for _, want := range []string{"  USAGE  ", "  COMMANDS  ", "-v --version"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("Fang help missing %q:\n%s", want, stdout)
		}
	}
}

func TestExecuteAddsShortAndLongVersionFlags(t *testing.T) {
	want := appversion.Current().Version
	for _, flag := range []string{"-v", "--version"} {
		t.Run(flag, func(t *testing.T) {
			stdout, stderr, err := run(t, newCommand(), flag)
			if err != nil {
				t.Fatalf("%s returned %v: %s", flag, err, stderr)
			}
			if !strings.Contains(stdout, want) {
				t.Fatalf("%s output missing version %q:\n%s", flag, want, stdout)
			}
		})
	}
}

func TestExecuteReturnsOriginalErrorAndRedactsItOnce(t *testing.T) {
	original := errors.New("request failed: token=secret")
	command := newCommand()
	command.RunE = func(*cobra.Command, []string) error {
		return original
	}

	_, stderr, err := run(t, command)
	if !errors.Is(err, original) {
		t.Fatalf("Execute returned %v, want original error %v", err, original)
	}
	if strings.Contains(stderr, "token=secret") {
		t.Fatalf("error output exposed a credential:\n%s", stderr)
	}
	if count := strings.Count(stderr, "[REDACTED]"); count != 1 {
		t.Fatalf("error output contains %d rendered errors, want 1:\n%s", count, stderr)
	}
}

func TestExecuteKeepsCompletionAvailable(t *testing.T) {
	stdout, stderr, err := run(t, newCommand(), "completion", "powershell")
	if err != nil {
		t.Fatalf("completion returned %v: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Register-ArgumentCompleter") {
		t.Fatalf("unexpected PowerShell completion output:\n%s", stdout)
	}
}

func TestExecuteKeepsDetailedVersionCommand(t *testing.T) {
	stdout, stderr, err := run(t, root.New(), "version")
	if err != nil {
		t.Fatalf("version returned %v: %s", err, stderr)
	}
	metadata := appversion.Current()
	want := fmt.Sprintf("ef %s (%s; duckdb %s; driver %s)",
		metadata.Version, metadata.GoVersion, metadata.DuckDBVersion, metadata.DuckDBDriverVersion)
	if !strings.Contains(stdout, want) {
		t.Fatalf("detailed version output missing %q:\n%s", want, stdout)
	}
}

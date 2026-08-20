package root

import (
	"bytes"
	"strings"
	"testing"
)

func execute(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := New()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestRoot_NoArgumentsShowsHelp(t *testing.T) {
	stdout, _, err := execute(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Usage:", "Available Commands:", "serve", "usage"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("root help missing %q:\n%s", want, stdout)
		}
	}
}

func TestRoot_RegistersCommandsAndCompletion(t *testing.T) {
	stdout, _, err := execute(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"serve", "hub", "version", "migrate", "usage", "tools", "inspect", "completion"} {
		if !strings.Contains(stdout, name) {
			t.Fatalf("help missing command %q:\n%s", name, stdout)
		}
	}
}

func TestRoot_SubcommandHelpDoesNotExposeEnvironmentSecrets(t *testing.T) {
	const secret = "sentinel-hub-token-that-must-not-leak"
	t.Setenv("EF_HUB_TOKEN", secret)
	stdout, stderr, err := execute(t, "hub", "--help")
	if err != nil {
		t.Fatal(err)
	}
	output := stdout + stderr
	if strings.Contains(output, secret) {
		t.Fatalf("hub help exposed EF_HUB_TOKEN:\n%s", output)
	}
	if !strings.Contains(output, "--hub-token") {
		t.Fatalf("hub help missing --hub-token:\n%s", output)
	}
}

func TestRoot_UnknownCommandReturnsSuggestionError(t *testing.T) {
	_, _, err := execute(t, "serbe")
	if err == nil {
		t.Fatal("unknown command returned nil")
	}
	if text := err.Error(); !strings.Contains(text, "unknown command") || !strings.Contains(text, "serve") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRoot_SubcommandHelpSucceeds(t *testing.T) {
	for _, args := range [][]string{{"serve", "--help"}, {"usage", "--help"}, {"tools", "--help"}} {
		if _, _, err := execute(t, args...); err != nil {
			t.Fatalf("%v returned %v", args, err)
		}
	}
}

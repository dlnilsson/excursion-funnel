package open

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/config"
)

func TestRunLaunchesBrowser(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("test browser launcher requires a Unix shell")
	}
	var (
		dir     = t.TempDir()
		args    = filepath.Join(dir, "browser-args")
		name    = "xdg-open"
		out     bytes.Buffer
		cfg     = config.Config{HubAddr: "homebox.tail588fb8.ts.net:9494"}
		wantURL = "https://homebox.tail588fb8.ts.net:8788/ui/"
	)
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	launcher := filepath.Join(dir, name)
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$EF_TEST_BROWSER_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("EF_TEST_BROWSER_ARGS", args)
	if err := Run(cfg, &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != wantURL+"\n" {
		t.Fatalf("browser arguments = %q, want %q", got, wantURL+"\n")
	}
	if out.String() != "Opened "+wantURL+"\n" {
		t.Fatalf("output = %q", out.String())
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = Run(cfg, &out)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), wantURL) {
		t.Fatalf("browser failure = %v, want exit status and dashboard URL", err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed launch reported success: %q", out.String())
	}
}

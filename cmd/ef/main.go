// Command ef is a local usage-telemetry proxy for Codex and Claude Code.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dlnilsson/excursion-funnel/cmd/root"
	"github.com/dlnilsson/excursion-funnel/internal/safelog"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.New().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", safelog.Redact(err.Error()))
		os.Exit(1)
	}
}

// Command ef is a local usage-telemetry proxy for Codex and Claude Code.
package main

import (
	"context"
	"os"
	"syscall"

	"github.com/dlnilsson/excursion-funnel/cmd/execute"
	"github.com/dlnilsson/excursion-funnel/cmd/root"
)

func main() {
	if err := execute.Execute(context.Background(), root.New(), os.Interrupt, syscall.SIGTERM); err != nil {
		os.Exit(1)
	}
}

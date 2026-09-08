// Package root assembles the ef command tree.
package root

import (
	"github.com/dlnilsson/excursion-funnel/cmd/hub"
	"github.com/dlnilsson/excursion-funnel/cmd/inspect"
	"github.com/dlnilsson/excursion-funnel/cmd/requests"
	"github.com/dlnilsson/excursion-funnel/cmd/serve"
	"github.com/dlnilsson/excursion-funnel/cmd/sessions"
	"github.com/dlnilsson/excursion-funnel/cmd/setup"
	"github.com/dlnilsson/excursion-funnel/cmd/tools"
	"github.com/dlnilsson/excursion-funnel/cmd/usage"
	versioncmd "github.com/dlnilsson/excursion-funnel/cmd/version"
	"github.com/spf13/cobra"
)

// New creates the complete ef command tree. It deliberately returns a plain
// Cobra command so Fang can wrap execution later without changing subcommands.
func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "ef",
		Short:         "Local usage-telemetry proxy for Codex and Claude Code",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `ef is a local usage-telemetry proxy for Codex and Claude Code.

The usage, tools, and inspect commands query a configured hub or running local
daemon through Quack, then fall back to a read-only DuckDB file when possible.`,
	}
	cmd.AddCommand(
		serve.New(),
		setup.New(),
		hub.New(),
		versioncmd.New(),
		usage.New(),
		sessions.New(),
		tools.New(),
		requests.New(),
		inspect.New(),
	)
	return cmd
}

// Package tools defines the ef tools Cobra command.
package tools

import (
	"github.com/dlnilsson/excursion-funnel/cmd/loading"
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	apptools "github.com/dlnilsson/excursion-funnel/internal/tools"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	limit      int
	all        bool
	json       bool
	verbose    bool
}

const loadingToolsText = "Loading tools…"

// New creates the tools command and its today child command.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), limit: report.DefaultRecentToolCallLimit}
	cmd := &cobra.Command{Use: "tools", Short: "Inspect recorded tool calls"}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().IntVar(&opts.limit, "limit", opts.limit, "maximum entries to show")
	cmd.PersistentFlags().BoolVar(&opts.all, "all", false, "include tool calls recorded from all sources")
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print tool calls as JSON")
	cmd.PersistentFlags().BoolVar(&opts.verbose, "verbose", false, "include tool-call metadata in output")
	cmd.AddCommand(&cobra.Command{
		Use:   "today",
		Short: "Show tool calls recorded during the current local day",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var (
				ctx       = cmd.Context()
				in        = cmd.InOrStdin()
				out       = cmd.OutOrStdout()
				statusOut = cmd.ErrOrStderr()
				appOpts   = apptools.Options{
					Connection: opts.connection.Resolve(cmd.Flags()),
					Limit:      opts.limit,
					Source:     reportflags.CurrentSource(opts.all),
					JSON:       opts.json,
					Verbose:    opts.verbose,
				}
			)
			if !loading.ShouldAnimate(appOpts.JSON, loading.IsTerminalWriter(out), loading.IsTerminalWriter(statusOut)) {
				return apptools.Run(ctx, in, out, appOpts)
			}
			rows, err := loading.Run(ctx, statusOut, loadingToolsText, func() ([]report.ToolCallRow, error) {
				return apptools.Load(ctx, appOpts)
			})
			if err != nil {
				return err
			}
			return apptools.Render(ctx, in, out, rows, appOpts)
		},
	})
	return cmd
}

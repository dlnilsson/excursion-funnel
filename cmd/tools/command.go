// Package tools defines the ef tools Cobra command.
package tools

import (
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	apptools "github.com/dlnilsson/excursion-funnel/internal/tools"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	limit      int
	json       bool
}

// New creates the tools command and its today child command.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), limit: report.DefaultRecentToolCallLimit}
	cmd := &cobra.Command{Use: "tools", Short: "Inspect recorded tool calls"}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().IntVar(&opts.limit, "limit", opts.limit, "maximum tool calls to print")
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print tool calls as JSON")
	cmd.AddCommand(&cobra.Command{
		Use:   "today",
		Short: "Show tool calls recorded during the current local day",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apptools.Run(cmd.Context(), cmd.OutOrStdout(), apptools.Options{
				Connection: opts.connection.Resolve(cmd.Flags()),
				Limit:      opts.limit,
				JSON:       opts.json,
			})
		},
	})
	return cmd
}

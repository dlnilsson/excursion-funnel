// Package inspect defines the ef inspect Cobra command.
package inspect

import (
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	appinspect "github.com/dlnilsson/excursion-funnel/internal/inspect"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	limit      int
}

// New creates the inspect command.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), limit: report.DefaultInspectLimit}
	cmd := &cobra.Command{
		Use:   "inspect request_or_response_id",
		Short: "Inspect requests by request or response ID",
		Args:  cobra.ExactArgs(1),
	}
	opts.connection.Bind(cmd.Flags())
	cmd.Flags().IntVar(&opts.limit, "limit", opts.limit, "maximum matching requests to print")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return appinspect.Run(cmd.Context(), cmd.OutOrStdout(), args[0], appinspect.Options{
			Connection: opts.connection.Resolve(cmd.Flags()),
			Limit:      opts.limit,
		})
	}
	return cmd
}

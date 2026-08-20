// Package serve defines the ef serve Cobra command.
package serve

import (
	"github.com/dlnilsson/excursion-funnel/cmd/configflags"
	appserve "github.com/dlnilsson/excursion-funnel/internal/serve"
	"github.com/spf13/cobra"
)

// New creates the serve command.
func New() *cobra.Command {
	flags := configflags.New()
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the local usage-telemetry proxy",
		Args:  cobra.NoArgs,
	}
	flags.BindServe(cmd.Flags())
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := flags.Resolve(cmd.Flags())
		if err != nil {
			return err
		}
		return appserve.Run(cmd.Context(), cfg, cmd.OutOrStdout())
	}
	return cmd
}

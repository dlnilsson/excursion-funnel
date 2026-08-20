// Package hub defines the ef hub Cobra command.
package hub

import (
	"github.com/dlnilsson/excursion-funnel/cmd/configflags"
	apphub "github.com/dlnilsson/excursion-funnel/internal/hub"
	"github.com/spf13/cobra"
)

// New creates the hub command.
func New() *cobra.Command {
	flags := configflags.New()
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Run the shared authenticated usage hub",
		Args:  cobra.NoArgs,
	}
	flags.BindHub(cmd.Flags())
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := flags.Resolve(cmd.Flags())
		if err != nil {
			return err
		}
		return apphub.Run(cmd.Context(), cfg, cmd.OutOrStdout())
	}
	return cmd
}

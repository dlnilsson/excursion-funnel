// Package open defines the ef open Cobra command.
package open

import (
	"github.com/dlnilsson/excursion-funnel/cmd/configflags"
	appopen "github.com/dlnilsson/excursion-funnel/internal/open"
	"github.com/spf13/cobra"
)

// New creates the open command.
func New() *cobra.Command {
	flags := configflags.New()
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the web dashboard in your default browser",
		Args:  cobra.NoArgs,
	}
	flags.BindOpen(cmd.Flags())
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := flags.Resolve(cmd.Flags())
		if err != nil {
			return err
		}
		return appopen.Run(cfg, cmd.OutOrStdout())
	}
	return cmd
}

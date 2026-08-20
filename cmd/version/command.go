// Package version defines the ef version command.
package version

import (
	appversion "github.com/dlnilsson/excursion-funnel/internal/version"
	"github.com/spf13/cobra"
)

// New creates the version command.
func New() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print ef and DuckDB version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return appversion.Run(nil, cmd.OutOrStdout())
		},
	}
}

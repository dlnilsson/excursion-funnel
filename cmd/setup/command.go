// Package setup defines commands for configuring local clients.
package setup

import (
	"fmt"
	"os"
	"path/filepath"

	appsetup "github.com/dlnilsson/excursion-funnel/internal/setup"
	"github.com/spf13/cobra"
)

// New creates the setup command and its supported client subcommands.
func New() *cobra.Command {
	command := &cobra.Command{
		Use:   "setup",
		Short: "Configure clients to use the local ef proxy",
	}
	command.AddCommand(&cobra.Command{
		Use:   "claude",
		Short: "Configure Claude Code to use the default local ef proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("locate home directory: %w", err)
			}
			var (
				path              = filepath.Join(home, ".claude", "settings.json")
				changed, setupErr = appsetup.Claude(path)
			)
			if setupErr != nil {
				return setupErr
			}
			if changed {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Claude Code configured in %s\n", path)
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Claude Code already configured in %s\n", path)
			}
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "codex",
		Short: "Configure Codex to use the default local ef proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("locate home directory: %w", err)
			}
			var (
				path              = filepath.Join(home, ".codex", "config.toml")
				changed, setupErr = appsetup.Codex(path)
			)
			if setupErr != nil {
				return setupErr
			}
			if changed {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Codex configured in %s\n", path)
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Codex already configured in %s\n", path)
			}
			return err
		},
	})
	return command
}

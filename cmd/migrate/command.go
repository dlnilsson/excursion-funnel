// Package migrate defines the ef migrate command.
package migrate

import (
	"os"
	"path/filepath"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	appmigrate "github.com/dlnilsson/excursion-funnel/internal/migrate"
	"github.com/spf13/cobra"
)

type options struct {
	dbPath string
	from   string
}

// New creates the migrate command.
func New() *cobra.Command {
	builtInDB := config.DefaultDBPath()
	opts := options{
		dbPath: builtInDB,
		from:   filepath.Join(filepath.Dir(builtInDB), "usage.sqlite"),
	}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a legacy SQLite ledger to DuckDB",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&opts.dbPath, "db", opts.dbPath, "destination DuckDB ledger path")
	cmd.Flags().StringVar(&opts.from, "from", opts.from, "legacy SQLite ledger path")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return run(cmd, opts)
	}
	return cmd
}

func run(cmd *cobra.Command, opts options) error {
	defaultDB := config.DefaultDBPath()
	if value := os.Getenv("EF_DB"); value != "" {
		defaultDB = value
	}
	dbPath := defaultDB
	from := filepath.Join(filepath.Dir(defaultDB), "usage.sqlite")
	if cmd.Flags().Changed("db") {
		dbPath = opts.dbPath
	}
	if cmd.Flags().Changed("from") {
		from = opts.from
	}
	return appmigrate.Run(cmd.Context(), cmd.OutOrStdout(), dbPath, from)
}

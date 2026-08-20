// Package reportflags binds shared reporting options to Cobra/pflag commands.
package reportflags

import (
	"os"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	"github.com/spf13/pflag"
)

// Flags binds report connection flags without exposing environment values in
// generated help.
type Flags struct {
	dbPath string
	hubKey string
}

// New initializes displayed values from built-in defaults.
func New() *Flags { return &Flags{dbPath: config.DefaultDBPath()} }

// Bind registers shared database and hub authentication flags.
func (f *Flags) Bind(fs *pflag.FlagSet) {
	fs.StringVar(&f.dbPath, "db", f.dbPath, "DuckDB ledger path")
	fs.StringVar(&f.hubKey, "hub-key", f.hubKey, "Ed25519 private key for remote hub authentication")
}

// Resolve applies EF_DB and EF_HUB_KEY unless an explicit flag overrides them.
func (f *Flags) Resolve(fs *pflag.FlagSet) reporting.Connection {
	defaultPath := config.DefaultDBPath()
	if value := os.Getenv("EF_DB"); value != "" {
		defaultPath = value
	}
	connection := reporting.Connection{
		DBPath:        defaultPath,
		DefaultDBPath: defaultPath,
		HubKey:        os.Getenv("EF_HUB_KEY"),
	}
	if fs.Changed("db") {
		connection.DBPath = f.dbPath
	}
	if fs.Changed("hub-key") {
		connection.HubKey = f.hubKey
	}
	return connection
}

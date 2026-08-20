package reportflags

import (
	"testing"

	"github.com/spf13/pflag"
)

func TestEnvironmentAndExplicitOverride(t *testing.T) {
	t.Setenv("EF_DB", "environment.duckdb")
	t.Setenv("EF_HUB_KEY", "environment-key")
	flags := New()
	set := pflag.NewFlagSet("report", pflag.ContinueOnError)
	flags.Bind(set)
	if err := set.Parse([]string{"--db", "flag.duckdb", "--hub-key", "flag-key"}); err != nil {
		t.Fatal(err)
	}
	connection := flags.Resolve(set)
	if connection.DBPath != "flag.duckdb" || connection.DefaultDBPath != "environment.duckdb" || connection.HubKey != "flag-key" {
		t.Fatalf("connection = %+v", connection)
	}
}

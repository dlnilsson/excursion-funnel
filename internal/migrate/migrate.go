// Package migrate imports legacy SQLite ledgers into DuckDB.
package migrate

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// Run migrates from into dbPath and reports success to out.
func Run(ctx context.Context, out io.Writer, dbPath, from string) error {
	if _, err := os.Stat(from); err != nil {
		return fmt.Errorf("legacy ledger %s: %w", from, err)
	}
	if err := store.MigrateSQLite(ctx, dbPath, from); err != nil {
		return err
	}
	fmt.Fprintf(out, "migrated %s to %s\n", from, dbPath)
	return nil
}

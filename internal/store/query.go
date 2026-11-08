package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dlnilsson/excursion-funnel/internal/sqlparams"
)

// QueryContext runs a reporting query. Local stores keep bound parameters;
// remote stores render literals before Quack serializes the query's filters.
func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.remoteCatalog != "" {
		var err error
		query, err = sqlparams.Bind(query, args...)
		if err != nil {
			return nil, fmt.Errorf("render remote query parameters: %w", err)
		}
		args = nil
	}
	return s.db.QueryContext(ctx, query, args...)
}

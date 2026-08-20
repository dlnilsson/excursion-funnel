// Package daemon contains lifecycle behavior shared by long-running services.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// RunRetention applies the configured startup retention policy.
func RunRetention(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) error {
	if cfg.RetentionDays <= 0 {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
	defer cancel()
	cutoff := time.Now().AddDate(0, 0, -cfg.RetentionDays)
	deleted, err := st.DeleteRequestsStartedBefore(cleanupCtx, cutoff)
	if err != nil {
		return fmt.Errorf("retention cleanup: %w", err)
	}
	log.Info("retention cleanup complete", "retention_days", cfg.RetentionDays, "deleted", deleted)
	return nil
}

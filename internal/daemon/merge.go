package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// StartStagingMerge runs a background loop that folds staged remote writes into
// the indexed ledger tables on an interval. It returns a stop function that
// blocks until the loop has exited. A merge failure is logged and retried on the
// next tick rather than killing the hub.
func StartStagingMerge(st *store.Store, interval time.Duration, log *slog.Logger) func() {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				merged, err := st.MergeStaging(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Warn("staging merge failed; staged rows retained", "err", err)
					continue
				}
				if merged > 0 {
					log.Debug("merged staged usage into ledger", "requests", merged)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

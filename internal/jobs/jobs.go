// Package jobs runs background maintenance tasks owned by the daemon.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/store"
)

// AggregateScheduler periodically refreshes materialized reporting tables.
type AggregateScheduler struct {
	store    *store.Store
	interval time.Duration
	timeout  time.Duration
	log      *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}
}

// NewAggregateScheduler builds a scheduler. A non-positive interval disables it.
func NewAggregateScheduler(st *store.Store, interval, timeout time.Duration, log *slog.Logger) *AggregateScheduler {
	return &AggregateScheduler{
		store:    st,
		interval: interval,
		timeout:  timeout,
		log:      log,
		done:     make(chan struct{}),
	}
}

// Start launches the scheduler. The first refresh runs immediately so /ui can
// read aggregates soon after daemon start.
func (s *AggregateScheduler) Start() {
	if s.interval <= 0 {
		close(s.done)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.run(ctx)
}

// Stop cancels the scheduler and waits for the goroutine to exit.
func (s *AggregateScheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

func (s *AggregateScheduler) run(ctx context.Context) {
	defer close(s.done)

	s.refresh(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *AggregateScheduler) refresh(parent context.Context) {
	ctx := parent
	cancel := func() {}
	if s.timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, s.timeout)
	}
	defer cancel()

	started := time.Now()
	if err := s.store.RefreshUsageAggregates(ctx); err != nil {
		s.log.Warn("usage aggregate refresh failed", "err", err)
		return
	}
	s.log.Info("usage aggregate refresh complete", "duration", time.Since(started))
}

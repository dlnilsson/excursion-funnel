// Package forward drains the distributed proxy's SQLite outbox into a Quack hub.
package forward

import (
	"context"
	"log/slog"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/store"
)

const batchSize = 50

// Config identifies the remote hub and retry cadence.
type Config struct {
	Address      string
	Token        string
	Insecure     bool
	PollInterval time.Duration
}

// Forwarder retries indefinitely while its owner is running.
type Forwarder struct {
	outbox *store.Outbox
	cfg    Config
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a stopped forwarder.
func New(outbox *store.Outbox, cfg Config, log *slog.Logger) *Forwarder {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	return &Forwarder{outbox: outbox, cfg: cfg, log: log, done: make(chan struct{})}
}

// Start launches the forwarding loop.
func (f *Forwarder) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go f.run(ctx)
}

// Stop stops forwarding and waits for the current network operation to return.
func (f *Forwarder) Stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	<-f.done
}

func (f *Forwarder) run(ctx context.Context) {
	defer close(f.done)
	var remote *store.Store
	defer func() {
		if remote != nil {
			_ = remote.Close()
		}
	}()
	backoff := f.cfg.PollInterval
	for {
		events, err := f.outbox.Pending(ctx, batchSize)
		if err == nil && len(events) == 0 {
			backoff = f.cfg.PollInterval
			if !wait(ctx, f.cfg.PollInterval) {
				return
			}
			continue
		}
		if err == nil && remote == nil {
			remote, err = store.OpenRemote(ctx, f.cfg.Address, f.cfg.Token, f.cfg.Insecure)
		}
		if err == nil {
			err = remote.InsertBatch(ctx, events)
		}
		if err == nil {
			ids := make([]string, len(events))
			for i := range events {
				ids[i] = events[i].RequestID
			}
			err = f.outbox.Acknowledge(ctx, ids)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log.Warn("hub forwarding failed; event remains in outbox", "err", err, "retry_in", backoff)
			if remote != nil {
				_ = remote.Close()
				remote = nil
			}
			if !wait(ctx, backoff) {
				return
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = f.cfg.PollInterval
		f.log.Debug("forwarded usage events to hub", "count", len(events))
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

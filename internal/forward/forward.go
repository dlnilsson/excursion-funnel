// Package forward drains the distributed proxy's SQLite outbox into a Quack hub.
package forward

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

const batchSize = 50

// Config identifies the remote hub and retry cadence.
type Config struct {
	Address      string
	KeyPath      string
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
	auth   *hubauth.Client
}

// New creates a stopped forwarder.
func New(outbox *store.Outbox, cfg Config, log *slog.Logger) *Forwarder {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	return &Forwarder{outbox: outbox, cfg: cfg, log: log, done: make(chan struct{}), auth: hubauth.NewClient(hubauth.ClientConfig{Address: cfg.Address, KeyPath: cfg.KeyPath, Insecure: cfg.Insecure})}
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
			var token string
			token, err = f.auth.Credential(ctx)
			if err == nil {
				remote, err = store.OpenRemote(ctx, f.cfg.Address, token, f.cfg.Insecure)
			}
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
			// Only discard the cached credential when the hub actually rejected
			// it. A server-side failure (e.g. an insert error) leaves the
			// credential valid, so invalidating here would force a needless
			// re-login on every retry.
			if isAuthError(err) {
				f.auth.Invalidate()
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

// isAuthError reports whether err indicates the hub rejected our credential.
// Quack surfaces credential rejection during ATTACH as an "Authentication
// failed" input error; other failures (insert errors, connection resets) leave
// the cached credential usable and must not trigger a re-login.
func isAuthError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Authentication failed")
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

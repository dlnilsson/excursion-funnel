package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// Outbox is a minimal durable SQLite spool. It is not an analytical store.
type Outbox struct {
	db *sql.DB
}

// OpenOutbox opens or creates a durable send buffer.
func OpenOutbox(path string) (*Outbox, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create outbox directory: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite outbox: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS usage_outbox (
  id TEXT PRIMARY KEY,
  payload BLOB NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_usage_outbox_created_at ON usage_outbox(created_at);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create SQLite outbox schema: %w", err)
	}
	return &Outbox{db: db}, nil
}

// Close closes the spool.
func (o *Outbox) Close() error { return o.db.Close() }

// InsertBatch durably spools events. Duplicate IDs are harmless retries.
func (o *Outbox) InsertBatch(ctx context.Context, events []queue.UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbox write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO usage_outbox (id, payload) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare outbox write: %w", err)
	}
	defer stmt.Close()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode outbox event %s: %w", event.RequestID, err)
		}
		if _, err := stmt.ExecContext(ctx, event.RequestID, payload); err != nil {
			return fmt.Errorf("spool outbox event %s: %w", event.RequestID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox write: %w", err)
	}
	return nil
}

// Pending returns the oldest unacknowledged events.
func (o *Outbox) Pending(ctx context.Context, limit int) ([]queue.UsageEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := o.db.QueryContext(ctx, `SELECT payload FROM usage_outbox ORDER BY created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read outbox: %w", err)
	}
	defer rows.Close()
	var events []queue.UsageEvent
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		var event queue.UsageEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("decode outbox event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox: %w", err)
	}
	return events, nil
}

// Acknowledge deletes events only after the hub transaction commits.
func (o *Outbox) Acknowledge(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	_, err := o.db.ExecContext(ctx, `DELETE FROM usage_outbox WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("acknowledge outbox events: %w", err)
	}
	return nil
}

// Count returns the current backlog size.
func (o *Outbox) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_outbox`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// WaitUntilEmpty is useful for graceful shutdown and integration tests.
func (o *Outbox) WaitUntilEmpty(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		count, err := o.Count(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), fmt.Errorf("%d outbox events remain", count))
		case <-ticker.C:
		}
	}
}

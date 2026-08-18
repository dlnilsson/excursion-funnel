// Package queue provides a bounded in-memory usage-event queue backed by a
// single writer goroutine, so concurrent proxy requests never write to
// the durable writer directly and a slow/contended database never blocks a proxied
// response.
package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// Usage holds token counts extracted from a provider response. Fields are
// pointers so an absent field round-trips as SQL NULL instead of a
// misleading zero. Provider-specific parsers (internal/openai,
// internal/anthropic) each produce a Usage value; this type is the common
// shape the write queue and store deal in, independent of provider.
type Usage struct {
	InputTokens       *int64
	CachedInputTokens *int64
	CacheWriteTokens  *int64
	OutputTokens      *int64
	ReasoningTokens   *int64
	TotalTokens       *int64
}

// ToolCall is one tool invocation emitted by a model. ArgumentsJSON preserves
// the provider payload, while Command and Description make the common
// Bash-style input shape easy to query without decoding JSON.
type ToolCall struct {
	ID            string
	Name          string
	Command       string
	Description   string
	ArgumentsJSON string
}

// NewToolCall builds a tool-call record from a provider's JSON input. It keeps
// the original JSON even when it is incomplete or malformed (which can happen
// when an SSE stream is interrupted), and extracts the optional command and
// description fields when the input is a JSON object.
func NewToolCall(id, name string, input []byte) ToolCall {
	call := ToolCall{
		ID:            id,
		Name:          name,
		ArgumentsJSON: string(input),
	}
	var fields struct {
		Command     json.RawMessage `json:"command"`
		Cmd         json.RawMessage `json:"cmd"`
		Description string          `json:"description"`
		Action      json.RawMessage `json:"action"`
	}
	if err := json.Unmarshal(input, &fields); err == nil {
		call.Command = commandText(fields.Command)
		if call.Command == "" {
			call.Command = commandText(fields.Cmd)
		}
		call.Description = fields.Description
		if call.Command == "" && len(fields.Action) > 0 {
			var action struct {
				Command json.RawMessage `json:"command"`
				Cmd     json.RawMessage `json:"cmd"`
			}
			if json.Unmarshal(fields.Action, &action) == nil {
				call.Command = commandText(action.Command)
				if call.Command == "" {
					call.Command = commandText(action.Cmd)
				}
			}
		}
	}
	return call
}

func commandText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var words []string
	if json.Unmarshal(raw, &words) == nil {
		return strings.Join(words, " ")
	}
	return ""
}

// SumTokens adds token counters, treating a nil pointer as zero. It returns nil
// when every argument is nil, so an all-absent sum round-trips as SQL NULL
// rather than a misleading zero — the same absent-vs-zero distinction the
// pointer fields themselves preserve. Provider parsers use it to derive totals
// the wire omits.
func SumTokens(vals ...*int64) *int64 {
	var (
		total   int64
		present bool
	)
	for _, v := range vals {
		if v != nil {
			total += *v
			present = true
		}
	}
	if !present {
		return nil
	}
	return &total
}

// UsageEvent is one terminal record of a proxied request, enqueued after the
// response has been fully sent (or the stream reached a terminal state).
type UsageEvent struct {
	RequestID  string
	ResponseID string
	Source     string
	Host       string

	StartedAt   time.Time
	CompletedAt time.Time

	Method      string
	Path        string
	UpstreamURL string

	ModelRequested string
	ModelReported  string
	Stream         bool

	HTTPStatus        int
	UpstreamRequestID string
	UserAgent         string
	Originator        string
	ClientName        string
	CodexSessionID    string

	ErrorType    string
	ErrorMessage string

	Usage     Usage
	UsageJSON json.RawMessage
	ToolCalls []ToolCall
}

// Writer persists a batch of usage events. Implementations are called only
// from the queue's single writer goroutine.
type Writer interface {
	InsertBatch(ctx context.Context, events []UsageEvent) error
}

// Config tunes queue capacity and writer batching/backpressure behavior.
type Config struct {
	// Capacity is the number of buffered events the channel can hold.
	Capacity int
	// BatchSize is the max number of events flushed in one Writer call.
	BatchSize int
	// FlushInterval bounds how long a partial batch waits before flushing.
	FlushInterval time.Duration
	// EnqueueTimeout bounds how long Enqueue blocks when the queue is full
	// before dropping the event.
	EnqueueTimeout time.Duration
}

// DefaultConfig returns the initial queue tuning from the project plan.
func DefaultConfig() Config {
	return Config{
		Capacity:       1000,
		BatchSize:      50,
		FlushInterval:  250 * time.Millisecond,
		EnqueueTimeout: 200 * time.Millisecond,
	}
}

// Queue is a bounded event channel drained by a single writer goroutine.
type Queue struct {
	cfg    Config
	writer Writer
	log    *slog.Logger

	events  chan UsageEvent
	closing chan struct{}
	done    chan struct{}

	dropped atomic.Int64
}

// New builds a Queue. Call Start to launch its writer goroutine.
func New(cfg Config, writer Writer, log *slog.Logger) *Queue {
	return &Queue{
		cfg:     cfg,
		writer:  writer,
		log:     log,
		events:  make(chan UsageEvent, cfg.Capacity),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start launches the single writer goroutine. Call once.
func (q *Queue) Start() {
	go q.run()
}

// Enqueue submits ev for persistence. It blocks up to cfg.EnqueueTimeout if
// the queue is full; on timeout it drops the event, increments the dropped
// counter, and logs one redacted warning. It never blocks the caller longer
// than that, so a slow writer can never fail an already-successful response.
func (q *Queue) Enqueue(ev UsageEvent) {
	select {
	case q.events <- ev:
		return
	default:
	}

	timer := time.NewTimer(q.cfg.EnqueueTimeout)
	defer timer.Stop()
	select {
	case q.events <- ev:
	case <-timer.C:
		q.dropped.Add(1)
		q.log.Warn("usage event queue full, dropping event",
			"request_id", ev.RequestID, "dropped_total", q.dropped.Load())
	}
}

// Dropped returns the number of usage events dropped due to a full queue.
func (q *Queue) Dropped() int64 {
	return q.dropped.Load()
}

// Close signals the writer goroutine to flush remaining buffered events and
// exit, waiting up to timeout. It must be called only after the HTTP server
// has stopped accepting requests, so no further Enqueue calls can race it.
func (q *Queue) Close(timeout time.Duration) error {
	close(q.closing)
	select {
	case <-q.done:
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func (q *Queue) run() {
	defer close(q.done)

	var (
		batch  = make([]UsageEvent, 0, q.cfg.BatchSize)
		ticker = time.NewTicker(q.cfg.FlushInterval)
	)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := q.writer.InsertBatch(context.Background(), batch); err != nil {
			q.log.Error("usage event batch write failed", "count", len(batch), "err", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case ev := <-q.events:
			batch = append(batch, ev)
			if len(batch) >= q.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-q.closing:
			q.drainRemaining(&batch)
			flush()
			return
		}
	}
}

// drainRemaining collects any events still buffered in the channel without
// blocking, so Close doesn't lose events that were enqueued just before
// shutdown.
func (q *Queue) drainRemaining(batch *[]UsageEvent) {
	for {
		select {
		case ev := <-q.events:
			*batch = append(*batch, ev)
		default:
			return
		}
	}
}

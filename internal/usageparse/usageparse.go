// Package usageparse is the provider-independent contract for extracting usage
// metadata from a proxied response.
//
// internal/openai and internal/anthropic had grown identical API surfaces —
// same extraction entry points, same streaming parser lifecycle — but with no
// interface between them the proxy carried a provider switch at every call
// site. This package names that shared shape so adding a provider is a
// registry entry rather than an edit to five switches.
//
// Everything here is best-effort by contract: a parse failure is reported to
// the caller as metadata, never as a reason to disturb the bytes already sent
// to the client.
package usageparse

import (
	"encoding/json"
	"errors"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

// Result is what every provider parser yields. Provider-specific extras that
// no consumer reads (OpenAI's status, Anthropic's stop_reason) stay on the
// concrete types rather than widening this shape.
type Result struct {
	ResponseID  string
	Model       string
	Usage       queue.Usage
	UsageJSON   json.RawMessage
	ToolCalls   []queue.ToolCall
	WebRequests []queue.WebRequest
	// ErrorType and ErrorMessage carry an error the provider reported inside an
	// otherwise well-formed response. They are distinct from a parse failure,
	// which is returned as an error.
	ErrorType    string
	ErrorMessage string
}

// StreamParser incrementally extracts usage from an SSE stream while the proxy
// relays the same bytes to the client.
type StreamParser interface {
	// Feed consumes the next chunk. Frame-level failures are latched rather
	// than returned, so the caller keeps feeding the stream it is relaying.
	Feed(b []byte)
	// Result finalizes the stream. It errors only when the stream ended before
	// any terminal event.
	Result() (Result, error)
	// PartialResult returns what has accumulated without requiring a terminal
	// event, for streams that died mid-flight after real tokens were spent.
	PartialResult() Result
	// Terminated reports whether a terminal event has been parsed. A client
	// that disconnects after this point still made a complete, billable
	// request.
	Terminated() bool
}

// Provider extracts usage from one upstream's responses.
type Provider interface {
	// ExtractCompleted parses a non-streaming success body.
	ExtractCompleted(body []byte) (Result, error)
	// ExtractError parses an error body. ok is false when body does not match
	// the provider's error shape.
	ExtractError(body []byte) (errType, errMessage string, ok bool)
	// NewStreamParser creates a parser bounded to limit buffered bytes.
	NewStreamParser(limit int) StreamParser
}

// errorPayload is the {"error": {"type", "message"}} envelope both the OpenAI
// and Anthropic APIs use for error bodies.
type errorPayload struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ExtractError parses the shared error envelope. ok is false when body is not
// valid JSON of that shape, or carries neither a type nor a message.
func ExtractError(body []byte) (errType, errMessage string, ok bool) {
	var payload errorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", false
	}
	if payload.Error.Type == "" && payload.Error.Message == "" {
		return "", "", false
	}
	return payload.Error.Type, payload.Error.Message, true
}

// ErrNoTerminalEvent reports a stream that ended before its provider sent a
// terminal event. Parsers wrap it so callers can distinguish a truncated
// stream from a malformed one.
var ErrNoTerminalEvent = errors.New("stream ended without terminal event")

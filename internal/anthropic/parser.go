package anthropic

import "github.com/dlnilsson/excursion-funnel/internal/usageparse"

// Parser adapts this package to the provider-independent usageparse contract.
// The concrete extraction functions keep returning CompletedResult so the
// Anthropic-only stop_reason field stays available to this package's own tests.
type Parser struct{}

// Compile-time proof the adapter still satisfies the contract.
var _ usageparse.Provider = Parser{}

// ExtractCompleted parses a non-streaming Messages API body.
func (Parser) ExtractCompleted(body []byte) (usageparse.Result, error) {
	result, err := ExtractCompleted(body)
	return result.toShared(), err
}

// ExtractError parses an Anthropic-style JSON error body.
func (Parser) ExtractError(body []byte) (errType, errMessage string, ok bool) {
	return ExtractError(body)
}

// NewStreamParser creates a bounded Anthropic Messages SSE parser.
func (Parser) NewStreamParser(limit int) usageparse.StreamParser {
	return &sharedStreamParser{parser: NewStreamParser(limit)}
}

func (r CompletedResult) toShared() usageparse.Result {
	return usageparse.Result{
		ResponseID:  r.ResponseID,
		Model:       r.Model,
		Usage:       r.Usage,
		UsageJSON:   r.UsageJSON,
		ToolCalls:   r.ToolCalls,
		WebRequests: r.WebRequests,
	}
}

func (r StreamResult) toShared() usageparse.Result {
	shared := r.CompletedResult.toShared()
	shared.ErrorType, shared.ErrorMessage = r.ErrorType, r.ErrorMessage
	return shared
}

// sharedStreamParser projects StreamParser onto usageparse.StreamParser.
type sharedStreamParser struct{ parser *StreamParser }

func (s *sharedStreamParser) Feed(b []byte) { s.parser.Feed(b) }

func (s *sharedStreamParser) Result() (usageparse.Result, error) {
	result, err := s.parser.Result()
	return result.toShared(), err
}

func (s *sharedStreamParser) PartialResult() usageparse.Result {
	return s.parser.PartialResult().toShared()
}

func (s *sharedStreamParser) Terminated() bool { return s.parser.Terminated() }

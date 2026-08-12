// Package anthropic parses Anthropic Messages API bodies to extract usage
// and error metadata. Parsing is best-effort and tolerant of unknown/missing
// fields: a parse failure here must never affect what the proxy has already
// sent to the client.
package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/sse"
)

// CompletedResult is the metadata extracted from a completed Messages API
// JSON body.
type CompletedResult struct {
	ResponseID  string
	Model       string
	StopReason  string
	Usage       queue.Usage
	UsageJSON   json.RawMessage
	ToolCalls   []queue.ToolCall
	WebRequests []queue.WebRequest
}

type messagePayload struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      json.RawMessage `json:"usage"`
	Content    []contentBlock  `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type serverToolUsage struct {
	WebSearchRequests *int64 `json:"web_search_requests"`
}

// messageUsage is the Messages API usage object. Unlike OpenAI's Responses
// API, Anthropic reports cache tokens as top-level fields and has no
// total_tokens or reasoning_tokens field.
type messageUsage struct {
	InputTokens              *int64          `json:"input_tokens"`
	OutputTokens             *int64          `json:"output_tokens"`
	CacheCreationInputTokens *int64          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64          `json:"cache_read_input_tokens"`
	ServerToolUse            serverToolUsage `json:"server_tool_use"`
}

// ExtractCompleted parses a non-streaming Messages API JSON body and returns
// its id, model, stop_reason, and usage. It returns an error only when body
// is not valid JSON matching the expected top-level shape.
func ExtractCompleted(body []byte) (CompletedResult, error) {
	var payload messagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return CompletedResult{}, err
	}

	result := CompletedResult{
		ResponseID:  payload.ID,
		Model:       payload.Model,
		StopReason:  payload.StopReason,
		UsageJSON:   payload.Usage,
		ToolCalls:   toolCallsFromContent(payload.Content),
		WebRequests: webRequestsFromContent(payload.Content),
	}
	if len(payload.Usage) == 0 {
		return result, nil
	}

	var u messageUsage
	if err := json.Unmarshal(payload.Usage, &u); err != nil {
		return result, err
	}
	result.Usage = u.toUsage()
	result.WebRequests = addSyntheticWebRequests(result.WebRequests, u.ServerToolUse.WebSearchRequests)
	return result, nil
}

func toolCallsFromContent(content []contentBlock) []queue.ToolCall {
	var calls []queue.ToolCall
	for _, block := range content {
		if block.Type != "tool_use" || block.Name == "" || isWebContentBlock(block.Type, block.Name) {
			continue
		}
		calls = append(calls, queue.NewToolCall(block.ID, block.Name, block.Input))
	}
	return calls
}

func webRequestsFromContent(content []contentBlock) []queue.WebRequest {
	var requests []queue.WebRequest
	for _, block := range content {
		if !isWebContentBlock(block.Type, block.Name) {
			continue
		}
		requests = append(requests, queue.NewWebRequest(block.ID, block.Name, block.Input))
	}
	return requests
}

func isWebContentBlock(typ, name string) bool {
	value := strings.ToLower(typ + " " + name)
	return strings.Contains(value, "web_search") || strings.Contains(value, "web-fetch") || strings.Contains(value, "web_fetch") ||
		strings.Contains(value, "websearch") || // Claude Code client-side WebSearch tool
		strings.Contains(value, "webfetch") // Claude Code client-side WebFetch tool
}

func addSyntheticWebRequests(requests []queue.WebRequest, count *int64) []queue.WebRequest {
	if count == nil || *count <= int64(len(requests)) {
		return requests
	}
	for i := int64(len(requests)); i < *count; i++ {
		requests = append(requests, queue.NewWebRequest("", "web_search", []byte(`{"source":"usage","web_search_requests":1}`)))
	}
	return requests
}

// toUsage projects Anthropic's usage fields onto the provider-independent
// queue.Usage shape, normalizing away two ways Anthropic differs from OpenAI so
// that cross-provider aggregation (which sums rows from both) stays consistent:
//
//   - Anthropic's input_tokens counts only fresh (uncached) input;
//     cache_read_input_tokens and cache_creation_input_tokens are additional
//     input the model processed. We fold all three into InputTokens so it means
//     "all input tokens" for both providers (matching OpenAI, whose input_tokens
//     already includes cached), leaving CachedInputTokens and CacheWriteTokens
//     as subsets of it.
//   - Anthropic reports no total_tokens field, so we derive TotalTokens as
//     InputTokens + OutputTokens, the same identity OpenAI reports on the wire.
//
// The untouched wire values remain available in usage_json.
func (u messageUsage) toUsage() queue.Usage {
	inputTotal := queue.SumTokens(u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	return queue.Usage{
		InputTokens:       inputTotal,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
		TotalTokens:       queue.SumTokens(inputTotal, u.OutputTokens),
	}
}

type errorPayload struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ExtractError parses an Anthropic-style JSON error body
// ({"type": "error", "error": {"type", "message"}}). ok is false if body
// doesn't match that shape.
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

// StreamResult is the metadata accumulated from a Messages API SSE stream.
type StreamResult struct {
	CompletedResult
	ErrorType    string
	ErrorMessage string
}

// StreamParser incrementally extracts usage metadata from Anthropic Messages
// SSE frames while the proxy streams the same bytes to the client. Usage
// arrives across message_start and message_delta rather than in one terminal
// object, so it is accumulated as events land.
//
// A frame that fails to parse is skipped rather than abandoning the stream:
// one unexpected event must not discard usage already accumulated. The first
// such failure is reported by Result only if no terminal event was ever seen.
type StreamParser struct {
	parser *sse.Parser

	result   StreamResult
	terminal bool
	parseErr error
	usage    messageUsage
	// rawUsage shallow-merges the raw usage objects across events so unknown
	// fields (cache_creation breakdowns, server_tool_use, service_tier, …)
	// survive into usage_json instead of being flattened to the typed subset.
	rawUsage  map[string]json.RawMessage
	toolCalls []streamToolCall
}

type streamToolCall struct {
	Index        int
	ID           string
	Name         string
	Input        []byte
	PartialInput []byte
	Web          bool
}

// NewStreamParser creates a bounded Anthropic Messages SSE parser.
func NewStreamParser(limit int) *StreamParser {
	return &StreamParser{parser: sse.NewParser(limit)}
}

// Feed consumes bytes from the upstream SSE stream. Frame-level failures are
// latched rather than returned, so the caller keeps feeding the stream it is
// already relaying to the client.
func (p *StreamParser) Feed(b []byte) {
	if err := p.parser.Feed(b, p.handleEvent); err != nil {
		p.noteParseErr(err)
	}
}

// Result finalizes the stream and returns the accumulated result. It returns
// an error only when the stream ended before any terminal event — once a
// terminal event has been seen the accumulated usage stands, even if later
// frames were malformed or the capture limit was hit.
func (p *StreamParser) Result() (StreamResult, error) {
	if p.terminal {
		return p.result, nil
	}
	if err := p.parser.Finalize(); err != nil {
		return p.result, err
	}
	if p.parseErr != nil {
		return p.result, p.parseErr
	}
	return p.result, errors.New("anthropic stream ended without terminal event")
}

// PartialResult returns whatever has accumulated so far without requiring a
// terminal event. Used when a stream dies mid-flight: message_start already
// reported real input and cache tokens that were genuinely spent.
func (p *StreamParser) PartialResult() StreamResult {
	return p.result
}

// Terminated reports whether a terminal event (message_stop or an error) has
// been parsed. A client that disconnects after this point still made a
// complete, billable request even though the proxy never read the upstream EOF.
func (p *StreamParser) Terminated() bool {
	return p.terminal
}

func (p *StreamParser) noteParseErr(err error) {
	if p.parseErr == nil {
		p.parseErr = err
	}
}

func (p *StreamParser) handleEvent(ev sse.Event) error {
	if len(bytes.TrimSpace(ev.Data)) == 0 {
		return nil
	}

	var envelope struct {
		Type    string          `json:"type"`
		Message *messagePayload `json:"message"`
		Delta   struct {
			StopReason  string `json:"stop_reason"`
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		Index        int             `json:"index"`
		ContentBlock *contentBlock   `json:"content_block"`
		Usage        json.RawMessage `json:"usage"`
		Error        struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ev.Data, &envelope); err != nil {
		p.noteParseErr(err)
		return nil
	}

	eventType := ev.Event
	if eventType == "" {
		eventType = envelope.Type
	}
	switch eventType {
	case "message_start":
		if envelope.Message != nil {
			p.result.ResponseID = envelope.Message.ID
			p.result.Model = envelope.Message.Model
			p.result.StopReason = envelope.Message.StopReason
			p.mergeUsage(envelope.Message.Usage)
		}
	case "message_delta":
		if envelope.Delta.StopReason != "" {
			p.result.StopReason = envelope.Delta.StopReason
		}
		p.mergeUsage(envelope.Usage)
	case "content_block_start":
		if envelope.ContentBlock != nil && envelope.ContentBlock.Type == "tool_use" && envelope.ContentBlock.Name != "" && !isWebContentBlock(envelope.ContentBlock.Type, envelope.ContentBlock.Name) {
			call := p.toolCall(envelope.Index)
			call.ID = envelope.ContentBlock.ID
			call.Name = envelope.ContentBlock.Name
			input := bytes.TrimSpace(envelope.ContentBlock.Input)
			if len(input) > 0 && !bytes.Equal(input, []byte("{}")) {
				call.Input = append(call.Input[:0], input...)
			}
			p.syncToolCalls()
		} else if envelope.ContentBlock != nil && isWebContentBlock(envelope.ContentBlock.Type, envelope.ContentBlock.Name) {
			call := p.toolCall(envelope.Index)
			call.ID = envelope.ContentBlock.ID
			call.Name = envelope.ContentBlock.Name
			call.Web = true
			input := bytes.TrimSpace(envelope.ContentBlock.Input)
			if len(input) > 0 && !bytes.Equal(input, []byte("{}")) {
				call.Input = append(call.Input[:0], input...)
			}
			p.syncWebRequests()
		}
	case "content_block_delta":
		if envelope.Delta.Type == "input_json_delta" {
			call := p.toolCall(envelope.Index)
			call.PartialInput = append(call.PartialInput, envelope.Delta.PartialJSON...)
			if call.Web {
				p.syncWebRequests()
			} else {
				p.syncToolCalls()
			}
		}
	case "message_stop":
		p.terminal = true
	case "error":
		if envelope.Error.Type != "" || envelope.Error.Message != "" {
			p.result.ErrorType, p.result.ErrorMessage = envelope.Error.Type, envelope.Error.Message
			p.terminal = true
		}
	}
	return nil
}

func (p *StreamParser) toolCall(index int) *streamToolCall {
	for i := range p.toolCalls {
		if p.toolCalls[i].Index == index {
			return &p.toolCalls[i]
		}
	}
	p.toolCalls = append(p.toolCalls, streamToolCall{Index: index})
	return &p.toolCalls[len(p.toolCalls)-1]
}

func (p *StreamParser) syncToolCalls() {
	if len(p.toolCalls) == 0 {
		return
	}
	calls := make([]queue.ToolCall, 0, len(p.toolCalls))
	for _, call := range p.toolCalls {
		if call.Name == "" {
			continue
		}
		input := call.Input
		if len(call.PartialInput) > 0 {
			input = call.PartialInput
		}
		calls = append(calls, queue.NewToolCall(call.ID, call.Name, input))
	}
	p.result.ToolCalls = calls
}

func (p *StreamParser) syncWebRequests() {
	var requests []queue.WebRequest
	for _, call := range p.toolCalls {
		if !call.Web || call.Name == "" {
			continue
		}
		input := call.Input
		if len(call.PartialInput) > 0 {
			input = call.PartialInput
		}
		requests = append(requests, queue.NewWebRequest(call.ID, call.Name, input))
	}
	if p.usage.ServerToolUse.WebSearchRequests != nil {
		requests = addSyntheticWebRequests(requests, p.usage.ServerToolUse.WebSearchRequests)
	}
	p.result.WebRequests = requests
}

// mergeUsage folds one event's usage object into the accumulated state, both
// as typed counters (for the numeric columns) and as raw JSON fields (for
// usage_json). Later events win per field; fields absent from an event leave
// the accumulated value alone.
func (p *StreamParser) mergeUsage(raw json.RawMessage) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		p.noteParseErr(err)
		return
	}
	var u messageUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		p.noteParseErr(err)
		return
	}

	if p.rawUsage == nil {
		p.rawUsage = make(map[string]json.RawMessage, len(fields))
	}
	maps.Copy(p.rawUsage, fields)
	if u.InputTokens != nil {
		p.usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens != nil {
		p.usage.OutputTokens = u.OutputTokens
	}
	if u.CacheCreationInputTokens != nil {
		p.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens != nil {
		p.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}
	if u.ServerToolUse.WebSearchRequests != nil {
		p.usage.ServerToolUse.WebSearchRequests = u.ServerToolUse.WebSearchRequests
	}
	p.syncUsage()
	p.syncWebRequests()
}

// syncUsage projects the accumulated state onto result so that both Result and
// PartialResult always see current values.
func (p *StreamParser) syncUsage() {
	p.result.Usage = p.usage.toUsage()
	if len(p.rawUsage) == 0 {
		return
	}
	if b, err := json.Marshal(p.rawUsage); err == nil {
		p.result.UsageJSON = b
	}
}

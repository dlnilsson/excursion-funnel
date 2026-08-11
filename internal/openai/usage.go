// Package openai parses OpenAI Responses API and Chat Completions API bodies
// to extract usage and error metadata. Parsing is best-effort and tolerant of
// unknown/missing fields: the API shapes can change, and a parse failure here
// must never affect what the proxy has already sent to the client.
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/sse"
)

// CompletedResult is the metadata extracted from a completed (or
// failed/incomplete) Responses API JSON body.
type CompletedResult struct {
	ResponseID string
	Model      string
	Status     string
	Usage      queue.Usage
	UsageJSON  json.RawMessage
	ToolCalls  []queue.ToolCall
}

type responsePayload struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Status  string                 `json:"status"`
	Usage   json.RawMessage        `json:"usage"`
	Output  []responseOutput       `json:"output"`
	Choices []chatCompletionChoice `json:"choices"`
}

// responseOutput covers the function and custom tool-call items in a
// Responses API output. Arguments is a JSON string for function_call, while
// input is a JSON object for other tool variants, so both are retained.
type responseOutput struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	CallID      string          `json:"call_id"`
	Name        string          `json:"name"`
	Arguments   json.RawMessage `json:"arguments"`
	Input       json.RawMessage `json:"input"`
	Command     json.RawMessage `json:"command"`
	Cmd         json.RawMessage `json:"cmd"`
	Description string          `json:"description"`
	Action      json.RawMessage `json:"action"`
}

type chatCompletionChoice struct {
	Message struct {
		ToolCalls []chatCompletionToolCall `json:"tool_calls"`
	} `json:"message"`
}

type chatCompletionToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// responseUsage covers both OpenAI usage shapes. The Responses API reports
// input_tokens/output_tokens with *_tokens_details sub-objects; the Chat
// Completions API reports the same counts as prompt_tokens/completion_tokens
// with prompt_tokens_details/completion_tokens_details. Only one naming
// appears in any given body, so both are decoded and the non-nil one wins.
type responseUsage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	TotalTokens        *int64 `json:"total_tokens"`
	CacheWriteTokens   *int64 `json:"cache_write_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`

	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u responseUsage) toUsage() queue.Usage {
	usage := queue.Usage{
		InputTokens:      firstNonNil(u.InputTokens, u.PromptTokens),
		OutputTokens:     firstNonNil(u.OutputTokens, u.CompletionTokens),
		TotalTokens:      u.TotalTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
	if u.InputTokensDetails != nil {
		usage.CachedInputTokens = u.InputTokensDetails.CachedTokens
	}
	if usage.CachedInputTokens == nil && u.PromptTokensDetails != nil {
		usage.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		usage.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	if usage.ReasoningTokens == nil && u.CompletionTokensDetails != nil {
		usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	// The Responses and Chat Completions APIs both report total_tokens, but if a
	// body ever omits it, derive the same input+output identity rather than
	// letting the row aggregate as a zero total. CachedInputTokens is a subset of
	// InputTokens and ReasoningTokens a subset of OutputTokens, so neither is
	// added here.
	if usage.TotalTokens == nil {
		usage.TotalTokens = queue.SumTokens(usage.InputTokens, usage.OutputTokens)
	}
	return usage
}

func firstNonNil(vals ...*int64) *int64 {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// ExtractCompleted parses a non-streaming Responses API or Chat Completions
// JSON body and returns its id, model, status, and usage. It returns an error
// only when body is not valid JSON matching the expected top-level shape.
func ExtractCompleted(body []byte) (CompletedResult, error) {
	var payload responsePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return CompletedResult{}, err
	}

	result := CompletedResult{
		ResponseID: payload.ID,
		Model:      payload.Model,
		Status:     payload.Status,
		UsageJSON:  payload.Usage,
		ToolCalls:  append(toolCallsFromResponseOutput(payload.Output), toolCallsFromChatChoices(payload.Choices)...),
	}
	if len(payload.Usage) == 0 {
		return result, nil
	}

	var u responseUsage
	if err := json.Unmarshal(payload.Usage, &u); err != nil {
		return result, err
	}
	result.Usage = u.toUsage()
	return result, nil
}

func toolCallsFromResponseOutput(items []responseOutput) []queue.ToolCall {
	var calls []queue.ToolCall
	for _, item := range items {
		name := item.Name
		if name == "" {
			if !isToolOutputType(item.Type) {
				continue
			}
			name = item.Type
		}
		if name == "" {
			continue
		}
		input := responseOutputInput(item)
		calls = append(calls, newCodexToolCall(firstNonEmpty(item.CallID, item.ID), name, decodeToolInput(input)))
	}
	return calls
}

func responseOutputInput(item responseOutput) []byte {
	input := item.Input
	if len(input) == 0 {
		input = item.Arguments
	}
	if len(input) > 0 {
		return input
	}
	if len(item.Command) == 0 && len(item.Cmd) == 0 && item.Description == "" && len(item.Action) == 0 {
		return nil
	}
	fields := map[string]json.RawMessage{}
	if len(item.Command) > 0 {
		fields["command"] = item.Command
	}
	if len(item.Cmd) > 0 {
		fields["cmd"] = item.Cmd
	}
	if item.Description != "" {
		encoded, _ := json.Marshal(item.Description)
		fields["description"] = encoded
	}
	if len(item.Action) > 0 {
		fields["action"] = item.Action
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func toolCallsFromChatChoices(choices []chatCompletionChoice) []queue.ToolCall {
	var calls []queue.ToolCall
	for _, choice := range choices {
		for _, item := range choice.Message.ToolCalls {
			if item.Function.Name == "" {
				continue
			}
			calls = append(calls, newCodexToolCall(item.ID, item.Function.Name, []byte(item.Function.Arguments)))
		}
	}
	return calls
}

// decodeToolInput unwraps APIs that encode the JSON input as a JSON string
// (for example, Responses function_call.arguments) while leaving object input
// untouched for storage and command/description extraction.
func decodeToolInput(raw json.RawMessage) []byte {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return []byte(encoded)
	}
	return raw
}

// newCodexToolCall preserves the raw Codex arguments while normalizing the
// command column from both JSON arguments and the JavaScript shell wrapper
// emitted by Codex clients. Anthropic tool calls intentionally continue to use
// queue.NewToolCall directly.
func newCodexToolCall(id, name string, input []byte) queue.ToolCall {
	call := queue.NewToolCall(id, name, input)
	call.Command = queue.SanitizeToolArguments(call.ArgumentsJSON)
	return call
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type errorPayload struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ExtractError parses an OpenAI-style JSON error body ({"error": {"type",
// "message"}}). ok is false if body doesn't match that shape.
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

// StreamResult is the metadata accumulated from a Responses API SSE stream.
type StreamResult struct {
	CompletedResult
	ErrorType    string
	ErrorMessage string
}

// StreamParser incrementally extracts usage metadata from OpenAI SSE frames
// while the proxy streams the same bytes to the client. It understands both
// Responses API events (response.completed and friends) and Chat Completions
// chunks (object: chat.completion.chunk).
//
// A frame that fails to parse is skipped rather than abandoning the stream:
// one unexpected event must not discard usage a terminal event already
// delivered. The first such failure is reported by Result only if no terminal
// event was ever seen.
type StreamParser struct {
	parser        *sse.Parser
	limit         int
	result        StreamResult
	terminal      bool
	parseErr      error
	chatToolCalls []streamToolCall
	responseTools []streamToolCall
}

type streamToolCall struct {
	Index        int
	HasIndex     bool
	ID           string
	CallID       string
	Type         string
	Name         string
	Input        []byte
	PartialInput []byte
}

// NewStreamParser creates a bounded OpenAI SSE parser.
func NewStreamParser(limit int) *StreamParser {
	return &StreamParser{parser: sse.NewParser(limit), limit: limit}
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
// terminal event has been seen the extracted usage stands, even if later
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
	return p.result, errors.New("openai stream ended without terminal event")
}

// PartialResult returns whatever has accumulated so far without requiring a
// terminal event. Used when a stream dies mid-flight: the tokens already
// reported are real spend and worth recording.
func (p *StreamParser) PartialResult() StreamResult {
	return p.result
}

// Terminated reports whether a terminal event (response.completed/failed/
// incomplete, an error, or the Chat Completions [DONE] sentinel) has been
// parsed. A client that disconnects after this point still made a complete,
// billable request even though the proxy never read the upstream EOF.
func (p *StreamParser) Terminated() bool {
	return p.terminal
}

func (p *StreamParser) noteParseErr(err error) {
	if p.parseErr == nil {
		p.parseErr = err
	}
}

// maxNestDepth bounds how many SSE-in-SSE layers handleEvent will unwrap. The
// ChatGPT Codex backend wraps each Responses frame one layer deep (an outer
// data: line whose value is itself an `event:`/`data:` frame); the cap keeps a
// pathological or adversarial stream from recursing without bound.
const maxNestDepth = 2

func (p *StreamParser) handleEvent(ev sse.Event) error {
	return p.handleEventDepth(ev, 0)
}

func (p *StreamParser) handleEventDepth(ev sse.Event, depth int) error {
	data := bytes.TrimSpace(ev.Data)
	if len(data) == 0 {
		return nil
	}
	// Chat Completions terminates the stream with a [DONE] sentinel. Treat it
	// as terminal: a stream that ended on the provider's own end-of-stream
	// marker completed normally, even when usage was not requested.
	if bytes.Equal(data, []byte("[DONE]")) {
		p.terminal = true
		return nil
	}

	// Some backends (notably the ChatGPT Codex responses endpoint) wrap each
	// Responses frame in a second SSE layer, so after the outer parser strips
	// `data:` the payload is itself `event: ...`/`data: {...}` rather than JSON.
	// Re-run that inner frame through an SSE parser so the real JSON event is
	// recovered, instead of json-parsing it as text (which fails on the leading
	// 'e'). Guarded by depth so nesting can never recurse without bound.
	if depth < maxNestDepth && isNestedSSE(data) {
		inner := sse.NewParser(p.limit)
		if err := inner.Feed(nestedFrameBytes(data), func(e sse.Event) error {
			return p.handleEventDepth(e, depth+1)
		}); err != nil {
			p.noteParseErr(fmt.Errorf("nested-frame: %w", err))
			return nil
		}
		if err := inner.Finalize(); err != nil {
			p.noteParseErr(fmt.Errorf("nested-frame: %w", err))
		}
		return nil
	}

	var envelope struct {
		Type        string          `json:"type"`
		Object      string          `json:"object"`
		ID          string          `json:"id"`
		Model       string          `json:"model"`
		Response    json.RawMessage `json:"response"`
		Usage       json.RawMessage `json:"usage"`
		Choices     json.RawMessage `json:"choices"`
		Item        json.RawMessage `json:"item"`
		ItemID      string          `json:"item_id"`
		OutputIndex *int            `json:"output_index"`
		Delta       string          `json:"delta"`
		Arguments   string          `json:"arguments"`
		Input       json.RawMessage `json:"input"`
		Error       struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ev.Data, &envelope); err != nil {
		p.noteParseErr(fmt.Errorf("outer-frame: %w", err))
		return nil
	}

	eventType := ev.Event
	if eventType == "" {
		eventType = envelope.Type
	}
	switch {
	case eventType == "response.completed", eventType == "response.failed", eventType == "response.incomplete":
		if len(envelope.Response) != 0 {
			result, err := ExtractCompleted(envelope.Response)
			if err != nil {
				p.noteParseErr(err)
			} else {
				p.result.CompletedResult = result
				p.mergeCompletedToolCalls(result.ToolCalls)
				if eventType == "response.failed" {
					p.populateResponseError(envelope.Response)
				}
			}
		}
		if p.result.ErrorType == "" && (envelope.Error.Type != "" || envelope.Error.Message != "") {
			p.result.ErrorType, p.result.ErrorMessage = envelope.Error.Type, envelope.Error.Message
		}
		p.terminal = true
	case eventType == "error":
		if envelope.Error.Type != "" || envelope.Error.Message != "" {
			p.result.ErrorType, p.result.ErrorMessage = envelope.Error.Type, envelope.Error.Message
			p.terminal = true
		}
	case eventType == "response.output_item.added", eventType == "response.output_item.done":
		p.mergeResponseOutputItem(envelope.Item, envelope.OutputIndex)
	case eventType == "response.function_call_arguments.delta", eventType == "response.custom_tool_call_input.delta":
		p.appendResponseToolInput(envelope.ItemID, envelope.OutputIndex, envelope.Delta)
	case eventType == "response.function_call_arguments.done":
		p.setResponseToolInput(envelope.ItemID, envelope.OutputIndex, []byte(envelope.Arguments))
	case eventType == "response.custom_tool_call_input.done":
		p.setResponseToolInput(envelope.ItemID, envelope.OutputIndex, decodeToolInput(envelope.Input))
	case envelope.Object == "chat.completion.chunk":
		p.handleChatChunk(envelope.ID, envelope.Model, envelope.Usage, envelope.Choices)
	}
	return nil
}

func isToolOutputType(typ string) bool {
	return strings.HasSuffix(typ, "_call")
}

func (p *StreamParser) mergeCompletedToolCalls(calls []queue.ToolCall) {
	for _, incoming := range calls {
		if incoming.Name == "" {
			continue
		}
		call := p.responseToolCall(incoming.ID, "", nil)
		call.ID = firstNonEmpty(incoming.ID, call.ID)
		call.Name = incoming.Name
		if incoming.ArgumentsJSON != "" {
			call.Input = []byte(incoming.ArgumentsJSON)
			call.PartialInput = nil
		}
	}
	p.syncToolCalls()
}

// handleChatChunk accumulates identity and usage from a Chat Completions
// chunk. Usage is only present on the final chunk, and only when the client
// asked for it via stream_options.include_usage.
func (p *StreamParser) handleChatChunk(id, model string, usage, choices json.RawMessage) {
	if id != "" {
		p.result.ResponseID = id
	}
	if model != "" {
		p.result.Model = model
	}
	p.mergeChatToolCalls(choices)
	if len(usage) == 0 || bytes.Equal(usage, []byte("null")) {
		return
	}

	var u responseUsage
	if err := json.Unmarshal(usage, &u); err != nil {
		p.noteParseErr(fmt.Errorf("chunk-usage: %w", err))
		return
	}
	p.result.Usage = u.toUsage()
	p.result.UsageJSON = usage
	p.terminal = true
}

func (p *StreamParser) mergeChatToolCalls(raw json.RawMessage) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	var choices []struct {
		Delta struct {
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &choices); err != nil {
		p.noteParseErr(fmt.Errorf("chat-tool-calls: %w", err))
		return
	}
	for _, choice := range choices {
		for _, delta := range choice.Delta.ToolCalls {
			call := p.chatToolCall(delta.Index)
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Function.Name != "" {
				call.Name = delta.Function.Name
			}
			call.PartialInput = append(call.PartialInput, delta.Function.Arguments...)
		}
	}
	p.syncChatToolCalls()
}

func (p *StreamParser) chatToolCall(index int) *streamToolCall {
	for i := range p.chatToolCalls {
		if p.chatToolCalls[i].Index == index {
			return &p.chatToolCalls[i]
		}
	}
	p.chatToolCalls = append(p.chatToolCalls, streamToolCall{Index: index, HasIndex: true})
	return &p.chatToolCalls[len(p.chatToolCalls)-1]
}

func (p *StreamParser) syncChatToolCalls() {
	p.syncToolCalls()
}

// mergeResponseOutputItem captures the tool identity sent in the standard
// Responses streaming events. Codex emits these events before the terminal
// response, and some Codex-compatible backends omit output from that terminal
// response, so relying only on response.completed loses every tool call.
func (p *StreamParser) mergeResponseOutputItem(raw json.RawMessage, outputIndex *int) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var item responseOutput
	if err := json.Unmarshal(raw, &item); err != nil {
		p.noteParseErr(fmt.Errorf("response-tool-item: %w", err))
		return
	}
	name := item.Name
	if name == "" && isToolOutputType(item.Type) {
		name = item.Type
	}
	if name == "" {
		return
	}
	call := p.responseToolCall(item.ID, item.CallID, outputIndex)
	call.ID = firstNonEmpty(item.ID, call.ID)
	call.CallID = firstNonEmpty(item.CallID, call.CallID)
	call.Type = firstNonEmpty(item.Type, call.Type)
	call.Name = name
	input := item.Input
	if len(input) == 0 {
		input = item.Arguments
	}
	if len(input) > 0 {
		decoded := decodeToolInput(input)
		if len(decoded) > 0 && !bytes.Equal(bytes.TrimSpace(decoded), []byte("{}")) {
			call.Input = append(call.Input[:0], decoded...)
			call.PartialInput = nil
		}
	} else if len(call.Input) == 0 && len(call.PartialInput) == 0 {
		// Built-in Codex calls can carry their input directly on the item (for
		// example an action/command object) instead of input or arguments.
		call.Input = append(call.Input[:0], raw...)
	}
	p.syncToolCalls()
}

func (p *StreamParser) appendResponseToolInput(itemID string, outputIndex *int, delta string) {
	if delta == "" {
		return
	}
	call := p.responseToolCall(itemID, "", outputIndex)
	call.PartialInput = append(call.PartialInput, delta...)
	p.syncToolCalls()
}

func (p *StreamParser) setResponseToolInput(itemID string, outputIndex *int, input []byte) {
	call := p.responseToolCall(itemID, "", outputIndex)
	call.Input = append(call.Input[:0], input...)
	call.PartialInput = nil
	p.syncToolCalls()
}

func (p *StreamParser) responseToolCall(itemID, callID string, outputIndex *int) *streamToolCall {
	for i := range p.responseTools {
		call := &p.responseTools[i]
		if itemID != "" && (call.ID == itemID || call.CallID == itemID) {
			return call
		}
		if callID != "" && (call.ID == callID || call.CallID == callID) {
			return call
		}
		if outputIndex != nil && call.HasIndex && call.Index == *outputIndex {
			return call
		}
	}
	call := streamToolCall{}
	if outputIndex != nil {
		call.Index, call.HasIndex = *outputIndex, true
	}
	call.ID, call.CallID = itemID, callID
	p.responseTools = append(p.responseTools, call)
	return &p.responseTools[len(p.responseTools)-1]
}

func (p *StreamParser) syncToolCalls() {
	calls := make([]queue.ToolCall, 0, len(p.responseTools)+len(p.chatToolCalls))
	for _, call := range p.responseTools {
		if call.Name == "" {
			continue
		}
		input := call.Input
		if len(call.PartialInput) > 0 {
			input = call.PartialInput
		}
		calls = append(calls, newCodexToolCall(firstNonEmpty(call.CallID, call.ID), call.Name, input))
	}
	for _, call := range p.chatToolCalls {
		if call.Name != "" {
			calls = append(calls, newCodexToolCall(call.ID, call.Name, call.PartialInput))
		}
	}
	p.result.ToolCalls = calls
}

// isNestedSSE reports whether data (already whitespace-trimmed) is an SSE frame
// wrapped inside another SSE frame's data field rather than a JSON event: it
// does not open with a JSON object/array but does begin with an `event:` or
// `data:` field line.
func isNestedSSE(data []byte) bool {
	if len(data) == 0 || data[0] == '{' || data[0] == '[' {
		return false
	}
	return bytes.HasPrefix(data, []byte("event:")) || bytes.HasPrefix(data, []byte("data:"))
}

// nestedFrameBytes returns data terminated by a blank line so a fresh sse.Parser
// sees one complete frame. It copies rather than appending in place because data
// aliases the outer parser's buffer.
func nestedFrameBytes(data []byte) []byte {
	buf := make([]byte, 0, len(data)+2)
	buf = append(buf, data...)
	return append(buf, '\n', '\n')
}

func (p *StreamParser) populateResponseError(body []byte) {
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	if payload.Error.Type != "" || payload.Error.Message != "" {
		p.result.ErrorType, p.result.ErrorMessage = payload.Error.Type, payload.Error.Message
	}
}

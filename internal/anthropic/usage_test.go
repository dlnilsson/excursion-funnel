package anthropic

import (
	"encoding/json"
	"testing"
)

func TestExtractCompleted_FullUsage(t *testing.T) {
	body := []byte(`{
		"id": "msg_abc123",
		"model": "claude-opus-5",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 10,
			"cache_read_input_tokens": 20
		}
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}

	var (
		wantID         = "msg_abc123"
		wantModel      = "claude-opus-5"
		wantStopReason = "end_turn"
	)
	if got.ResponseID != wantID || got.Model != wantModel || got.StopReason != wantStopReason {
		t.Fatalf("ExtractCompleted() = %+v, want id=%s model=%s stop_reason=%s", got, wantID, wantModel, wantStopReason)
	}

	// InputTokens is normalized to the full prompt: fresh 100 + cache read 20 +
	// cache creation 10 = 130. Cached and CacheWrite remain as subsets of it.
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 130)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 50)
	checkInt64Ptr(t, "CacheWriteTokens", got.Usage.CacheWriteTokens, 10)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 20)
	// Anthropic reports no total_tokens; we derive Input + Output = 130 + 50.
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 180)
	if got.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens = %d, want nil (Anthropic has no reasoning_tokens field)", *got.Usage.ReasoningTokens)
	}
	if len(got.UsageJSON) == 0 {
		t.Fatal("ExtractCompleted() UsageJSON is empty, want raw usage object preserved")
	}
}

func TestExtractCompleted_ToolUse(t *testing.T) {
	body := []byte(`{
		"id": "msg_tool",
		"model": "claude-opus-5",
		"content": [{
			"type": "tool_use",
			"id": "toolu_bash",
			"name": "Bash",
			"input": {"command": "go test ./...", "description": "Run tests"}
		}]
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one call", got.ToolCalls)
	}
	call := got.ToolCalls[0]
	if call.ID != "toolu_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want Bash command and description", call)
	}
	if call.ArgumentsJSON != `{"command": "go test ./...", "description": "Run tests"}` {
		t.Fatalf("ArgumentsJSON = %s, want original input JSON", call.ArgumentsJSON)
	}
}

func TestExtractCompleted_WebSearchUsage(t *testing.T) {
	body := []byte(`{
		"id":"msg_web","model":"claude-opus-5",
		"usage":{"input_tokens":4,"output_tokens":2,"server_tool_use":{"web_search_requests":2}}
	}`)
	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if len(got.WebRequests) != 2 {
		t.Fatalf("WebRequests = %+v, want two synthetic web requests", got.WebRequests)
	}
	for _, request := range got.WebRequests {
		if request.Name != "web_search" || request.ArgumentsJSON == "" {
			t.Fatalf("web request = %+v, want synthetic usage details", request)
		}
	}
}

// Claude Code's WebSearch/WebFetch are client-side tools: the API body carries
// a plain tool_use block named WebSearch/WebFetch (no server_tool_use count).
// These belong in the web-request ledger, not the generic tool-call ledger.
func TestExtractCompleted_ClientWebTools(t *testing.T) {
	body := []byte(`{
		"id":"msg_client_web","model":"claude-opus-5",
		"content":[
			{"type":"tool_use","id":"toolu_ws","name":"WebSearch","input":{"query":"nord theme"}},
			{"type":"tool_use","id":"toolu_wf","name":"WebFetch","input":{"url":"https://example.com/docs"}}
		]
	}`)
	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want none (web tools must not be double-counted)", got.ToolCalls)
	}
	if len(got.WebRequests) != 2 {
		t.Fatalf("WebRequests = %+v, want two client web requests", got.WebRequests)
	}
	search := got.WebRequests[0]
	if search.Name != "WebSearch" || search.Query != "nord theme" {
		t.Fatalf("web search = %+v, want WebSearch with projected query", search)
	}
	fetch := got.WebRequests[1]
	if fetch.Name != "WebFetch" || fetch.URL != "https://example.com/docs" {
		t.Fatalf("web fetch = %+v, want WebFetch with projected url", fetch)
	}
}

func TestExtractCompleted_MissingUsageFields(t *testing.T) {
	body := []byte(`{
		"id": "msg_def456",
		"model": "claude-opus-5",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 100)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 50)
	if got.Usage.CachedInputTokens != nil {
		t.Fatalf("CachedInputTokens = %d, want nil", *got.Usage.CachedInputTokens)
	}
	if got.Usage.CacheWriteTokens != nil {
		t.Fatalf("CacheWriteTokens = %d, want nil", *got.Usage.CacheWriteTokens)
	}
	// Total derives from the present buckets even when cache fields are absent.
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 150)
}

func TestExtractCompleted_MalformedJSON(t *testing.T) {
	if _, err := ExtractCompleted([]byte(`{not json`)); err == nil {
		t.Fatal("ExtractCompleted() error = nil, want error for malformed JSON")
	}
}

func TestExtractError_MatchingShape(t *testing.T) {
	body := []byte(`{"type": "error", "error": {"type": "invalid_request_error", "message": "missing model"}}`)

	errType, errMessage, ok := ExtractError(body)
	if !ok {
		t.Fatal("ExtractError() ok = false, want true")
	}
	if errType != "invalid_request_error" || errMessage != "missing model" {
		t.Fatalf("ExtractError() = (%q, %q), want (invalid_request_error, missing model)", errType, errMessage)
	}
}

func TestExtractError_NonMatchingShape(t *testing.T) {
	if _, _, ok := ExtractError([]byte(`{"id": "msg_abc123", "stop_reason": "end_turn"}`)); ok {
		t.Fatal("ExtractError() ok = true, want false for non-error body")
	}
	if _, _, ok := ExtractError([]byte(`not json`)); ok {
		t.Fatal("ExtractError() ok = true, want false for malformed body")
	}
}

func TestStreamParser_AccumulatesUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	chunks := [][]byte{
		[]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_stream","model":"claude-opus-5","usage":{"input_tokens":13,"cache_creation_input_tokens":5,"cache_read_input_tokens":8}}}

`),
		[]byte(`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":21}}

event: message_stop
data: {"type":"message_stop"}

`),
	}
	for _, chunk := range chunks {
		p.Feed(chunk)
	}

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ResponseID != "msg_stream" || got.Model != "claude-opus-5" || got.StopReason != "end_turn" {
		t.Fatalf("Result() = %+v, want accumulated metadata", got)
	}
	// Full prompt: fresh 13 + cache read 8 + cache creation 5 = 26.
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 26)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 21)
	checkInt64Ptr(t, "CacheWriteTokens", got.Usage.CacheWriteTokens, 5)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 8)
	// Derived total: Input 26 + Output 21.
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 47)
	if got.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens = %d, want nil", *got.Usage.ReasoningTokens)
	}
	if len(got.UsageJSON) == 0 {
		t.Fatal("Result() UsageJSON is empty, want accumulated usage object")
	}
}

func TestStreamParser_AccumulatesToolUseInput(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test ./...\",\"description\":\"Run tests\"}"}}

event: message_stop
data: {"type":"message_stop"}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one call", got.ToolCalls)
	}
	call := got.ToolCalls[0]
	if call.ID != "toolu_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want complete streamed Bash call", call)
	}
}

// The streaming path must classify a client-side WebSearch block the same way
// the non-streaming path does: as a web request, never a generic tool call.
func TestStreamParser_ClientWebSearch(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_ws","name":"WebSearch","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"nord theme\"}"}}

event: message_stop
data: {"type":"message_stop"}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want none (WebSearch must not be double-counted)", got.ToolCalls)
	}
	if len(got.WebRequests) != 1 {
		t.Fatalf("WebRequests = %+v, want one streamed WebSearch", got.WebRequests)
	}
	request := got.WebRequests[0]
	if request.ID != "toolu_ws" || request.Name != "WebSearch" || request.Query != "nord theme" {
		t.Fatalf("web request = %+v, want complete streamed WebSearch call", request)
	}
}

func TestStreamParser_ErrorEvent(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"try later"}}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ErrorType != "overloaded_error" || got.ErrorMessage != "try later" {
		t.Fatalf("Result() error = (%q, %q), want overloaded_error try later", got.ErrorType, got.ErrorMessage)
	}
}

func TestStreamParser_MissingTerminal(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\"}}\n\n"))
	if _, err := p.Result(); err == nil {
		t.Fatal("Result() error = nil, want missing terminal error")
	}
}

// usage_json exists for forward compatibility, so fields the typed struct does
// not know about must survive the streaming path untouched.
func TestStreamParser_UsageJSONPreservesUnknownFields(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_u","model":"claude-opus-5","usage":{"input_tokens":13,"cache_creation":{"ephemeral_5m_input_tokens":40},"service_tier":"standard"}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":21,"server_tool_use":{"web_search_requests":2}}}

event: message_stop
data: {"type":"message_stop"}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 13)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 21)

	var usage map[string]any
	if err := json.Unmarshal(got.UsageJSON, &usage); err != nil {
		t.Fatalf("UsageJSON is not valid JSON: %v (%s)", err, got.UsageJSON)
	}
	// Merged across both events, unknown fields intact.
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation", "service_tier", "server_tool_use"} {
		if _, ok := usage[key]; !ok {
			t.Fatalf("UsageJSON missing %q: %s", key, got.UsageJSON)
		}
	}
	cacheCreation, ok := usage["cache_creation"].(map[string]any)
	if !ok {
		t.Fatalf("cache_creation = %T, want nested object: %s", usage["cache_creation"], got.UsageJSON)
	}
	if cacheCreation["ephemeral_5m_input_tokens"] != float64(40) {
		t.Fatalf("cache_creation.ephemeral_5m_input_tokens = %v, want 40", cacheCreation["ephemeral_5m_input_tokens"])
	}
}

// A stream cut short still knows the input and cache tokens message_start
// reported — that spend is real and must survive as partial state.
func TestStreamParser_PartialResultCarriesStartUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_cut","model":"claude-opus-5","usage":{"input_tokens":97,"cache_read_input_tokens":12}}}

`))

	got := p.PartialResult()
	if got.ResponseID != "msg_cut" || got.Model != "claude-opus-5" {
		t.Fatalf("PartialResult() = %+v, want msg_cut/claude-opus-5", got)
	}
	// Full prompt so far: fresh 97 + cache read 12 = 109.
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 109)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 12)
	// Even a cut-short stream carries a derived total (no output yet): Input 109.
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 109)
	if _, err := p.Result(); err == nil {
		t.Fatal("Result() error = nil, want missing terminal error")
	}
}

func TestStreamParser_MalformedFrameBeforeTerminalKeepsUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_ok","model":"claude-opus-5","usage":{"input_tokens":4}}}

event: junk
data: not-json-at-all

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":6}}

event: message_stop
data: {"type":"message_stop"}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v, want nil after a terminal event", err)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 4)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 6)
}

func checkInt64Ptr(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}

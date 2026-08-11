package openai

import "testing"

func TestExtractCompleted_FullUsage(t *testing.T) {
	body := []byte(`{
		"id": "resp_abc123",
		"model": "gpt-5.3-codex",
		"status": "completed",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150,
			"cache_write_tokens": 10,
			"input_tokens_details": {"cached_tokens": 20},
			"output_tokens_details": {"reasoning_tokens": 5}
		}
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}

	var (
		wantID     = "resp_abc123"
		wantModel  = "gpt-5.3-codex"
		wantStatus = "completed"
	)
	if got.ResponseID != wantID || got.Model != wantModel || got.Status != wantStatus {
		t.Fatalf("ExtractCompleted() = %+v, want id=%s model=%s status=%s", got, wantID, wantModel, wantStatus)
	}

	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 100)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 50)
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 150)
	checkInt64Ptr(t, "CacheWriteTokens", got.Usage.CacheWriteTokens, 10)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 20)
	checkInt64Ptr(t, "ReasoningTokens", got.Usage.ReasoningTokens, 5)

	if len(got.UsageJSON) == 0 {
		t.Fatal("ExtractCompleted() UsageJSON is empty, want raw usage object preserved")
	}
}

func TestExtractCompleted_MissingUsageFields(t *testing.T) {
	body := []byte(`{
		"id": "resp_def456",
		"model": "gpt-5.3-codex",
		"status": "completed",
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}

	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 100)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 50)
	// No total_tokens on the wire, so it is derived as Input + Output.
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 150)
	if got.Usage.CachedInputTokens != nil {
		t.Fatalf("CachedInputTokens = %d, want nil", *got.Usage.CachedInputTokens)
	}
	if got.Usage.ReasoningTokens != nil {
		t.Fatalf("ReasoningTokens = %d, want nil", *got.Usage.ReasoningTokens)
	}
	if got.Usage.CacheWriteTokens != nil {
		t.Fatalf("CacheWriteTokens = %d, want nil", *got.Usage.CacheWriteTokens)
	}
}

func TestExtractCompleted_NoUsageObject(t *testing.T) {
	body := []byte(`{"id": "resp_ghi789", "model": "gpt-5.3-codex", "status": "in_progress"}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if got.Usage.InputTokens != nil {
		t.Fatalf("InputTokens = %d, want nil", *got.Usage.InputTokens)
	}
	if len(got.UsageJSON) != 0 {
		t.Fatalf("UsageJSON = %q, want empty", got.UsageJSON)
	}
}

func TestExtractCompleted_FunctionCall(t *testing.T) {
	body := []byte(`{
		"id": "resp_tool",
		"model": "gpt-5.5",
		"output": [{
			"type": "function_call",
			"call_id": "call_bash",
			"name": "Bash",
			"arguments": "{\"command\":\"go test ./...\",\"description\":\"Run tests\"}"
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
	if call.ID != "call_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want Bash command and description", call)
	}
}

func TestExtractCompleted_CodexJavaScriptToolCall(t *testing.T) {
	body := []byte(`{
		"id": "resp_codex_tool",
		"model": "gpt-5.5-codex",
		"output": [{
			"type": "function_call",
			"call_id": "call_bash",
			"name": "Bash",
			"arguments": "const r = await tools.shell_command({command:\"git status --short\"}); text(r)"
		}]
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one call", got.ToolCalls)
	}
	if got.ToolCalls[0].Command != "git status --short" {
		t.Fatalf("command = %q, want sanitized Codex command", got.ToolCalls[0].Command)
	}
}

func TestExtractCompleted_IgnoresNonToolOutput(t *testing.T) {
	body := []byte(`{
		"id": "resp_message",
		"output": [
			{"type": "reasoning"},
			{"type": "message", "content": [{"type": "output_text", "text": "done"}]}
		]
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want no calls for ordinary output", got.ToolCalls)
	}
}

func TestExtractCompleted_MalformedJSON(t *testing.T) {
	if _, err := ExtractCompleted([]byte(`{not json`)); err == nil {
		t.Fatal("ExtractCompleted() error = nil, want error for malformed JSON")
	}
}

func TestExtractError_MatchingShape(t *testing.T) {
	body := []byte(`{"error": {"type": "invalid_request_error", "message": "missing model"}}`)

	errType, errMessage, ok := ExtractError(body)
	if !ok {
		t.Fatal("ExtractError() ok = false, want true")
	}
	if errType != "invalid_request_error" || errMessage != "missing model" {
		t.Fatalf("ExtractError() = (%q, %q), want (invalid_request_error, missing model)", errType, errMessage)
	}
}

func TestExtractError_NonMatchingShape(t *testing.T) {
	if _, _, ok := ExtractError([]byte(`{"id": "resp_abc123", "status": "completed"}`)); ok {
		t.Fatal("ExtractError() ok = true, want false for non-error body")
	}
	if _, _, ok := ExtractError([]byte(`not json`)); ok {
		t.Fatal("ExtractError() ok = true, want false for malformed body")
	}
}

func TestStreamParser_CompletedUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	chunks := [][]byte{
		[]byte("event: response.created\n"),
		[]byte("data: {\"type\":\"response.created\"}\n\n"),
		[]byte("event: response.completed\n"),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_stream","model":"gpt-5.3-codex","status":"completed","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}` + "\n\n"),
	}
	for _, chunk := range chunks {
		p.Feed(chunk)
	}

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ResponseID != "resp_stream" || got.Model != "gpt-5.3-codex" {
		t.Fatalf("Result() = %+v, want response metadata", got)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 11)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 7)
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 18)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 3)
	checkInt64Ptr(t, "ReasoningTokens", got.Usage.ReasoningTokens, 2)
	if len(got.UsageJSON) == 0 {
		t.Fatal("Result() UsageJSON is empty, want raw usage object")
	}
}

// The ChatGPT Codex backend can wrap each Responses frame in a second SSE
// layer, so the outer data: value is itself `event:`/`data:` text. The parser
// must unwrap it and still extract the terminal usage rather than json-parsing
// the leading 'e'.
func TestStreamParser_NestedSSEFrame(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte("data: event: response.completed\n" +
		`data: data: {"type":"response.completed","response":{"id":"resp_nested","model":"gpt-5.5","status":"completed","usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}` + "\n\n"))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ResponseID != "resp_nested" || got.Model != "gpt-5.5" {
		t.Fatalf("Result() = %+v, want resp_nested/gpt-5.5", got)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 9)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 4)
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 13)
}

// Responses streaming emits tool identity and arguments as separate events.
// The terminal response used by the Codex backend may contain usage without
// repeating output, so the parser must retain the streamed tool call itself.
func TestStreamParser_ResponsesToolCallEvents(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	chunks := [][]byte{
		[]byte(`event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_item","call_id":"call_bash","name":"Bash","arguments":""}}

`),
		[]byte(`event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_item","output_index":0,"delta":"{\"command\":\"go test "}

`),
		[]byte(`event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_item","output_index":0,"delta":"./...\",\"description\":\"Run tests\"}"}

`),
		[]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_tool_stream","model":"gpt-5.5-codex","status":"completed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}

`),
	}
	for _, chunk := range chunks {
		p.Feed(chunk)
	}

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one streamed call", got.ToolCalls)
	}
	call := got.ToolCalls[0]
	if call.ID != "call_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want streamed Bash command and description", call)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 12)
}

func TestStreamParser_FailedEvent(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: response.failed
data: {"type":"response.failed","response":{"id":"resp_bad","model":"gpt-5.3-codex","status":"failed","error":{"type":"server_error","message":"boom"}}}

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ErrorType != "server_error" || got.ErrorMessage != "boom" {
		t.Fatalf("Result() error = (%q, %q), want server_error boom", got.ErrorType, got.ErrorMessage)
	}
}

func TestStreamParser_MissingTerminal(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	if _, err := p.Result(); err == nil {
		t.Fatal("Result() error = nil, want missing terminal error")
	}
}

// A single unparseable frame must not discard usage that a terminal event
// already delivered.
func TestStreamParser_MalformedFrameAfterTerminalKeepsUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_ok","model":"gpt-5.3-codex","usage":{"input_tokens":5,"output_tokens":2}}}

event: junk
data: not-json-at-all

`))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v, want nil after a terminal event", err)
	}
	if got.ResponseID != "resp_ok" {
		t.Fatalf("ResponseID = %q, want resp_ok", got.ResponseID)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 5)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 2)
}

// Without a terminal event, the latched frame error is what Result reports.
func TestStreamParser_MalformedFrameWithoutTerminalReportsError(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte("event: junk\ndata: not-json-at-all\n\n"))
	if _, err := p.Result(); err == nil {
		t.Fatal("Result() error = nil, want the latched frame parse error")
	}
}

func TestStreamParser_ChatCompletionsChunkUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"),
		[]byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.3","choices":[],"usage":{"prompt_tokens":31,"completion_tokens":9,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":16},"completion_tokens_details":{"reasoning_tokens":4}}}` + "\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	for _, chunk := range chunks {
		p.Feed(chunk)
	}

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.ResponseID != "chatcmpl-1" || got.Model != "gpt-5.3" {
		t.Fatalf("Result() = %+v, want chatcmpl-1/gpt-5.3", got)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 31)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 9)
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 40)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 16)
	checkInt64Ptr(t, "ReasoningTokens", got.Usage.ReasoningTokens, 4)
}

// Chat Completions omits usage unless stream_options.include_usage is set. The
// stream still terminated normally, so that must not read as a parse failure.
func TestStreamParser_ChatCompletionsDoneWithoutUsage(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5.3","choices":[{"delta":{"content":"hi"}}]}` + "\n\ndata: [DONE]\n\n"))

	got, err := p.Result()
	if err != nil {
		t.Fatalf("Result() error = %v, want nil for a [DONE]-terminated stream", err)
	}
	if got.ResponseID != "chatcmpl-2" || got.Model != "gpt-5.3" {
		t.Fatalf("Result() = %+v, want chatcmpl-2/gpt-5.3", got)
	}
	if got.Usage.InputTokens != nil || got.Usage.OutputTokens != nil {
		t.Fatalf("Usage = %+v, want no token counts", got.Usage)
	}
}

func TestExtractCompleted_ChatCompletionsBody(t *testing.T) {
	body := []byte(`{
	  "id": "chatcmpl-3",
	  "object": "chat.completion",
	  "model": "gpt-5.3",
	  "usage": {
	    "prompt_tokens": 12,
	    "completion_tokens": 4,
	    "total_tokens": 16,
	    "prompt_tokens_details": {"cached_tokens": 8},
	    "completion_tokens_details": {"reasoning_tokens": 1}
	  }
	}`)

	got, err := ExtractCompleted(body)
	if err != nil {
		t.Fatalf("ExtractCompleted() error = %v", err)
	}
	if got.ResponseID != "chatcmpl-3" || got.Model != "gpt-5.3" {
		t.Fatalf("ExtractCompleted() = %+v, want chatcmpl-3/gpt-5.3", got)
	}
	checkInt64Ptr(t, "InputTokens", got.Usage.InputTokens, 12)
	checkInt64Ptr(t, "OutputTokens", got.Usage.OutputTokens, 4)
	checkInt64Ptr(t, "TotalTokens", got.Usage.TotalTokens, 16)
	checkInt64Ptr(t, "CachedInputTokens", got.Usage.CachedInputTokens, 8)
	checkInt64Ptr(t, "ReasoningTokens", got.Usage.ReasoningTokens, 1)
}

func TestStreamParser_PartialResultBeforeTerminal(t *testing.T) {
	p := NewStreamParser(1024 * 1024)
	p.Feed([]byte(`data: {"id":"chatcmpl-4","object":"chat.completion.chunk","model":"gpt-5.3","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))

	got := p.PartialResult()
	if got.ResponseID != "chatcmpl-4" || got.Model != "gpt-5.3" {
		t.Fatalf("PartialResult() = %+v, want chatcmpl-4/gpt-5.3", got)
	}
	if _, err := p.Result(); err == nil {
		t.Fatal("Result() error = nil, want missing terminal error")
	}
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

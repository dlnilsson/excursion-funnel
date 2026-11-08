package queue

import (
	"encoding/json"
	"testing"
)

func TestExtractToolResults(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []ToolResult
	}{
		{"anthropic error and success", `{"messages":[{"role":"user","content":[
{"type":"tool_result","tool_use_id":"bad","is_error":true,"content":"private output"},
{"type":"tool_result","tool_use_id":"ok","content":"done"}]}]}`,
			[]ToolResult{{ID: "bad", Failed: true}, {ID: "ok"}}},
		{"responses JSON string", `{"input":[{"type":"function_call_output","call_id":"bad","output":"{\"exit_code\":2,\"output\":\"private\"}"}]}`,
			[]ToolResult{{ID: "bad", ExitCode: new(int64(2)), Failed: true}}},
		{"responses JSON object", `{"input":[{"type":"function_call_output","call_id":"ok","output":{"exit_code":0}}]}`,
			[]ToolResult{{ID: "ok", ExitCode: new(int64(0))}}},
		{"custom tool preamble", `{"input":[{"type":"custom_tool_call_output","call_id":"bad","output":"Chunk ID: abc\nWall time: 1 seconds\nProcess exited with code 127\nFinal output:\nprivate"}]}`,
			[]ToolResult{{ID: "bad", ExitCode: new(int64(127)), Failed: true}}},
		{"chat completion", `{"messages":[{"role":"tool","tool_call_id":"ok","content":"Exit code: 0\nOutput:\nExit code: 3"}]}`,
			[]ToolResult{{ID: "ok", ExitCode: new(int64(0))}}},
		{"MCP explicit error", `{"input":[{"type":"function_call_output","call_id":"bad","output":{"isError":true,"content":[{"type":"text","text":"private"}]}}]}`,
			[]ToolResult{{ID: "bad", Failed: true}}},
		{"MCP nested exit code", `{"input":[{"type":"function_call_output","call_id":"bad","output":{"isError":false,"content":[{"type":"text","text":"{\"exit_code\":1}"}]}}]}`,
			[]ToolResult{{ID: "bad", ExitCode: new(int64(1)), Failed: true}}},
		{"responses content blocks", `{"input":[{"type":"function_call_output","call_id":"bad","output":[{"type":"output_text","text":"Exit code: 9\nOutput:\nprivate"}]}]}`,
			[]ToolResult{{ID: "bad", ExitCode: new(int64(9)), Failed: true}}},
		{"unknown output", `{"input":[{"type":"function_call_output","call_id":"a","output":"command failed"}]}`, nil},
		{"malformed metadata", `{"input":[{"type":"function_call_output","call_id":"a","output":{"exit_code":2,"is_error":"bad"}}]}`, nil},
		{"running command", `{"input":[{"type":"function_call_output","call_id":"a","output":{"exit_code":null,"session_id":123}}]}`, nil},
		{"stdout cannot supply status", `{"input":[{"type":"function_call_output","call_id":"a","output":"Output:\nExit code: 1"}]}`, nil},
		{"missing call ID", `{"input":[{"type":"function_call_output","output":{"exit_code":1}}]}`, nil},
		{"user text", `{"messages":[{"role":"user","content":"Exit code: 1"}]}`, nil},
		{"unrelated input", `{"input":[{"type":"message","call_id":"a","output":{"exit_code":1}}]}`, nil},
		{"string input", `{"input":"hello"}`, nil},
		{"malformed request", `{"input":[`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				got         = ExtractToolResults([]byte(test.body))
				gotJSON, _  = json.Marshal(got)
				wantJSON, _ = json.Marshal(test.want)
			)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("results = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

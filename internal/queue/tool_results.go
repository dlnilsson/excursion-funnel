package queue

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ExtractToolResults reads client-returned results from Anthropic Messages,
// OpenAI Responses and Chat Completions requests. Malformed or unrecognized
// results are ignored; output text is inspected but never retained.
func ExtractToolResults(body []byte) []ToolResult {
	var request struct {
		Input    json.RawMessage `json:"input"`
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	var results []ToolResult
	var input []struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(request.Input, &input)
	for _, item := range input {
		if item.Type != "function_call_output" && item.Type != "custom_tool_call_output" {
			continue
		}
		if result, ok := toolOutputResult(item.CallID, item.Output); ok {
			results = append(results, result)
		}
	}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			if result, ok := toolOutputResult(message.ToolCallID, message.Content); ok {
				results = append(results, result)
			}
			continue
		}
		if message.Role != "user" {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" || block.ToolUseID == "" {
				continue
			}
			// Anthropic defines an omitted is_error as false.
			result, _ := toolOutputResult(block.ToolUseID, block.Content)
			result.ID = block.ToolUseID
			result.Failed = result.Failed || block.IsError
			results = append(results, result)
		}
	}
	return results
}

func toolOutputResult(id string, raw json.RawMessage) (ToolResult, bool) {
	return parseToolOutput(id, raw, 0)
}

func parseToolOutput(id string, raw json.RawMessage, depth int) (ToolResult, bool) {
	result := ToolResult{ID: id}
	if id == "" || len(raw) == 0 || depth > 4 {
		return result, false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	var fields struct {
		ExitCode *int64          `json:"exit_code"`
		IsError  *bool           `json:"is_error"`
		MCPError *bool           `json:"isError"`
		Content  json.RawMessage `json:"content"`
	}
	known := false
	if json.Unmarshal(raw, &fields) == nil {
		result.ExitCode = fields.ExitCode
		result.Failed = fields.ExitCode != nil && *fields.ExitCode > 0 ||
			fields.IsError != nil && *fields.IsError || fields.MCPError != nil && *fields.MCPError
		known = fields.ExitCode != nil || fields.IsError != nil || fields.MCPError != nil
		if known && len(fields.Content) == 0 {
			return result, true
		}
	} else {
		fields.Content = nil
	}
	// Some clients wrap results in MCP-style text content blocks.
	content := raw
	if len(fields.Content) > 0 {
		content = fields.Content
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		for _, block := range blocks {
			if block.Type != "text" && block.Type != "output_text" {
				continue
			}
			encoded, _ := json.Marshal(block.Text)
			if nested, ok := parseToolOutput(id, encoded, depth+1); ok {
				known = true
				result.Failed = result.Failed || nested.Failed
				if nested.ExitCode != nil && (result.ExitCode == nil || *nested.ExitCode > 0) {
					result.ExitCode = nested.ExitCode
				}
			}
		}
		return result, known
	}
	// Codex shell results use a metadata preamble. Stop at the output
	// boundary so command stdout cannot impersonate an exit status.
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "Output:" || line == "Final output:" {
			break
		}
		for _, prefix := range []string{"Process exited with code ", "Exit code: "} {
			if value, ok := strings.CutPrefix(line, prefix); ok {
				if code, err := strconv.ParseInt(value, 10, 64); err == nil {
					result.ExitCode, result.Failed = &code, code > 0
					return result, true
				}
			}
		}
	}
	return result, known
}

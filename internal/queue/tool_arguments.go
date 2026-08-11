package queue

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SanitizeToolArguments extracts a shell command from the argument_json saved
// for a tool call. It handles both a JSON object such as
// {"command":"git status"} and the JavaScript wrapper emitted by some
// clients, such as tools.shell_command({command:"git status"}).
//
// The raw argument_json remains the source of truth. An empty string is
// returned when no command field can be extracted.
func SanitizeToolArguments(argumentJSON string) string {
	argumentJSON = strings.TrimSpace(argumentJSON)
	if argumentJSON == "" {
		return ""
	}

	if command, ok := commandFromJSON(argumentJSON); ok {
		return sanitizeShellCommand(command)
	}
	if command, ok := commandFromJavaScript(argumentJSON); ok {
		return sanitizeShellCommand(command)
	}
	return ""
}

func commandFromJSON(input string) (string, bool) {
	var fields struct {
		Command json.RawMessage `json:"command"`
		Cmd     json.RawMessage `json:"cmd"`
		Action  json.RawMessage `json:"action"`
	}
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return "", false
	}

	if command := commandText(fields.Command); command != "" {
		return command, true
	}
	if command := commandText(fields.Cmd); command != "" {
		return command, true
	}
	if len(fields.Action) > 0 {
		var action struct {
			Command json.RawMessage `json:"command"`
			Cmd     json.RawMessage `json:"cmd"`
		}
		if json.Unmarshal(fields.Action, &action) == nil {
			if command := commandText(action.Command); command != "" {
				return command, true
			}
			if command := commandText(action.Cmd); command != "" {
				return command, true
			}
		}
	}
	return "", false
}

// commandFromJavaScript extracts a command property from a JavaScript object
// literal. This intentionally supports the small object shape used by the
// shell tool instead of attempting to evaluate JavaScript.
func commandFromJavaScript(input string) (string, bool) {
	for i := 0; i < len(input); {
		if input[i] == '\'' || input[i] == '"' || input[i] == '`' {
			// Quoted values cannot contain a property boundary that belongs to
			// the surrounding object, so skip them as one token.
			i = skipJavaScriptString(input, i)
			continue
		}
		if input[i] != '{' && input[i] != ',' {
			i++
			continue
		}

		keyStart := skipSpace(input, i+1)
		key, next, ok := javaScriptPropertyName(input, keyStart)
		if !ok {
			i++
			continue
		}
		next = skipSpace(input, next)
		if next >= len(input) || input[next] != ':' {
			i++
			continue
		}
		valueStart := skipSpace(input, next+1)
		value, ok := javaScriptPropertyValue(input, valueStart)
		if ok && (key == "command" || key == "cmd") {
			if command := commandText(json.RawMessage(value)); command != "" {
				return command, true
			}
			if text, ok := jsonStringValue(value); ok {
				return text, text != ""
			}
		}
		i = valueStart
	}
	return "", false
}

func javaScriptPropertyName(input string, start int) (name string, next int, ok bool) {
	if start >= len(input) {
		return "", start, false
	}
	if input[start] == '\'' || input[start] == '"' || input[start] == '`' {
		end := skipJavaScriptString(input, start)
		if end <= start+1 || end > len(input) {
			return "", end, false
		}
		name, ok = unquoteJavaScriptString(input[start:end])
		return name, end, ok
	}

	end := start
	for end < len(input) && isJavaScriptIdentifierChar(input[end]) {
		end++
	}
	if end == start {
		return "", end, false
	}
	return input[start:end], end, true
}

func javaScriptPropertyValue(input string, start int) (string, bool) {
	if start >= len(input) {
		return "", false
	}
	if input[start] == '\'' || input[start] == '"' || input[start] == '`' {
		end := skipJavaScriptString(input, start)
		if end <= start+1 || end > len(input) {
			return "", false
		}
		return input[start:end], true
	}

	depth := 0
	for i := start; i < len(input); i++ {
		switch input[i] {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			if depth == 0 {
				return strings.TrimSpace(input[start:i]), true
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(input[start:i]), true
			}
		}
	}
	return strings.TrimSpace(input[start:]), true
}

func skipJavaScriptString(input string, start int) int {
	quote := input[start]
	for i := start + 1; i < len(input); i++ {
		if input[i] == '\\' {
			i++
			continue
		}
		if input[i] == quote {
			return i + 1
		}
	}
	return len(input)
}

func unquoteJavaScriptString(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal([]byte(raw), &text); err != nil {
			return "", false
		}
		return text, true
	}
	if raw[0] == '`' {
		return raw[1 : len(raw)-1], true
	}
	text, err := strconv.Unquote(`"` + strings.ReplaceAll(raw[1:len(raw)-1], `"`, `\\"`) + `"`)
	if err != nil {
		return "", false
	}
	return text, true
}

func jsonStringValue(raw string) (string, bool) {
	return unquoteJavaScriptString(raw)
}

func isJavaScriptIdentifierChar(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func skipSpace(input string, index int) int {
	for index < len(input) {
		switch input[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func sanitizeShellCommand(command string) string {
	command = strings.TrimSpace(command)
	if inner, ok := unwrapPowerShellArrayCount(command); ok {
		return strings.TrimSpace(inner)
	}
	return command
}

// unwrapPowerShellArrayCount removes the bookkeeping wrapper commonly used
// when a shell tool needs both command output and a count:
// $md = @(rg --files); $md.Count
func unwrapPowerShellArrayCount(command string) (string, bool) {
	if len(command) < 4 || command[0] != '$' {
		return "", false
	}

	nameEnd := 1
	for nameEnd < len(command) && isPowerShellIdentifierChar(command[nameEnd]) {
		nameEnd++
	}
	if nameEnd == 1 {
		return "", false
	}
	name := command[1:nameEnd]
	index := skipSpace(command, nameEnd)
	if index >= len(command) || command[index] != '=' {
		return "", false
	}
	index = skipSpace(command, index+1)
	if index+1 >= len(command) || command[index] != '@' || command[index+1] != '(' {
		return "", false
	}

	close, ok := matchingParenthesis(command, index+1)
	if !ok {
		return "", false
	}
	suffix := strings.TrimSpace(command[close+1:])
	if suffix != "; $"+name+".Count" {
		return "", false
	}
	return command[index+2 : close], true
}

func matchingParenthesis(input string, open int) (int, bool) {
	depth := 0
	var quote byte
	for i := open; i < len(input); i++ {
		if quote != 0 {
			if input[i] == '`' && quote == '"' {
				i++
				continue
			}
			if input[i] == quote {
				if quote == '\'' && i+1 < len(input) && input[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch input[i] {
		case '\'', '"':
			quote = input[i]
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func isPowerShellIdentifierChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

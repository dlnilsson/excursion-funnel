package queue

import "testing"

func TestSanitizeToolArguments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "javascript shell wrapper",
			input: `const r = await tools.shell_command({command:"$md = @(rg --files -g '*.md' -g '*.markdown'); $md.Count","workdir":"C:\\Users\\Daniel\\dev\\excursion-funnel"}); text(r)`,
			want:  `rg --files -g '*.md' -g '*.markdown'`,
		},
		{
			name:  "json command object",
			input: `{"command": "git ls-files | sed 's/.*\\.//' | sort | uniq -c | sort -rn", "description": "Count tracked files by extension"}`,
			want:  `git ls-files | sed 's/.*\.//' | sort | uniq -c | sort -rn`,
		},
		{
			name:  "json cmd alias",
			input: `{"cmd":["git","status","--short"]}`,
			want:  "git status --short",
		},
		{
			name:  "nested action",
			input: `{"action":{"command":"go test ./..."}}`,
			want:  "go test ./...",
		},
		{
			name:  "no command",
			input: `{"description":"Run tests"}`,
			want:  "",
		},
		{
			name:  "malformed input",
			input: `{"command":"go test ./...`,
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeToolArguments(tt.input); got != tt.want {
				t.Fatalf("SanitizeToolArguments() = %q, want %q", got, tt.want)
			}
		})
	}
}

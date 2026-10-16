package ui

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var modelFamilySource = regexp.MustCompile(`(?s)\nfunction modelFamily\(model\) \{\n.*?\n\}\n`)

// TestModelFamily runs the dashboard's modelFamily JavaScript with node,
// so the test covers the exact function shipped in index.html.
func TestModelFamily(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	fn := modelFamilySource.Find(page)
	if fn == nil {
		t.Fatal("modelFamily function not found in index.html")
	}

	tests := map[string]string{
		"claude-opus-5":              "claude-opus",
		"claude-opus-5-5":            "claude-opus",
		"claude-opus-4-8":            "claude-opus",
		"claude-sonnet-5":            "claude-sonnet",
		"claude-sonnet-4-5-20250929": "claude-sonnet",
		"gpt-5.6-sol":                "gpt-sol",
		"gpt-6.1-sol":                "gpt-sol",
		"gpt-6-astra":                "gpt-astra",
		"gpt-5.6-terra":              "gpt-terra",
		"gpt-4o":                     "gpt-4o",
		"unknown":                    "unknown",
		"5":                          "5",
	}
	models := make([]string, 0, len(tests))
	for model := range tests {
		models = append(models, model)
	}
	input, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("marshal models: %v", err)
	}

	var script strings.Builder
	script.Write(fn)
	script.WriteString("const models = ")
	script.Write(input)
	script.WriteString(";\nprocess.stdout.write(JSON.stringify(Object.fromEntries(models.map(m => [m, modelFamily(m)]))));\n")

	cmd := exec.CommandContext(t.Context(), node, "-")
	cmd.Stdin = strings.NewReader(script.String())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run node: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode node output %q: %v", out, err)
	}
	for model, want := range tests {
		if got[model] != want {
			t.Errorf("modelFamily(%q) = %q, want %q", model, got[model], want)
		}
	}
}

package provider_test

import (
	"net/http"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/dlnilsson/excursion-funnel/internal/provider"
)

// headerFor rebuilds the request headers a client fixture describes.
func headerFor(tt clientFixture) http.Header {
	h := http.Header{}
	if tt.userAgent != "" {
		h.Set("User-Agent", tt.userAgent)
	}
	if tt.originator != "" {
		h.Set("Originator", tt.originator)
	}
	return h
}

func TestForPath(t *testing.T) {
	for _, tt := range pathFixtures {
		t.Run(tt.path, func(t *testing.T) {
			if got := provider.ForPath(tt.path); got != tt.want {
				t.Fatalf("ForPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestClientName(t *testing.T) {
	for _, tt := range clientFixtures {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.ClientName(headerFor(tt)); got != tt.want {
				t.Fatalf("ClientName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsWebToolName(t *testing.T) {
	webTools := []string{
		"web_search_call", // OpenAI / Codex
		"web_search",      // Anthropic server-side + synthetic
		"web_fetch",       // Anthropic server-side
		"web-fetch",       // Anthropic hyphenated variant
		"WebSearch",       // Claude Code client-side
		"WebFetch",        // Claude Code client-side
	}
	for _, name := range webTools {
		if !provider.IsWebToolName(name) {
			t.Errorf("IsWebToolName(%q) = false, want true", name)
		}
	}

	nonWeb := []string{"GET", "POST", "CONNECT", "Bash", "Read", "", "search"}
	for _, name := range nonWeb {
		if provider.IsWebToolName(name) {
			t.Errorf("IsWebToolName(%q) = true, want false", name)
		}
	}
}

func TestIsWebToolBlockMatchesEitherTypeOrName(t *testing.T) {
	if !provider.IsWebToolBlock("server_tool_use", "web_search") {
		t.Error("web tool on the name field not recognized")
	}
	if !provider.IsWebToolBlock("web_search_tool_result", "") {
		t.Error("web tool on the type field not recognized")
	}
	if provider.IsWebToolBlock("tool_use", "Bash") {
		t.Error("ordinary tool use classified as web activity")
	}
}

func TestFreshInputSubtractsAnthropicCacheWrites(t *testing.T) {
	for _, tt := range freshInputFixtures {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.FreshInput(tt.provider, tt.input, tt.cached, tt.cacheWrite)
			if got != tt.want {
				t.Fatalf("FreshInput(%s, %d, %d, %d) = %d, want %d",
					tt.provider, tt.input, tt.cached, tt.cacheWrite, got, tt.want)
			}
		})
	}
}

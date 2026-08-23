// Package provider classifies recorded requests: which upstream provider
// served them, which client sent them, which tool calls are web activity, and
// how many input tokens were genuinely fresh.
//
// Each classification exists in two forms — a Go predicate the proxy applies
// while recording, and a SQL expression the reporting queries apply while
// reading. They must agree, or a row means one thing when written and another
// when read. Keeping both forms adjacent, with a test that runs the same
// fixtures through each, is what holds them together; they previously lived in
// different packages and had already drifted.
package provider

import (
	"net/http"
	"regexp"
	"strings"
)

// Provider names recorded in the ledger.
const (
	OpenAI    = "openai"
	Anthropic = "anthropic"
	Unknown   = "unknown"
)

// claudeSDKUserAgent matches the Claude Agent SDK's user agent, which is
// distinct from the Claude Code CLI and is reported separately.
var claudeSDKUserAgent = regexp.MustCompile(`\bsdk-ts\b.*\bagent-sdk/\d+(?:\.\d+)*\b`)

// ForPath returns the provider that serves an endpoint. The OpenAI Responses
// and Anthropic Messages endpoints both live under /v1 but are distinctly
// named, so the endpoint alone is an unambiguous discriminator.
func ForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages"):
		return Anthropic
	case strings.Contains(path, "/responses"), strings.Contains(path, "/chat/completions"):
		return OpenAI
	default:
		return Unknown
	}
}

// SQLForPath is ForPath as a SQL expression over the given column.
func SQLForPath(column string) string {
	return "CASE " +
		"WHEN " + column + " LIKE '%/messages%' THEN 'anthropic' " +
		"WHEN " + column + " LIKE '%/responses%' OR " + column + " LIKE '%/chat/completions%' THEN 'openai' " +
		"ELSE 'unknown' END"
}

// ClientName returns a stable, display-ready client label from safe request
// headers. Raw details stay in user_agent/originator for inspect output.
func ClientName(h http.Header) string {
	return clientName(strings.TrimSpace(h.Get("User-Agent")), strings.TrimSpace(h.Get("Originator")))
}

func clientName(userAgent, originator string) string {
	var (
		lowerUA         = strings.ToLower(userAgent)
		lowerOriginator = strings.ToLower(originator)
	)
	switch {
	case strings.Contains(lowerUA, "zed") || strings.Contains(lowerOriginator, "zed"):
		return "Zed"
	case strings.Contains(lowerUA, "codex-tui"):
		return "Codex CLI"
	case strings.Contains(lowerUA, "codex") || strings.Contains(lowerOriginator, "codex"):
		return "Codex"
	case claudeSDKUserAgent.MatchString(lowerUA):
		return "ACP/sdk"
	case strings.Contains(lowerUA, "claude-cli"):
		return "Claude Code"
	case originator != "":
		return originator
	case userAgent != "":
		return userAgent
	default:
		return "Unknown"
	}
}

// ClientSQL is ClientName as a SQL expression. Rows written by this proxy carry
// the label in client_name and take the first branch; the remaining branches
// reclassify older rows recorded before that column existed.
//
// The ACP/sdk rule has no SQL equivalent — it needs a regexp match that would
// not survive translation to LIKE — so a legacy Agent SDK row falls through to
// its raw user agent rather than being mislabelled. clientNameSQLDivergence in
// the tests records this as the one deliberate exception.
func ClientSQL() string {
	return "CASE " +
		"WHEN client_name IS NOT NULL AND client_name != '' THEN client_name " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%zed%' OR lower(COALESCE(originator, '')) LIKE '%zed%' THEN 'Zed' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%codex-tui%' THEN 'Codex CLI' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%codex%' OR lower(COALESCE(originator, '')) LIKE '%codex%' THEN 'Codex' " +
		"WHEN lower(COALESCE(user_agent, '')) LIKE '%claude-cli%' THEN 'Claude Code' " +
		"WHEN originator IS NOT NULL AND originator != '' THEN originator " +
		"WHEN user_agent IS NOT NULL AND user_agent != '' THEN user_agent " +
		"ELSE 'Unknown' END"
}

// webToolMarkers are the substrings that identify a provider web-tool call.
var webToolMarkers = []string{"web_search", "websearch", "web_fetch", "web-fetch", "webfetch"}

// IsWebToolName reports whether name is a provider web-tool call that belongs
// in the web-request ledger — OpenAI's web_search_call, Claude Code's
// client-side WebSearch/WebFetch, and Anthropic's server-side web_search /
// web_fetch — as opposed to generic forward-proxy traffic (GET/POST/…).
func IsWebToolName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range webToolMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsWebToolBlock reports whether a content block's type or name identifies web
// activity. Anthropic carries the marker on either field depending on whether
// the tool ran server-side or in the client.
func IsWebToolBlock(typ, name string) bool {
	return IsWebToolName(typ + " " + name)
}

// FreshInput returns input tokens the model actually had to read: total input
// minus cache reads, and for Anthropic minus cache writes as well, because
// Anthropic's input total folds both in. It never goes below zero — a provider
// reporting a cache read larger than its input is reporting noise, not credit.
func FreshInput(provider string, input, cached, cacheWrite int64) int64 {
	fresh := input - cached
	if provider == Anthropic {
		fresh -= cacheWrite
	}
	return max(fresh, 0)
}

// FreshInputSQL is FreshInput as a SQL expression over the ledger columns,
// classifying the provider from pathColumn.
func FreshInputSQL(pathColumn string) string {
	return `GREATEST(
   COALESCE(input_tokens, 0) - COALESCE(cached_input_tokens, 0) -
   CASE WHEN ` + SQLForPath(pathColumn) + ` = 'anthropic' THEN COALESCE(cache_write_tokens, 0) ELSE 0 END,
   0
 )`
}

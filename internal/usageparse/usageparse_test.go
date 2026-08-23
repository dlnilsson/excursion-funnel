package usageparse_test

import (
	"errors"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/anthropic"
	"github.com/dlnilsson/excursion-funnel/internal/openai"
	"github.com/dlnilsson/excursion-funnel/internal/usageparse"
)

// providers is every registered parser. Table-driving the contract here means a
// new provider inherits these assertions instead of restating them.
var providers = map[string]usageparse.Provider{
	"openai":    openai.Parser{},
	"anthropic": anthropic.Parser{},
}

func TestExtractErrorReadsSharedEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantOK      bool
		wantType    string
		wantMessage string
	}{
		{
			name: "openai shape", body: `{"error":{"type":"invalid_request_error","message":"bad model"}}`,
			wantOK: true, wantType: "invalid_request_error", wantMessage: "bad model",
		},
		{
			name: "anthropic shape", body: `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
			wantOK: true, wantType: "overloaded_error", wantMessage: "overloaded",
		},
		{name: "message only", body: `{"error":{"message":"boom"}}`, wantOK: true, wantMessage: "boom"},
		{name: "empty error object", body: `{"error":{}}`},
		{name: "no error member", body: `{"id":"resp_1"}`},
		{name: "not json", body: `event: ping`},
		{name: "empty body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errType, errMessage, ok := usageparse.ExtractError([]byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tt.wantOK)
			}
			if errType != tt.wantType || errMessage != tt.wantMessage {
				t.Fatalf("got (%q, %q), want (%q, %q)", errType, errMessage, tt.wantType, tt.wantMessage)
			}
		})
	}
}

func TestProvidersShareTheErrorEnvelope(t *testing.T) {
	const body = `{"error":{"type":"rate_limit_error","message":"slow down"}}`
	for name, parser := range providers {
		t.Run(name, func(t *testing.T) {
			errType, errMessage, ok := parser.ExtractError([]byte(body))
			if !ok || errType != "rate_limit_error" || errMessage != "slow down" {
				t.Fatalf("ExtractError = (%q, %q, %t)", errType, errMessage, ok)
			}
		})
	}
}

func TestStreamParserReportsMissingTerminalEvent(t *testing.T) {
	for name, parser := range providers {
		t.Run(name, func(t *testing.T) {
			stream := parser.NewStreamParser(1 << 16)
			if stream.Terminated() {
				t.Fatal("fresh parser reports a terminal event")
			}
			_, err := stream.Result()
			if !errors.Is(err, usageparse.ErrNoTerminalEvent) {
				t.Fatalf("Result() error = %v, want ErrNoTerminalEvent", err)
			}
		})
	}
}

func TestPartialResultIsAvailableBeforeTermination(t *testing.T) {
	// A stream cut short still has to yield whatever was already reported: those
	// tokens were genuinely spent.
	stream := openai.Parser{}.NewStreamParser(1 << 16)
	stream.Feed([]byte("event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,` +
		`"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"}}` + "\n\n"))
	partial := stream.PartialResult()
	if len(partial.ToolCalls) != 1 || partial.ToolCalls[0].Name != "shell" {
		t.Fatalf("PartialResult tool calls = %+v, want one shell call", partial.ToolCalls)
	}
	if stream.Terminated() {
		t.Fatal("parser reports terminated before a terminal event")
	}
}

func TestCompletedResultCarriesUsageAcrossProviders(t *testing.T) {
	tests := []struct {
		provider string
		body     string
		wantID   string
		wantIn   int64
	}{
		{
			provider: "openai", wantID: "resp_1", wantIn: 12,
			body: `{"id":"resp_1","model":"gpt-5.6","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`,
		},
		{
			provider: "anthropic", wantID: "msg_1", wantIn: 12,
			body: `{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":12,"output_tokens":3}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			result, err := providers[tt.provider].ExtractCompleted([]byte(tt.body))
			if err != nil {
				t.Fatalf("ExtractCompleted: %v", err)
			}
			if result.ResponseID != tt.wantID {
				t.Fatalf("ResponseID = %q, want %q", result.ResponseID, tt.wantID)
			}
			if result.Usage.InputTokens == nil || *result.Usage.InputTokens != tt.wantIn {
				t.Fatalf("InputTokens = %v, want %d", result.Usage.InputTokens, tt.wantIn)
			}
			if len(result.UsageJSON) == 0 {
				t.Fatal("UsageJSON not preserved")
			}
		})
	}
}

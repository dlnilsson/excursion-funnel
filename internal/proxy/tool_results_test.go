package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyCapturesToolResultsWithoutChangingTraffic(t *testing.T) {
	const request = `{"model":"test","input":[{"type":"function_call_output","call_id":"call-1","output":"{\"exit_code\":2,\"output\":\"private tool output\"}"}]}`
	const response = `{"id":"resp-1","model":"test","usage":{"input_tokens":2,"output_tokens":1}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != request {
			t.Errorf("forwarded request changed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(upstream.Close)
	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", request)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != response {
		t.Fatalf("forwarded response changed: %v", err)
	}
	results := sink.one(t).ToolResults
	if len(results) != 1 || results[0].ID != "call-1" || !results[0].Failed || results[0].ExitCode == nil || *results[0].ExitCode != 2 {
		t.Fatalf("tool results = %+v", results)
	}
}

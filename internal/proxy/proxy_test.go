package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/testutil"
)

type recordingSink struct {
	mu     sync.Mutex
	events []queue.UsageEvent
}

type hijackingRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (r *hijackingRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.conn, r.rw, nil
}

func (s *recordingSink) Enqueue(ev queue.UsageEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) one(t *testing.T) queue.UsageEvent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) != 1 {
		t.Fatalf("events len = %d, want 1", len(s.events))
	}
	return s.events[0]
}

func TestProxy_OpenAIStreamingUsage(t *testing.T) {
	body := `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_stream","model":"gpt-5.3-codex","status":"completed","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}

`
	// Codex posts /v1/responses; the proxy strips /v1 for OpenAI routes, so the
	// upstream root sees /responses.
	upstream := streamingUpstream(t, "/responses", body)
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.3-codex","stream":true}`)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != body {
		t.Fatalf("response body = %q, want %q", gotBody, body)
	}

	ev := sink.one(t)
	if !ev.Stream || ev.ResponseID != "resp_stream" || ev.ModelReported != "gpt-5.3-codex" {
		t.Fatalf("event = %+v, want streaming OpenAI metadata", ev)
	}
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 11)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 7)
	checkUsagePtr(t, "TotalTokens", ev.Usage.TotalTokens, 18)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty", ev.ErrorType)
	}
}

func TestProxy_ProjectContextHeadersOverrideBody(t *testing.T) {
	body := `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_context","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}

`
	upstream := streamingUpstream(t, "/responses", body)
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(
		`{"model":"gpt-5.6-sol","stream":true,"input":"<environment_context><cwd>/body/path</cwd></environment_context>"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EF-Cwd", "/header/path")
	req.Header.Set("X-EF-Git-Branch", "feature/header")
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	_ = rec.Result().Body.Close()

	ev := sink.one(t)
	if ev.Directory != "/header/path" || ev.GitBranch != "feature/header" {
		t.Fatalf("project context = (%q, %q), want header values", ev.Directory, ev.GitBranch)
	}
}

func TestProxy_CapturesProviderSessionHeaders(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		header     string
		sessionID  string
		other      string
		otherValue string
	}{
		{name: "openai", path: "/v1/responses", body: `{"model":"gpt-test"}`, header: "session-id", sessionID: "openai-session", other: "session_id", otherValue: "legacy-openai-session"},
		{name: "openai legacy", path: "/v1/responses", body: `{"model":"gpt-test"}`, header: "session_id", sessionID: "legacy-openai-session", other: "X-Claude-Code-Session-Id", otherValue: "wrong-anthropic"},
		{name: "anthropic", path: "/v1/messages", body: `{"model":"claude-test"}`, header: "X-Claude-Code-Session-Id", sessionID: "anthropic-session", other: "session-id", otherValue: "wrong-openai"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if test.name == "openai" {
					_, _ = io.WriteString(w, `{"id":"resp","model":"gpt-test","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"msg","model":"claude-test","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer upstream.Close()

			sink := &recordingSink{}
			p := newTestProxy(t, upstream.URL, upstream.URL, sink)
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(test.header, "  "+test.sessionID+"  ")
			req.Header.Set(test.other, test.otherValue)
			rec := httptest.NewRecorder()
			p.Handler().ServeHTTP(rec, req)
			_ = rec.Result().Body.Close()

			if event := sink.one(t); event.SessionID != test.sessionID || event.CodexSessionID != "" {
				t.Fatalf("session fields = %q/%q, want %q/empty", event.SessionID, event.CodexSessionID, test.sessionID)
			}
		})
	}
}

// The ChatGPT Codex backend streams SSE with no Content-Type header. The proxy
// must still classify a 2xx response to a stream=true request as SSE and parse
// usage from the frames, rather than json-parsing "event: ..." as a JSON body.
func TestProxy_OpenAIStreamingUsageWithoutContentType(t *testing.T) {
	body := `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_noct","model":"gpt-5.5","status":"completed","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}

`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("upstream path = %q, want %q", r.URL.Path, "/responses")
		}
		// Deliberately omit Content-Type, mirroring the ChatGPT Codex backend.
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.5","stream":true}`)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != body {
		t.Fatalf("response body = %q, want %q", gotBody, body)
	}

	ev := sink.one(t)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty (no parse_error)", ev.ErrorType)
	}
	if !ev.Stream || ev.ResponseID != "resp_noct" || ev.ModelReported != "gpt-5.5" {
		t.Fatalf("event = %+v, want streaming OpenAI metadata", ev)
	}
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 5)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 3)
	checkUsagePtr(t, "TotalTokens", ev.Usage.TotalTokens, 8)
}

// The ChatGPT Codex backend can additionally wrap each Responses frame in a
// second SSE layer (an outer data: line whose value is itself `event:`/`data:`
// text) and still omit Content-Type. The proxy must classify it as SSE and the
// parser must unwrap the inner frame to recover usage, not record a parse_error.
func TestProxy_OpenAIStreamingUsageNestedSSE(t *testing.T) {
	body := "data: event: response.completed\n" +
		`data: data: {"type":"response.completed","response":{"id":"resp_nested","model":"gpt-5.5","status":"completed","usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}` + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("upstream path = %q, want %q", r.URL.Path, "/responses")
		}
		// Mirror the ChatGPT Codex backend: nested SSE, no Content-Type.
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.5","stream":true}`)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != body {
		t.Fatalf("response body = %q, want %q", gotBody, body)
	}

	ev := sink.one(t)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q (%q), want empty (no parse_error)", ev.ErrorType, ev.ErrorMessage)
	}
	if !ev.Stream || ev.ResponseID != "resp_nested" || ev.ModelReported != "gpt-5.5" {
		t.Fatalf("event = %+v, want streaming OpenAI metadata", ev)
	}
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 9)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 4)
	checkUsagePtr(t, "TotalTokens", ev.Usage.TotalTokens, 13)
}

func TestProxy_OpenAIStreamingToolCall(t *testing.T) {
	body := `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_item","call_id":"call_bash","name":"Bash","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_item","delta":"{\"cmd\":\"go test ./...\"}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_tool_proxy","model":"gpt-5.5-codex","status":"completed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}

`
	upstream := streamingUpstream(t, "/responses", body)
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.5-codex","stream":true}`)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	ev := sink.one(t)
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one Codex tool call", ev.ToolCalls)
	}
	call := ev.ToolCalls[0]
	if call.ID != "call_bash" || call.Name != "Bash" || call.Command != "go test ./..." {
		t.Fatalf("tool call = %+v, want persisted Codex Bash call", call)
	}
}

func TestProxy_ForwardProxyDoesNotRecordHTTPWebRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robots.txt" || r.URL.RawQuery != "source=test" {
			t.Errorf("upstream URL = %q, want /robots.txt?source=test", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "User-agent: *\nDisallow: /private\n")
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestWebProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	target := upstream.URL + "/robots.txt?source=test"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "web-capture-test")
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("Parse proxy URL: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET through forward proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "User-agent: *\nDisallow: /private\n" {
		t.Fatalf("proxy response = (%d, %q), want successful forwarded body", resp.StatusCode, body)
	}

	sink.mu.Lock()
	n := len(sink.events)
	sink.mu.Unlock()
	if n != 0 {
		t.Fatalf("events len = %d, want no generic forward-proxy event", n)
	}
}

func TestProxy_ForwardProxyDisabledByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be reached when the web proxy is disabled")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("Parse proxy URL: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(upstream.URL + "/robots.txt")
	if err != nil {
		t.Fatalf("GET through forward proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when web proxy disabled", resp.StatusCode)
	}
	sink.mu.Lock()
	n := len(sink.events)
	sink.mu.Unlock()
	if n != 0 {
		t.Fatalf("events len = %d, want none recorded when web proxy disabled", n)
	}
}

func TestProxy_ForwardProxyCONNECTTunnel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "tunneled")
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestWebProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	// Route an HTTPS-style request to the upstream host through CONNECT. The
	// upstream is plain HTTP, so dial the tunnel and speak HTTP over it to
	// prove the tunnel carries bytes end to end past the ServeMux.
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatalf("Parse proxy URL: %v", err)
	}
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("Parse upstream URL: %v", err)
	}
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstreamURL.Host, upstreamURL.Host); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(conn)
	connectResp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if connectResp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200 Connection Established", connectResp.StatusCode)
	}

	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstreamURL.Host); err != nil {
		t.Fatalf("write tunneled GET: %v", err)
	}
	tunneled, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	body, _ := io.ReadAll(tunneled.Body)
	tunneled.Body.Close()
	if string(body) != "tunneled" {
		t.Fatalf("tunneled body = %q, want %q", body, "tunneled")
	}

	_ = conn.Close()
	proxyServer.Close()
	upstream.Close()
	testutil.AssertNoGoroutineLeaks(t, "internal/proxy.(*Proxy).handleConnect.func")
	sink.mu.Lock()
	n := len(sink.events)
	sink.mu.Unlock()
	if n != 0 {
		t.Fatalf("events len = %d, want no generic CONNECT event", n)
	}
}

func TestProxy_ForwardProxyCONNECTDialUsesRequestContext(t *testing.T) {
	p := newTestWebProxy(t, "https://openai.example", "https://anthropic.example", &recordingSink{})
	client, server := net.Pipe()
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, client)
		close(readDone)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-readDone
	})

	type contextKey struct{}
	key := contextKey{}
	want := "request context"
	var got context.Context
	p.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		got = ctx
		return nil, context.Canceled
	}
	req := httptest.NewRequestWithContext(context.WithValue(t.Context(), key, want), http.MethodConnect, "http://target.test:443", nil)
	req.Host = "target.test:443"
	rec := &hijackingRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		conn:             server,
		rw:               bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)),
	}

	p.handleWebConnect(rec, req)
	if got == nil {
		t.Fatal("dial context was not received")
	}
	if value := got.Value(key); value != want {
		t.Fatalf("dial context value = %v, want %q", value, want)
	}
}

func TestProxy_AnthropicStreamingUsage(t *testing.T) {
	body := `event: message_start
data: {"type":"message_start","message":{"id":"msg_stream","model":"claude-opus-5","usage":{"input_tokens":13,"cache_read_input_tokens":8}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test ./...\",\"description\":\"Run tests\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":21}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := streamingUpstream(t, "/v1/messages", body)
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/messages", `{"model":"claude-opus-5","stream":true}`)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != body {
		t.Fatalf("response body = %q, want %q", gotBody, body)
	}

	ev := sink.one(t)
	if !ev.Stream || ev.ResponseID != "msg_stream" || ev.ModelReported != "claude-opus-5" {
		t.Fatalf("event = %+v, want streaming Anthropic metadata", ev)
	}
	// Input is normalized to include cache read: fresh 13 + cache read 8 = 21.
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 21)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 21)
	checkUsagePtr(t, "CachedInputTokens", ev.Usage.CachedInputTokens, 8)
	// Anthropic sends no total_tokens; derived from Input 21 + Output 21.
	checkUsagePtr(t, "TotalTokens", ev.Usage.TotalTokens, 42)
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want one Bash call", ev.ToolCalls)
	}
	call := ev.ToolCalls[0]
	if call.ID != "toolu_bash" || call.Name != "Bash" || call.Command != "go test ./..." || call.Description != "Run tests" {
		t.Fatalf("tool call = %+v, want complete streamed Bash call", call)
	}
}

// The client's Accept-Encoding is forwarded verbatim, so the upstream can
// return a gzip-compressed SSE body. The proxy must relay those bytes to the
// client untouched while still decoding its own telemetry copy — otherwise the
// parser sees gzip noise, never reaches message_stop, and falsely reports a
// parse_error on an otherwise-healthy 200 stream.
func TestProxy_AnthropicGzipStreamingUsage(t *testing.T) {
	raw := `event: message_start
data: {"type":"message_start","message":{"id":"msg_gzip","model":"claude-opus-4-8","usage":{"input_tokens":13,"cache_read_input_tokens":8}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":21}}

event: message_stop
data: {"type":"message_stop"}

`
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	compressed := gz.Bytes()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/messages", `{"model":"claude-opus-4-8","stream":true}`)
	defer resp.Body.Close()

	gotBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(gotBody, compressed) {
		t.Fatalf("client body was re-encoded; proxy must stay byte-transparent")
	}

	ev := sink.one(t)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q (%s), want empty", ev.ErrorType, ev.ErrorMessage)
	}
	if ev.ResponseID != "msg_gzip" || ev.ModelReported != "claude-opus-4-8" {
		t.Fatalf("event = %+v, want decoded gzip metadata", ev)
	}
	testutil.AssertNoGoroutineLeaks(t, "internal/proxy.newDecodingCapture.func")
	// Input normalized to include cache read: fresh 13 + cache read 8 = 21.
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 21)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 21)
	checkUsagePtr(t, "CachedInputTokens", ev.Usage.CachedInputTokens, 8)
}

// An encoding the stdlib cannot decode must not become a bogus parse_error: the
// client still gets its bytes, and the row simply carries no usage.
func TestProxy_UndecodableEncodingSkipsUsageWithoutParseError(t *testing.T) {
	body := []byte("this is not really brotli, but the proxy cannot decode br")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/messages", `{"model":"claude-opus-4-8","stream":true}`)
	defer resp.Body.Close()

	gotBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("client body altered; proxy must stay byte-transparent")
	}

	ev := sink.one(t)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty for an undecodable body", ev.ErrorType)
	}
}

// The proxy pins the upstream to gzip when the client accepts it, so the usage
// parser always gets a stdlib-decodable body regardless of what fancier codec
// (br, zstd) the client also advertised.
func TestProxy_ForcesGzipUpstreamWhenClientAcceptsIt(t *testing.T) {
	raw := `event: message_start
data: {"type":"message_start","message":{"id":"msg_forced","model":"claude-opus-4-8","usage":{"input_tokens":5}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`
	var gotAcceptEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write([]byte(raw))
		_ = zw.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gz.Bytes())
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-opus-4-8","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "br, gzip, deflate")
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if gotAcceptEncoding != "gzip" {
		t.Fatalf("upstream Accept-Encoding = %q, want %q", gotAcceptEncoding, "gzip")
	}

	ev := sink.one(t)
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q (%s), want empty", ev.ErrorType, ev.ErrorMessage)
	}
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 5)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 9)
}

func TestClientAcceptsGzip(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "missing", header: "", want: false},
		{name: "gzip only", header: "gzip", want: true},
		{name: "list with gzip", header: "br, gzip, deflate", want: true},
		{name: "wildcard", header: "*", want: true},
		{name: "gzip with q", header: "gzip;q=0.5", want: true},
		{name: "gzip disabled by q0", header: "gzip;q=0", want: false},
		{name: "wildcard disabled by q0", header: "*;q=0", want: false},
		{name: "no gzip", header: "br, zstd", want: false},
		{name: "identity only", header: "identity", want: false},
		{name: "case insensitive", header: "GZIP", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.header != "" {
				h.Set("Accept-Encoding", tt.header)
			}
			if got := clientAcceptsGzip(h); got != tt.want {
				t.Fatalf("clientAcceptsGzip(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestUpstreamPath(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		path     string
		want     string
	}{
		{"openai strips v1 responses", "openai", "/v1/responses", "/responses"},
		{"openai strips v1 chat", "openai", "/v1/chat/completions", "/chat/completions"},
		{"openai without v1 unchanged", "openai", "/responses", "/responses"},
		{"openai bare v1 becomes root", "openai", "/v1", "/"},
		{"anthropic verbatim", "anthropic", "/v1/messages", "/v1/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upstreamPath(tt.provider, tt.path); got != tt.want {
				t.Fatalf("upstreamPath(%q, %q) = %q, want %q", tt.provider, tt.path, got, tt.want)
			}
		})
	}
}

func TestProxy_BrokenStreamingUsageRecordsParseError(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	upstream := streamingUpstream(t, "/responses", body)
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.3-codex","stream":true}`)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != body {
		t.Fatalf("response body = %q, want %q", gotBody, body)
	}

	ev := sink.one(t)
	if ev.ErrorType != "parse_error" {
		t.Fatalf("ErrorType = %q, want parse_error", ev.ErrorType)
	}
}

func TestProxy_UpstreamResponseHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"id":"late"}`)
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p, err := NewWithOptions(upstream.URL, upstream.URL, sink, slog.New(slog.DiscardHandler), Options{
		RequestTimeout: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}

	resp := postJSON(t, p.Handler(), "/v1/responses", `{"model":"gpt-5.3-codex"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Fatalf("events len = %d, want 0", len(sink.events))
	}
}

// An idle stream is an upstream-side failure: the caller is still connected and
// tokens may already have been spent, so it must produce a row.
func TestProxy_IdleStreamTimeoutRecordsInterruptedStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p, err := NewWithOptions(upstream.URL, upstream.URL, sink, slog.New(slog.DiscardHandler), Options{
		IdleWriteTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	start := time.Now()
	resp, err := http.Post(proxyServer.URL+"/v1/responses", "application/json", bytes.NewBufferString(`{"model":"gpt-5.3-codex","stream":true}`))
	if err != nil {
		t.Fatalf("POST proxy: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle stream returned after %v, want under 1s", elapsed)
	}

	ev := waitForEvent(t, sink)
	if ev.ErrorType != errStreamInterrupted {
		t.Fatalf("ErrorType = %q, want %q", ev.ErrorType, errStreamInterrupted)
	}
	if ev.ModelRequested != "gpt-5.3-codex" || !ev.Stream {
		t.Fatalf("event = %+v, want the streaming request's identity preserved", ev)
	}
}

// An upstream that dies mid-stream must still produce a row carrying whatever
// usage was already reported — that spend is real.
func TestProxy_UpstreamDiesMidStreamRecordsPartialUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"id":"msg_cut","model":"claude-opus-5","usage":{"input_tokens":97,"cache_read_input_tokens":12}}}

`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack and slam the socket so the proxy sees a read failure rather
		// than a clean EOF.
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	resp, err := http.Post(proxyServer.URL+"/v1/messages", "application/json", bytes.NewBufferString(`{"model":"claude-opus-5","stream":true}`))
	if err != nil {
		t.Fatalf("POST proxy: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	ev := waitForEvent(t, sink)
	if ev.ErrorType != errStreamInterrupted {
		t.Fatalf("ErrorType = %q, want %q", ev.ErrorType, errStreamInterrupted)
	}
	if ev.ResponseID != "msg_cut" || ev.ModelReported != "claude-opus-5" {
		t.Fatalf("event = %+v, want partial identity from message_start", ev)
	}
	// Input normalized to include cache read: fresh 97 + cache read 12 = 109.
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 109)
	checkUsagePtr(t, "CachedInputTokens", ev.Usage.CachedInputTokens, 12)
}

// A client that hangs up mid-stream is not a completed request; there is
// nothing to attribute, so no row is written.
func TestProxy_ClientDisconnectRecordsNothing(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyServer.URL+"/v1/responses",
		bytes.NewBufferString(`{"model":"gpt-5.3-codex","stream":true}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST proxy: %v", err)
	}
	// Read the first frame so the copy is underway, then hang up.
	buf := make([]byte, 16)
	_, _ = resp.Body.Read(buf)
	cancel()
	_ = resp.Body.Close()

	// Give the handler time to notice and finish; it must stay silent.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := len(sink.events)
		sink.mu.Unlock()
		if n != 0 {
			t.Fatalf("events len = %d, want 0 for a client disconnect", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProxy_ClientDisconnectAfterTerminalRecordsUsage covers Codex's real
// behavior: it closes the socket the instant it reads the terminal SSE event,
// before the proxy reads the upstream EOF. The full response was delivered and
// parsed, so the request must still be recorded with its usage — not discarded
// as an abandoned disconnect.
func TestProxy_ClientDisconnectAfterTerminalRecordsUsage(t *testing.T) {
	const body = `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_disc","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}

`
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold the connection open past the terminal event so the proxy is still
		// mid-copy when the client hangs up — exactly the window Codex closes in.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)

	sink := &recordingSink{}
	p := newTestProxy(t, upstream.URL, upstream.URL, sink)
	proxyServer := httptest.NewServer(p.Handler())
	defer proxyServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyServer.URL+"/v1/responses",
		bytes.NewBufferString(`{"model":"gpt-5.6-sol","stream":true}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST proxy: %v", err)
	}
	// Drain exactly the terminal frame the upstream wrote, then hang up.
	if _, err := io.ReadFull(resp.Body, make([]byte, len(body))); err != nil {
		t.Fatalf("read terminal frame: %v", err)
	}
	cancel()
	_ = resp.Body.Close()

	ev := waitForEvent(t, sink)
	if ev.ModelReported != "gpt-5.6-sol" {
		t.Fatalf("ModelReported = %q, want %q", ev.ModelReported, "gpt-5.6-sol")
	}
	if ev.ErrorType != "" {
		t.Fatalf("ErrorType = %q, want empty (complete response)", ev.ErrorType)
	}
	checkUsagePtr(t, "InputTokens", ev.Usage.InputTokens, 11)
	checkUsagePtr(t, "OutputTokens", ev.Usage.OutputTokens, 7)
	checkUsagePtr(t, "TotalTokens", ev.Usage.TotalTokens, 18)
}

func TestPeekClientContext(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		directory string
		gitBranch string
	}{
		{
			name:      "claude code environment block",
			body:      `{"system":[{"type":"text","text":"Environment:\n- Working directory: C:\\Users\\Daniel\\dev\\excursion-funnel\n- Current branch: feature/project-context\n- Platform: win32"}]}`,
			directory: `C:\Users\Daniel\dev\excursion-funnel`,
			gitBranch: "feature/project-context",
		},
		{
			name:      "codex environment context",
			body:      `{"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>C:\\Users\\Daniel\\dev\\excursion-funnel</cwd>\n  <shell>powershell</shell>\n</environment_context>"}]}]}`,
			directory: `C:\Users\Daniel\dev\excursion-funnel`,
		},
		{name: "missing", body: `{"model":"gpt-5.6-sol"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory, branch := peekClientContext([]byte(test.body))
			if directory != test.directory || branch != test.gitBranch {
				t.Fatalf("peekClientContext() = (%q, %q), want (%q, %q)", directory, branch, test.directory, test.gitBranch)
			}
		})
	}
}

func waitForEvent(t *testing.T, sink *recordingSink) queue.UsageEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := len(sink.events)
		sink.mu.Unlock()
		if n > 0 {
			return sink.one(t)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a usage event")
	return queue.UsageEvent{}
}

func streamingUpstream(t *testing.T, wantPath, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Fatalf("upstream path = %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
}

func newTestProxy(t *testing.T, openaiUpstream, anthropicUpstream string, sink EventSink) *Proxy {
	t.Helper()
	p, err := NewWithOptions(openaiUpstream, anthropicUpstream, sink, slog.New(slog.DiscardHandler), Options{})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}
	return p
}

func newTestWebProxy(t *testing.T, openaiUpstream, anthropicUpstream string, sink EventSink) *Proxy {
	t.Helper()
	p, err := NewWithOptions(openaiUpstream, anthropicUpstream, sink, slog.New(slog.DiscardHandler), Options{WebProxyEnabled: true})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}
	return p
}

func postJSON(t *testing.T, h http.Handler, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func checkUsagePtr(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}

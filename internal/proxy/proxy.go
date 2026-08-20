// Package proxy is a transparent multi-provider HTTP proxy for AI-coding-agent
// traffic (Codex over the OpenAI Responses API, Claude Code over the Anthropic
// Messages API).
//
// It selects the upstream by inspecting the request endpoint. Anthropic paths
// are forwarded verbatim; OpenAI paths have their leading /v1 stripped so the
// same client request reaches either the ChatGPT Codex backend
// (…/backend-api/codex/responses) or the platform API (…/v1/responses) purely by
// swapping the configured upstream root. The request body is always
// byte-transparent — only the path prefix is normalized.
//
// Usage-event emission is best-effort: non-streaming JSON bodies are captured
// up to a bound, and streaming SSE chunks are parsed as they pass through.
// Parse failures are recorded in telemetry and never change bytes sent to the
// client.
package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/anthropic"
	"github.com/dlnilsson/excursion-funnel/internal/openai"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
)

var (
	claudeSDKUserAgent = regexp.MustCompile(`\bsdk-ts\b.*\bagent-sdk/\d+(?:\.\d+)*\b`)
	workingDirectoryRE = regexp.MustCompile(`(?mi)^[ \t]*(?:-[ \t]*)?Working directory:[ \t]*([^\r\n]+?)[ \t]*$`)
	currentBranchRE    = regexp.MustCompile(`(?mi)^[ \t]*(?:-[ \t]*)?Current branch:[ \t]*([^\r\n]+?)[ \t]*$`)
	codexCwdRE         = regexp.MustCompile(`(?is)<cwd>[ \t\r\n]*(.*?)[ \t\r\n]*</cwd>`)
)

// maxCaptureBytes bounds how much of a non-streaming response body is
// buffered for usage parsing. Responses API / Messages API JSON bodies are
// small; the cap just protects against an unexpectedly huge or misbehaving
// upstream body.
const maxCaptureBytes = 1 << 20 // 1 MiB

const copyBufferSize = 32 * 1024

var copyBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, copyBufferSize)
		return &buf
	},
}

func getCopyBuffer() []byte {
	return *copyBufferPool.Get().(*[]byte)
}

func putCopyBuffer(buf []byte) {
	if cap(buf) != copyBufferSize {
		return
	}
	buf = buf[:copyBufferSize]
	copyBufferPool.Put(&buf)
}

// EventSink receives a terminal UsageEvent once a proxied request completes.
// Enqueue must never block the caller for long — *queue.Queue satisfies this
// by dropping events under sustained backpressure instead of blocking.
type EventSink interface {
	Enqueue(queue.UsageEvent)
}

// hopByHop headers are connection-scoped and must not be forwarded across the
// proxy. Keys are in http.CanonicalHeaderKey form.
var hopByHop = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// Proxy forwards traffic to one of two provider upstreams, chosen per request
// by endpoint.
type Proxy struct {
	openai           *url.URL // upstream root for OpenAI Responses traffic
	anthropic        *url.URL // upstream root for Anthropic Messages traffic
	client           *http.Client
	webClient        *http.Client
	dialer           *net.Dialer
	sink             EventSink
	log              *slog.Logger
	idleWriteTimeout time.Duration
	source           string
	host             string
	webProxyEnabled  bool
}

// Options tunes proxy-side timeouts. Zero durations disable that timeout.
type Options struct {
	RequestTimeout   time.Duration
	IdleWriteTimeout time.Duration
	Source           string
	Host             string
	// WebProxyEnabled turns on the opt-in forward/CONNECT proxy for
	// non-provider web traffic. When false, absolute-form and CONNECT
	// requests, and unroutable origin-form paths, are rejected rather than
	// forwarded — keeping the daemon a provider-only LLM proxy by default.
	WebProxyEnabled bool
}

// NewWithOptions builds a Proxy with explicit timeout settings.
func NewWithOptions(openaiUpstream, anthropicUpstream string, sink EventSink, log *slog.Logger, opts Options) (*Proxy, error) {
	oa, err := url.Parse(openaiUpstream)
	if err != nil {
		return nil, errors.New("proxy: invalid openai upstream: " + err.Error())
	}
	an, err := url.Parse(anthropicUpstream)
	if err != nil {
		return nil, errors.New("proxy: invalid anthropic upstream: " + err.Error())
	}

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: opts.RequestTimeout,
		// Pass the client's Accept-Encoding through untouched and never
		// auto-decompress upstream bodies — keeps the proxy byte-transparent.
		DisableCompression: true,
	}
	webTransport := transport.Clone()
	// Requests arriving through this process's forward-proxy endpoint must go
	// directly to their destination. Reusing the environment proxy here would
	// recurse when HTTP_PROXY/HTTPS_PROXY points at this same daemon.
	webTransport.Proxy = nil
	return &Proxy{
		openai:    oa,
		anthropic: an,
		client: &http.Client{
			Transport: transport,
			// No client timeout: SSE streams are long-lived. Cancellation is
			// driven by the request context instead.
			Timeout: 0,
			// Surface upstream redirects to the client instead of following.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		webClient: &http.Client{
			Transport: webTransport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		dialer:           dialer,
		sink:             sink,
		log:              log,
		idleWriteTimeout: opts.IdleWriteTimeout,
		source:           opts.Source,
		host:             opts.Host,
		webProxyEnabled:  opts.WebProxyEnabled,
	}, nil
}

// Handler returns the routed HTTP handler.
//
// CONNECT and absolute-form (forward-proxy) requests are dispatched to
// handleProxy directly, ahead of the ServeMux. A CONNECT request carries its
// target in authority-form with an empty URL.Path, which ServeMux cannot match
// against the "/" pattern — routing it through the mux would 404 it before the
// CONNECT tunnel handler ever ran.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealth)
	mux.HandleFunc("/", p.handleProxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || r.URL.IsAbs() {
			p.handleProxy(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (p *Proxy) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// route selects the upstream root and provider name from the request path. The
// OpenAI Responses and Anthropic Messages endpoints both live under /v1 but are
// distinctly named, so the endpoint alone is an unambiguous discriminator.
func (p *Proxy) route(path string) (base *url.URL, provider string, ok bool) {
	switch {
	case strings.Contains(path, "/messages"):
		return p.anthropic, "anthropic", true
	case strings.Contains(path, "/responses"), strings.Contains(path, "/chat/completions"):
		return p.openai, "openai", true
	default:
		// TODO: ambiguous endpoints like /v1/models are not routed yet.
		return nil, "", false
	}
}

func (p *Proxy) handleProxy(w http.ResponseWriter, r *http.Request) {
	var (
		reqID = newRequestID()
		start = time.Now()
	)
	// Standard forward-proxy requests use absolute-form URLs; CONNECT carries
	// its destination in Host. Handle both before provider path routing so a
	// web URL containing /messages or /responses is never misclassified. The
	// forward proxy is opt-in; when disabled these are rejected rather than
	// forwarded so the daemon stays a provider-only LLM proxy.
	if r.Method == http.MethodConnect || r.URL.IsAbs() {
		if !p.webProxyEnabled {
			p.log.Warn("web proxy disabled", "req_id", reqID, "method", r.Method, "host", r.Host)
			http.Error(w, "forward proxy is disabled; start ef with -web-proxy to enable", http.StatusForbidden)
			return
		}
		p.handleWebProxy(w, r)
		return
	}

	base, provider, ok := p.route(r.URL.Path)
	if !ok {
		if !p.webProxyEnabled {
			p.log.Warn("unroutable request", "req_id", reqID, "method", r.Method, "path", r.URL.Path)
			http.Error(w, "no upstream for this endpoint", http.StatusNotFound)
			return
		}
		p.handleWebProxy(w, r)
		return
	}

	// Join the (provider-normalized) client path onto the provider root.
	target := *base
	target.Path = singleJoiningSlash(base.Path, upstreamPath(provider, r.URL.Path))
	target.RawQuery = r.URL.RawQuery

	// Read the body fully so we can forward it unchanged and cheaply peek at
	// model/stream for logging. Request bodies here are small.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.log.Warn("read request body", "req_id", reqID, "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	model, stream := peekModelStream(body)
	directory, gitBranch := peekClientContext(body)

	// Bind the upstream request to the client context so a Codex/Claude Code
	// disconnect cancels the upstream call instead of leaking a stream. Keep
	// our own cancel hook so the idle body timer can abort stalled streams too.
	upstreamCtx, cancelUpstream := context.WithCancel(r.Context())
	defer cancelUpstream()
	upReq, err := http.NewRequestWithContext(upstreamCtx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		p.log.Error("build upstream request", "req_id", reqID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	copyHeaders(upReq.Header, r.Header)
	// Constrain the upstream to gzip whenever the client can accept it. The
	// upstream then returns gzip or identity — both stdlib-decodable — so the
	// usage parser always sees the real SSE frames instead of, say, brotli it
	// cannot read. The client still receives an encoding it advertised. When the
	// client cannot take gzip, its Accept-Encoding is forwarded untouched and the
	// capture falls back to best-effort decoding.
	if clientAcceptsGzip(r.Header) {
		upReq.Header.Set("Accept-Encoding", "gzip")
	}
	upReq.ContentLength = int64(len(body))

	p.log.Info("proxy start",
		"req_id", reqID, "provider", provider, "method", r.Method, "path", r.URL.Path,
		"model", model, "stream", stream)

	resp, err := p.client.Do(upReq)
	if err != nil {
		p.log.Error("upstream request failed", "req_id", reqID, "provider", provider, "err", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Excursion-Request-Id", reqID)
	w.WriteHeader(resp.StatusCode)

	isSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	// Some OpenAI backends stream SSE without a Content-Type header — notably the
	// ChatGPT Codex backend at chatgpt.com/backend-api/codex. A 2xx response to a
	// stream=true request is such a stream, so classify it as SSE; otherwise the
	// usage parser would json-parse "event: ..." as a body and record a spurious
	// parse_error. Non-2xx bodies stay on the JSON path so error extraction still
	// works even when the error response also omits Content-Type.
	if !isSSE && stream && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		isSSE = true
	}
	var capture bodyCapture
	if isSSE {
		capture = newStreamCapture(provider, maxCaptureBytes)
	} else {
		capture = newTeeCapture(maxCaptureBytes)
	}

	// The client's Accept-Encoding is forwarded verbatim, so the upstream body
	// can arrive compressed. flushingCopy relays those bytes to the client
	// untouched, but the usage parser needs the decoded stream — otherwise it
	// sees gzip noise, finds no SSE frames, and misreports a parse_error. Decode
	// the telemetry copy here; the client-facing bytes are never affected.
	contentEncoding := resp.Header.Get("Content-Encoding")
	sink, parseable, finishCapture := newCaptureSink(contentEncoding, capture)
	if !parseable {
		p.log.Warn("usage capture skipped: undecodable content-encoding",
			"req_id", reqID, "provider", provider, "content_encoding", contentEncoding)
	}

	var onChunk func()
	if p.idleWriteTimeout > 0 {
		timer := time.AfterFunc(p.idleWriteTimeout, cancelUpstream)
		defer timer.Stop()
		onChunk = func() {
			timer.Reset(p.idleWriteTimeout)
		}
	}

	n, cerr := flushingCopy(w, resp.Body, sink, p.idleWriteTimeout, onChunk)
	// Drain and close the decoder (if any) before reading the parser: this
	// guarantees every decoded byte has been fed to capture and the decode
	// goroutine has exited, so the reads below race with nothing.
	finishCapture()
	completedAt := time.Now()
	dur := completedAt.Sub(start)

	// A cancelled client context means the caller hung up. That also tears down
	// the upstream call, so the copy may surface as a read failure — check the
	// client first to avoid misattributing a disconnect to the upstream. The
	// idle timer cancels only the upstream child context, so it stays clear of
	// this check.
	clientGone := r.Context().Err() != nil
	// Codex closes the socket the instant it reads the terminal SSE event, which
	// cancels the client context and surfaces here as a copy error — but the full
	// response was already parsed, so it is a complete, billable request. Only a
	// disconnect *before* the terminal event is truly nothing to record.
	streamComplete := false
	if sc, ok := capture.(*streamCapture); ok {
		streamComplete = sc.terminated()
	}
	switch {
	case cerr != nil && (clientGone || !cerr.upstream) && !streamComplete:
		// Nothing worth recording: the client hung up before the response
		// finished, so there is no completed request to attribute usage to.
		p.log.Warn("proxy client disconnected",
			"req_id", reqID, "provider", provider, "status", resp.StatusCode,
			"bytes", n, "dur_ms", dur.Milliseconds(), "err", cerr)
		return
	case cerr != nil && streamComplete:
		// The client disconnected, but only after the upstream delivered a
		// terminal event — the response is complete, so record it as done and
		// let the parser below supply the full usage.
		cerr = nil
		p.log.Info("proxy done (client disconnected after completion)",
			"req_id", reqID, "provider", provider, "status", resp.StatusCode,
			"bytes", n, "model", model, "stream", stream, "dur_ms", dur.Milliseconds())
	case cerr != nil:
		// The upstream broke mid-response. Tokens were spent, so this still
		// gets a row — marked stream_interrupted, carrying whatever usage the
		// parser had accumulated by then.
		p.log.Warn("proxy upstream stream interrupted",
			"req_id", reqID, "provider", provider, "status", resp.StatusCode,
			"bytes", n, "model", model, "stream", stream,
			"dur_ms", dur.Milliseconds(), "err", cerr)
	default:
		p.log.Info("proxy done",
			"req_id", reqID, "provider", provider, "status", resp.StatusCode,
			"bytes", n, "model", model, "stream", stream, "dur_ms", dur.Milliseconds())
	}

	ev := queue.UsageEvent{
		RequestID:         reqID,
		Source:            p.source,
		Host:              p.host,
		StartedAt:         start,
		CompletedAt:       completedAt,
		Method:            r.Method,
		Path:              r.URL.Path,
		UpstreamURL:       target.String(),
		ModelRequested:    model,
		Stream:            stream,
		HTTPStatus:        resp.StatusCode,
		UpstreamRequestID: firstNonEmptyHeader(resp.Header, "X-Request-Id", "Request-Id"),
		UserAgent:         r.Header.Get("User-Agent"),
		Originator:        r.Header.Get("Originator"),
		ClientName:        clientName(r.Header),
		CodexSessionID:    r.Header.Get("session_id"),
		Directory:         firstNonEmpty(r.Header.Get("X-EF-Cwd"), directory),
		GitBranch:         firstNonEmpty(r.Header.Get("X-EF-Git-Branch"), gitBranch),
	}
	switch {
	case !parseable:
		// The body could not be decoded (unsupported Content-Encoding), so usage
		// is unavailable. Still mark an interrupted stream so the row is not
		// mistaken for a clean, fully-parsed response.
		if cerr != nil {
			ev.ErrorType, ev.ErrorMessage = errStreamInterrupted, cerr.Error()
		}
	case isSSE:
		c := capture.(*streamCapture)
		if cerr != nil {
			populateInterruptedStreamUsage(&ev, c, cerr)
		} else {
			populateStreamUsage(&ev, c)
		}
	default:
		c := capture.(*teeCapture)
		if cerr != nil {
			// The captured body is truncated by definition; parsing it would
			// report a misleading parse_error instead of the real cause.
			ev.ErrorType, ev.ErrorMessage = errStreamInterrupted, cerr.Error()
		} else {
			populateUsage(&ev, provider, resp.StatusCode, c.Bytes())
		}
	}
	if ev.ErrorType == errParseError {
		p.log.Warn("usage parse failed",
			"req_id", reqID, "provider", provider, "stream", stream,
			"content_encoding", contentEncoding, "err", ev.ErrorMessage)
	} else {
		p.log.Debug("usage parsed",
			"req_id", reqID, "provider", provider, "stream", stream,
			"model_reported", ev.ModelReported, "error_type", ev.ErrorType)
	}
	p.sink.Enqueue(ev)
}

// handleWebProxy accepts standard forward-proxy requests. HTTP requests use
// an absolute URL and are forwarded normally; HTTPS requests use CONNECT and
// are tunneled byte-for-byte. CONNECT cannot reveal the encrypted path, so it
// records the destination host, while plain HTTP records the full URL.
//
// The daemon defaults to loopback, making this suitable for a local agent
// proxy. Do not expose the listen address publicly without adding an
// authentication boundary.
func (p *Proxy) handleWebProxy(w http.ResponseWriter, r *http.Request) {
	reqID := newRequestID()
	if r.Method == http.MethodConnect {
		p.handleWebConnect(w, r)
		return
	}
	if !r.URL.IsAbs() || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" {
		p.log.Warn("unroutable request", "req_id", reqID, "method", r.Method, "path", r.URL.Path)
		http.Error(w, "forward proxy requires an absolute URL or CONNECT", http.StatusNotFound)
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "invalid web request", http.StatusBadRequest)
		return
	}
	copyHeaders(upReq.Header, r.Header)
	upReq.Host = r.URL.Host
	upReq.RequestURI = ""

	resp, err := p.webClient.Do(upReq)
	if err != nil {
		http.Error(w, "web upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Excursion-Request-Id", reqID)
	w.WriteHeader(resp.StatusCode)
	buf := getCopyBuffer()
	_, _ = io.CopyBuffer(w, resp.Body, buf)
	putCopyBuffer(buf)
}

func (p *Proxy) handleWebConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	if target == "" {
		http.Error(w, "CONNECT target is required", http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT is not supported", http.StatusNotImplemented)
		return
	}
	clientConn, clientRW, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()

	upstreamConn, err := p.dialer.DialContext(context.Background(), "tcp", target)
	if err != nil {
		_, _ = clientRW.WriteString("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		_ = clientRW.Flush()
		return
	}
	defer upstreamConn.Close()

	if _, err := clientRW.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := clientRW.Flush(); err != nil {
		return
	}

	copyDone := make(chan error, 2)
	go func() {
		// Hijack may leave already-read request bytes in clientRW.Reader (for
		// example the beginning of a TLS ClientHello), so copy that reader rather
		// than the raw connection to avoid dropping bytes.
		_, copyErr := io.Copy(upstreamConn, clientRW.Reader)
		copyDone <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(clientConn, upstreamConn)
		copyDone <- copyErr
	}()
	firstErr := <-copyDone
	_ = clientConn.Close()
	_ = upstreamConn.Close()
	secondErr := <-copyDone
	if firstErr == io.EOF {
		firstErr = nil
	}
	if secondErr == io.EOF {
		secondErr = nil
	}
	_ = firstErr
	_ = secondErr
}

// populateUsage extracts model/usage/error metadata from a captured
// non-streaming response body using the parser for provider, and fills in
// the corresponding UsageEvent fields. Parse failures are recorded as
// parse_error on the event but never returned to the caller — a malformed
// or truncated capture must not affect the already-sent proxy response.
func populateUsage(ev *queue.UsageEvent, provider string, httpStatus int, body []byte) {
	isSuccess := httpStatus >= 200 && httpStatus < 300

	switch provider {
	case "openai":
		if isSuccess {
			result, err := openai.ExtractCompleted(body)
			if err != nil {
				ev.ErrorType, ev.ErrorMessage = errParseError, err.Error()
				return
			}
			ev.ResponseID, ev.ModelReported, ev.Usage, ev.UsageJSON, ev.ToolCalls, ev.WebRequests = result.ResponseID, result.Model, result.Usage, result.UsageJSON, result.ToolCalls, result.WebRequests
			return
		}
		if errType, errMessage, ok := openai.ExtractError(body); ok {
			ev.ErrorType, ev.ErrorMessage = errType, errMessage
		}
	case "anthropic":
		if isSuccess {
			result, err := anthropic.ExtractCompleted(body)
			if err != nil {
				ev.ErrorType, ev.ErrorMessage = errParseError, err.Error()
				return
			}
			ev.ResponseID, ev.ModelReported, ev.Usage, ev.UsageJSON, ev.ToolCalls, ev.WebRequests = result.ResponseID, result.Model, result.Usage, result.UsageJSON, result.ToolCalls, result.WebRequests
			return
		}
		if errType, errMessage, ok := anthropic.ExtractError(body); ok {
			ev.ErrorType, ev.ErrorMessage = errType, errMessage
		}
	}
}

// Error types recorded on the usage row when telemetry could not be read
// normally. Neither ever affects the bytes already sent to the client.
const (
	// errParseError means the response completed but its usage could not be
	// extracted.
	errParseError = "parse_error"
	// errStreamInterrupted means the upstream response never finished.
	errStreamInterrupted = "stream_interrupted"
)

// populateStreamUsage fills event fields from a completed SSE parser. Any
// malformed or prematurely-ended stream is recorded as parse_error.
func populateStreamUsage(ev *queue.UsageEvent, capture *streamCapture) {
	switch capture.provider {
	case "openai":
		result, err := capture.openai.Result()
		if err != nil {
			ev.ToolCalls = capture.openai.PartialResult().ToolCalls
			ev.WebRequests = capture.openai.PartialResult().WebRequests
			ev.ErrorType, ev.ErrorMessage = errParseError, err.Error()
			return
		}
		ev.ResponseID = result.ResponseID
		ev.ModelReported = result.Model
		ev.Usage = result.Usage
		ev.UsageJSON = result.UsageJSON
		ev.ToolCalls = result.ToolCalls
		ev.WebRequests = result.WebRequests
		ev.ErrorType = result.ErrorType
		ev.ErrorMessage = result.ErrorMessage
	case "anthropic":
		result, err := capture.anthropic.Result()
		if err != nil {
			ev.ToolCalls = capture.anthropic.PartialResult().ToolCalls
			ev.WebRequests = capture.anthropic.PartialResult().WebRequests
			ev.ErrorType, ev.ErrorMessage = errParseError, err.Error()
			return
		}
		ev.ResponseID = result.ResponseID
		ev.ModelReported = result.Model
		ev.Usage = result.Usage
		ev.UsageJSON = result.UsageJSON
		ev.ToolCalls = result.ToolCalls
		ev.WebRequests = result.WebRequests
		ev.ErrorType = result.ErrorType
		ev.ErrorMessage = result.ErrorMessage
	}
}

// populateInterruptedStreamUsage records an upstream stream that died before
// reaching a terminal event. Whatever the parser accumulated is real spend and
// is kept; the row is marked stream_interrupted so it stays distinguishable
// from a clean response and from a response that merely failed to parse.
func populateInterruptedStreamUsage(ev *queue.UsageEvent, capture *streamCapture, cause error) {
	ev.ResponseID, ev.ModelReported, ev.Usage, ev.UsageJSON, ev.ToolCalls, ev.WebRequests = capture.partial()
	ev.ErrorType, ev.ErrorMessage = errStreamInterrupted, cause.Error()
}

type bodyCapture interface {
	Write([]byte)
}

// newCaptureSink returns the writer flushingCopy mirrors client-bound bytes
// into for telemetry, decoding the upstream Content-Encoding so the parser sees
// the same bytes the client will after it decompresses. The returned finish
// func drains and tears down any decoder and must be called before the inner
// capture is read. parseable is false when the encoding is one the stdlib
// cannot decode (brotli, zstd, …); the caller then skips usage parsing rather
// than feeding the parser bytes it would misreport as a parse_error.
func newCaptureSink(encoding string, inner bodyCapture) (sink bodyCapture, parseable bool, finish func()) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return inner, true, func() {}
	case "gzip", "x-gzip":
		d := newDecodingCapture(inner, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
		return d, true, d.finish
	default:
		return discardCapture{}, false, func() {}
	}
}

// discardCapture drops everything written to it. Used when the body cannot be
// decoded, so no bytes reach the parser.
type discardCapture struct{}

func (discardCapture) Write([]byte) {}

// decodingCapture streams compressed upstream bytes into an inner capture,
// decompressing on the fly. Bytes arrive via Write (from flushingCopy) and are
// piped through newReader's decoder into inner. It never touches the bytes the
// proxy relays to the client — this is a telemetry-only decode.
type decodingCapture struct {
	pw   *io.PipeWriter
	done chan struct{}
}

func newDecodingCapture(inner bodyCapture, newReader func(io.Reader) (io.Reader, error)) *decodingCapture {
	pr, pw := io.Pipe()
	d := &decodingCapture{pw: pw, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		r, err := newReader(pr)
		if err != nil {
			// The decoder could not start (bad header). Drain so Write never
			// blocks, then give up on this row's usage.
			_, _ = io.Copy(io.Discard, pr)
			return
		}
		buf := getCopyBuffer()
		defer putCopyBuffer(buf)
		for {
			nr, er := r.Read(buf)
			if nr > 0 {
				inner.Write(buf[:nr])
			}
			if er != nil {
				// Feed whatever decoded; discard any trailing bytes the decoder
				// left unread (e.g. a truncated gzip trailer) so Write unblocks.
				_, _ = io.Copy(io.Discard, pr)
				return
			}
		}
	}()
	return d
}

func (d *decodingCapture) Write(p []byte) {
	// Best-effort: once the decoder goroutine gives up and closes the pipe,
	// further writes error and are simply dropped.
	_, _ = d.pw.Write(p)
}

func (d *decodingCapture) finish() {
	_ = d.pw.Close()
	<-d.done
}

// teeCapture accumulates up to limit bytes for later parsing. Writes beyond
// the limit are silently discarded — the capture is a best-effort usage-
// parsing aid, never a correctness requirement, so truncation just means the
// eventual parse may fail (recorded as parse_error) rather than corrupting
// anything already sent to the client.
type teeCapture struct {
	buf   bytes.Buffer
	limit int
	full  bool
}

type streamCapture struct {
	provider  string
	openai    *openai.StreamParser
	anthropic *anthropic.StreamParser
}

func newStreamCapture(provider string, limit int) *streamCapture {
	c := &streamCapture{provider: provider}
	switch provider {
	case "openai":
		c.openai = openai.NewStreamParser(limit)
	case "anthropic":
		c.anthropic = anthropic.NewStreamParser(limit)
	}
	return c
}

func (s *streamCapture) Write(p []byte) {
	switch s.provider {
	case "openai":
		s.openai.Feed(p)
	case "anthropic":
		s.anthropic.Feed(p)
	}
}

// terminated reports whether the underlying provider parser has already seen a
// terminal SSE event. When true, a client disconnect mid-copy still represents
// a complete, billable response rather than an abandoned request.
func (s *streamCapture) terminated() bool {
	switch s.provider {
	case "openai":
		return s.openai.Terminated()
	case "anthropic":
		return s.anthropic.Terminated()
	}
	return false
}

// partial returns the metadata accumulated so far, without requiring the
// stream to have reached a terminal event.
func (s *streamCapture) partial() (responseID, model string, usage queue.Usage, usageJSON json.RawMessage, toolCalls []queue.ToolCall, webRequests []queue.WebRequest) {
	switch s.provider {
	case "openai":
		r := s.openai.PartialResult()
		return r.ResponseID, r.Model, r.Usage, r.UsageJSON, r.ToolCalls, r.WebRequests
	case "anthropic":
		r := s.anthropic.PartialResult()
		return r.ResponseID, r.Model, r.Usage, r.UsageJSON, r.ToolCalls, r.WebRequests
	}
	return "", "", queue.Usage{}, nil, nil, nil
}

func newTeeCapture(limit int) *teeCapture {
	return &teeCapture{limit: limit}
}

func (t *teeCapture) Write(p []byte) {
	if t.full {
		return
	}
	remaining := t.limit - t.buf.Len()
	if remaining <= 0 {
		t.full = true
		return
	}
	if len(p) > remaining {
		p = p[:remaining]
		t.full = true
	}
	t.buf.Write(p)
}

func (t *teeCapture) Bytes() []byte {
	return t.buf.Bytes()
}

// copyError records which side of the proxied copy failed. The distinction
// drives telemetry: a client-side failure means the caller hung up and there
// is nothing worth recording, while an upstream-side failure means the model
// call itself broke mid-response after real tokens were already spent.
type copyError struct {
	upstream bool
	err      error
}

func (e *copyError) Error() string { return e.err.Error() }
func (e *copyError) Unwrap() error { return e.err }

// flushingCopy streams src to w, flushing after every chunk so SSE frames reach
// the client immediately. A failing Flush (writer without a flusher, or client
// gone) is non-fatal; only write/read errors stop the copy. If capture is
// non-nil, every chunk successfully written to w is also mirrored into it.
func flushingCopy(w http.ResponseWriter, src io.Reader, capture bodyCapture, idleTimeout time.Duration, onChunk func()) (int64, *copyError) {
	var (
		rc    = http.NewResponseController(w)
		buf   = getCopyBuffer()
		total int64
	)
	defer putCopyBuffer(buf)
	if idleTimeout > 0 {
		if err := rc.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
			return total, &copyError{err: err}
		}
		defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	}
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := w.Write(buf[:nr])
			total += int64(nw)
			if ew != nil {
				return total, &copyError{err: ew}
			}
			if nw < nr {
				return total, &copyError{err: io.ErrShortWrite}
			}
			if capture != nil {
				capture.Write(buf[:nw])
			}
			if idleTimeout > 0 {
				if err := rc.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
					return total, &copyError{err: err}
				}
			}
			if onChunk != nil {
				onChunk()
			}
			_ = rc.Flush()
		}
		if er != nil {
			if er == io.EOF {
				return total, nil
			}
			return total, &copyError{upstream: true, err: er}
		}
	}
}

// copyHeaders copies src into dst, dropping hop-by-hop headers and any header
// named in the Connection field (RFC 7230 §6.1).
func copyHeaders(dst, src http.Header) {
	drop := map[string]bool{}
	for _, field := range src["Connection"] {
		for tok := range strings.SplitSeq(field, ",") {
			if t := strings.TrimSpace(tok); t != "" {
				drop[http.CanonicalHeaderKey(t)] = true
			}
		}
	}
	for k, vv := range src {
		if hopByHop[k] || drop[k] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// clientAcceptsGzip reports whether the client's Accept-Encoding permits a
// gzip-encoded response — an explicit "gzip" or a wildcard "*", either with a
// non-zero q-value. A missing header returns false: absence signals no explicit
// preference, and forcing gzip on a client that never asked for it risks handing
// it a body it will not decode.
func clientAcceptsGzip(h http.Header) bool {
	ae := h.Get("Accept-Encoding")
	if ae == "" {
		return false
	}
	for part := range strings.SplitSeq(ae, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		coding, params, _ := strings.Cut(token, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding != "gzip" && coding != "*" {
			continue
		}
		if qValue(params) > 0 {
			return true
		}
	}
	return false
}

// qValue extracts the q-value from Accept-Encoding parameters (the text after
// the first ";"). It defaults to 1.0 when no parsable q parameter is present.
func qValue(params string) float64 {
	for p := range strings.SplitSeq(params, ";") {
		p = strings.TrimSpace(p)
		if v, ok := strings.CutPrefix(p, "q="); ok {
			if q, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return q
			}
		}
	}
	return 1.0
}

// peekModelStream best-effort extracts `model` and `stream` for logging only.
// Both providers put these at the top level of the request body. Parse failures
// are ignored — they never affect what is forwarded.
func peekModelStream(body []byte) (model string, stream bool) {
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)
	return peek.Model, peek.Stream
}

// peekClientContext best-effort extracts the project context embedded by
// Claude Code (the top-level Messages API "system" field) and Codex (content
// blocks in the top-level Responses API "input" field). Only these known
// fields are inspected — never walked generically — so a request is not
// misattributed to text that happens to appear elsewhere in the JSON body.
// Blocks are scanned in document order and a later match overrides an
// earlier one, so the most recent occurrence wins; for Codex, whose "input"
// doubles as conversation history, pasted text matching these markers is a
// known residual source of false positives. Parse failures are ignored and
// never affect the forwarded body.
func peekClientContext(body []byte) (directory, branch string) {
	var req struct {
		System json.RawMessage `json:"system"`
		Input  []struct {
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	_ = json.Unmarshal(body, &req)
	for _, text := range clientContextBlocks(req.System) {
		directory, branch = scanClientContext(text, directory, branch)
	}
	for _, item := range req.Input {
		for _, text := range clientContextBlocks(item.Content) {
			directory, branch = scanClientContext(text, directory, branch)
		}
	}
	return directory, branch
}

// clientContextBlocks extracts the text of a Messages/Responses API content
// field, which is either a plain string or an ordered array of {"text": "..."}
// blocks. Order is preserved so callers can let later blocks take precedence.
func clientContextBlocks(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	texts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return texts
}

// scanClientContext applies the working-directory/branch markers to a single
// text block, keeping the previous value unless this block has a fresh match.
func scanClientContext(text, directory, branch string) (string, string) {
	if match := workingDirectoryRE.FindStringSubmatch(text); len(match) > 1 {
		directory = strings.TrimSpace(match[1])
	} else if match := codexCwdRE.FindStringSubmatch(text); len(match) > 1 {
		directory = strings.TrimSpace(match[1])
	}
	if match := currentBranchRE.FindStringSubmatch(text); len(match) > 1 {
		branch = strings.TrimSpace(match[1])
	}
	return directory, branch
}

// clientName returns a stable, display-ready client label from safe request
// headers. Raw details stay in user_agent/originator for inspect output.
func clientName(h http.Header) string {
	ua := strings.TrimSpace(h.Get("User-Agent"))
	originator := strings.TrimSpace(h.Get("Originator"))
	lowerUA := strings.ToLower(ua)
	lowerOriginator := strings.ToLower(originator)

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
	case ua != "":
		return ua
	default:
		return "Unknown"
	}
}

// firstNonEmptyHeader returns the value of the first header in keys that is
// present and non-empty, or "" if none match.
func firstNonEmptyHeader(h http.Header, keys ...string) string {
	for _, k := range keys {
		if v := h.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// upstreamPath normalizes the client path before it is joined to the upstream
// root. OpenAI's two Responses backends live under different prefixes — the
// platform API under /v1, the ChatGPT Codex backend under /backend-api/codex —
// so a Codex request to /v1/responses must lose its /v1 and take the prefix from
// the configured upstream root instead. Anthropic keeps its path verbatim.
func upstreamPath(provider, path string) string {
	if provider != "openai" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, "/v1/"); ok {
		return "/" + rest
	}
	if path == "/v1" {
		return "/"
	}
	return path
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

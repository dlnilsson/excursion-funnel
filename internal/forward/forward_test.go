package forward

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
	"github.com/dlnilsson/excursion-funnel/internal/queue"
	"github.com/dlnilsson/excursion-funnel/internal/store"
)

func TestForwarderDrainsOutboxToQuackHub(t *testing.T) {
	if os.Getenv("EF_TEST_QUACK") == "" {
		t.Skip("set EF_TEST_QUACK=1 to run the extension integration test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	internalAddress := listener.Addr().String()
	_ = listener.Close()

	hub, err := store.Open(filepath.Join(t.TempDir(), "hub.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	auth := hubauth.NewHubWithLogger(map[string]string{strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))): ssh.FingerprintSHA256(sshPub)}, nil)
	t.Cleanup(auth.Close)
	if _, err := hub.StartQuackAuthenticated(t.Context(), internalAddress, "internal-token", auth.ValidateSession); err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse("http://" + internalAddress)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	authHandler := auth.Handler()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/challenge") || (r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth") {
			authHandler.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox, err := store.OpenOutbox(filepath.Join(t.TempDir(), "outbox.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	event := queue.UsageEvent{
		RequestID: "forwarded-1", Source: "integration", StartedAt: time.Now(),
		Method: "POST", Path: "/v1/responses", UpstreamURL: "/",
		ToolCalls: []queue.ToolCall{{ID: "call-1", Name: "shell", Command: "echo ok"}},
	}
	if err := outbox.InsertBatch(t.Context(), []queue.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	forwarder := New(outbox, Config{
		Address: strings.TrimPrefix(gateway.URL, "http://"), KeyPath: keyPath, Insecure: true, PollInterval: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	forwarder.Start()
	t.Cleanup(forwarder.Stop)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := outbox.WaitUntilEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := hub.DB().QueryRow(`SELECT source FROM requests WHERE id = 'forwarded-1'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "integration" {
		t.Fatalf("source = %q", source)
	}
	var toolCount int
	if err := hub.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE request_id = 'forwarded-1'`).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if toolCount != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCount)
	}
}

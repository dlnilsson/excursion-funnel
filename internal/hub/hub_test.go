package hub

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/testutil"
	proxyproto "github.com/pires/go-proxyproto"
)

func TestRequireLoopbackAddr(t *testing.T) {
	if err := requireLoopbackAddr("127.0.0.1:9495"); err != nil {
		t.Fatalf("loopback rejected: %v", err)
	}
	if err := requireLoopbackAddr("0.0.0.0:9495"); err == nil {
		t.Fatal("public bind accepted")
	}
}

func TestGatewayListenerUsesProxyProtocolRemoteAddr(t *testing.T) {
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newGatewayListener(rawListener)
	remoteAddr := make(chan string, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remoteAddr <- r.RemoteAddr
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve() error = %v, want http.ErrServerClosed", err)
		}
	})

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	source := &net.TCPAddr{IP: net.ParseIP("100.64.0.42"), Port: 43210}
	header := proxyproto.HeaderProxyFromAddrs(2, source, conn.RemoteAddr())
	if _, err := header.WriteTo(conn); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: hub.test\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if got := <-remoteAddr; got != "100.64.0.42:43210" {
		t.Fatalf("RemoteAddr = %q", got)
	}
}

func TestGatewayListenerRejectsHeaderlessConnection(t *testing.T) {
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newGatewayListener(rawListener)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := fmt.Fprint(client, "GET / HTTP/1.1\r\nHost: hub.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept() error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("Accept() timed out")
	}
	defer serverConn.Close()
	if _, err := serverConn.Read(make([]byte, 1)); !errors.Is(err, proxyproto.ErrNoProxyProtocol) {
		t.Fatalf("Read() error = %v, want proxyproto.ErrNoProxyProtocol", err)
	}
}

func TestRunServersStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := runServers(ctx, "127.0.0.1:0", handler, "127.0.0.1:0", handler, time.Second, log); err != nil {
		t.Fatal(err)
	}
	testutil.AssertNoGoroutineLeaks(t, "internal/hub.runServers.func")
}

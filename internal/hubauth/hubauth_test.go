package hubauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/testutil"
	"golang.org/x/crypto/ssh"
)

func TestAuthorizedKeyLoginAndSession(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, append([]byte("from=\"127.0.0.1\" "), ssh.MarshalAuthorizedKey(sshPub)...), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHubWithLogger(allowed, nil)
	t.Cleanup(func() { testutil.AssertNoGoroutineLeaks(t, "internal/hubauth.(*Hub).reapLoop") })
	t.Cleanup(hub.Close)
	server := httptest.NewServer(hub.Handler())
	t.Cleanup(server.Close)

	challengeResponse, err := http.Get(server.URL + "/api/v1/auth/challenge")
	if err != nil {
		t.Fatal(err)
	}
	var challenge Challenge
	if err := json.NewDecoder(challengeResponse.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	challengeResponse.Body.Close()
	raw, err := base64.RawURLEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, signingPayload(challenge.ID, raw))
	body, _ := json.Marshal(loginRequest{ID: challenge.ID, PublicKey: canonicalKey(sshPub), Signature: base64.RawURLEncoding.EncodeToString(signature)})
	response, err := http.Post(server.URL+"/api/v1/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %s", response.Status)
	}
	var session loginResponse
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !hub.ValidateSession(session.Credential) {
		t.Fatal("minted session was rejected")
	}
	// A successful challenge is consumed and cannot be replayed.
	replay, err := http.Post(server.URL+"/api/v1/auth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay status = %s, want 401", replay.Status)
	}
	replay.Body.Close()
}

func TestAuthenticationRequestUsesStrictJSON(t *testing.T) {
	hub := NewHubWithLogger(map[string]string{}, nil)
	t.Cleanup(func() { testutil.AssertNoGoroutineLeaks(t, "internal/hubauth.(*Hub).reapLoop") })
	t.Cleanup(hub.Close)
	server := httptest.NewServer(hub.Handler())
	t.Cleanup(server.Close)

	oversized := fmt.Sprintf(`{"id":"id","public_key":"key","signature":"signature"}%s`,
		strings.Repeat(" ", 33<<10))
	tests := []struct {
		name   string
		body   []byte
		status int
	}{
		{name: "duplicate field", body: []byte(`{"id":"first","id":"second","public_key":"key","signature":"signature"}`), status: http.StatusBadRequest},
		{name: "invalid UTF-8", body: []byte("{\"id\":\"\xff\",\"public_key\":\"key\",\"signature\":\"signature\"}"), status: http.StatusBadRequest},
		{name: "incorrect field casing", body: []byte(`{"ID":"id","public_key":"key","signature":"signature"}`), status: http.StatusBadRequest},
		{name: "trailing value", body: []byte(`{"id":"id","public_key":"key","signature":"signature"}{}`), status: http.StatusBadRequest},
		{name: "unknown field", body: []byte(`{"id":"id","public_key":"key","signature":"signature","future":true}`), status: http.StatusUnauthorized},
		{name: "oversized body", body: []byte(oversized), status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := http.Post(server.URL+"/api/v1/auth", "application/json", bytes.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.status)
			}
		})
	}
}

func TestUnmarshalAuthResponseRejectsOversizedBody(t *testing.T) {
	var value map[string]string
	body := `{"status":"ok"}` + strings.Repeat(" ", maxAuthJSON)
	if err := unmarshalAuthResponse(strings.NewReader(body), &value); err == nil {
		t.Fatal("oversized authentication response accepted")
	}
}

func TestLoadAuthorizedKeysIgnoresInvalidAndNonEd25519(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authorized_keys")
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("# imported keys\nthis is not a public key\n")
	contents = append(contents, ssh.MarshalAuthorizedKey(rsaPub)...)
	contents = append(contents, []byte("no-port-forwarding ")...)
	contents = append(contents, ssh.MarshalAuthorizedKey(signer.PublicKey())...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	allowed, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("load mixed key file: %v", err)
	}
	wantKey := canonicalKey(signer.PublicKey())
	if len(allowed) != 1 || allowed[wantKey] != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("allowed = %v, want only %s", allowed, wantKey)
	}

	// A file without any usable key must still fail rather than start a hub
	// that no client can authenticate to.
	if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(rsaPub), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthorizedKeys(path); err == nil {
		t.Fatal("key file without Ed25519 key accepted")
	}
}

func TestClientUsesExplicitOpenSSHKeyAndCachesCredential(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHubWithLogger(map[string]string{canonicalKey(sshPub): ssh.FingerprintSHA256(sshPub)}, nil)
	t.Cleanup(func() { testutil.AssertNoGoroutineLeaks(t, "internal/hubauth.(*Hub).reapLoop") })
	t.Cleanup(hub.Close)
	server := httptest.NewServer(hub.Handler())
	t.Cleanup(server.Close)
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewClient(ClientConfig{Address: strings.TrimPrefix(server.URL, "http://"), KeyPath: path, Insecure: true})
	first, err := client.Credential(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Credential(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("credential was not cached")
	}
}

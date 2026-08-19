package hubauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	hub := NewHub(allowed)
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
	hub := NewHub(map[string]string{canonicalKey(sshPub): ssh.FingerprintSHA256(sshPub)})
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

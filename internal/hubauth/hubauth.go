// Package hubauth implements the Ed25519 login protocol used by remote hubs.
package hubauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	challengeTTL  = time.Minute
	sessionTTL    = 5 * time.Minute
	maxChallenges = 1024
	maxKeySize    = 10 << 20
)

// Challenge is the public challenge response.
type Challenge struct {
	ID        string    `json:"id"`
	Challenge string    `json:"challenge"`
	ExpiresAt time.Time `json:"expires_at"`
}

type loginRequest struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

type loginResponse struct {
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type challengeState struct {
	raw     []byte
	expires time.Time
}
type sessionState struct {
	fingerprint      string
	created, expires time.Time
}

// Hub owns startup-loaded authorization keys and ephemeral credentials.
type Hub struct {
	mu         sync.Mutex
	allowed    map[string]string // canonical public key -> fingerprint
	challenges map[string]challengeState
	sessions   map[[32]byte]sessionState
	failures   map[string][]time.Time
	log        *slog.Logger
	stop       chan struct{}
}

// LoadAuthorizedKeys loads ordinary ssh-ed25519 keys once, at hub startup.
// Malformed entries and unsupported key types are ignored.
func LoadAuthorizedKeys(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authorized keys: %w", err)
	}
	keys := make(map[string]string)
	for len(bytes.TrimSpace(data)) > 0 {
		line, rest := nextLine(data)
		data = rest
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(line)
		if err != nil {
			continue
		}
		if pub.Type() != ssh.KeyAlgoED25519 {
			continue
		}
		keys[canonicalKey(pub)] = ssh.FingerprintSHA256(pub)
	}
	if len(keys) == 0 {
		return nil, errors.New("authorized keys contains no ssh-ed25519 keys")
	}
	return keys, nil
}

func nextLine(data []byte) ([]byte, []byte) {
	if before, after, ok := bytes.Cut(data, []byte{'\n'}); ok {
		return before, after
	}
	return data, nil
}

// NewHubWithLogger creates a hub authenticator that logs login outcomes.
func NewHubWithLogger(allowed map[string]string, log *slog.Logger) *Hub {
	h := &Hub{allowed: allowed, challenges: make(map[string]challengeState), sessions: make(map[[32]byte]sessionState), failures: make(map[string][]time.Time), log: log, stop: make(chan struct{})}
	go h.reapLoop()
	return h
}
func (h *Hub) Close() { close(h.stop) }

func (h *Hub) reapLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			h.reap(time.Now())
		case <-h.stop:
			return
		}
	}
}
func (h *Hub) reap(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, c := range h.challenges {
		if !now.Before(c.expires) {
			delete(h.challenges, id)
		}
	}
	for hash, s := range h.sessions {
		if !now.Before(s.expires) {
			delete(h.sessions, hash)
		}
	}
	for ip, attempts := range h.failures {
		kept := attempts[:0]
		for _, at := range attempts {
			if now.Sub(at) < time.Minute {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(h.failures, ip)
		} else {
			h.failures[ip] = kept
		}
	}
}

// Handler exposes the challenge and login endpoints. The caller should route
// all remaining paths to the loopback Quack proxy.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/challenge", h.challenge)
	mux.HandleFunc("POST /api/v1/auth", h.authenticate)
	return mux
}

func (h *Hub) challenge(w http.ResponseWriter, _ *http.Request) {
	challenge := make([]byte, 32)
	id := make([]byte, 18)
	if _, err := rand.Read(challenge); err != nil {
		http.Error(w, "authentication unavailable", 500)
		return
	}
	if _, err := rand.Read(id); err != nil {
		http.Error(w, "authentication unavailable", 500)
		return
	}
	now := time.Now()
	expires := now.Add(challengeTTL)
	h.mu.Lock()
	for key, value := range h.challenges {
		if !now.Before(value.expires) {
			delete(h.challenges, key)
		}
	}
	if len(h.challenges) >= maxChallenges {
		h.mu.Unlock()
		http.Error(w, "too many authentication challenges", http.StatusServiceUnavailable)
		return
	}
	idS := base64.RawURLEncoding.EncodeToString(id)
	h.challenges[idS] = challengeState{raw: challenge, expires: expires}
	h.mu.Unlock()
	writeJSON(w, Challenge{ID: idS, Challenge: base64.RawURLEncoding.EncodeToString(challenge), ExpiresAt: expires})
}

func (h *Hub) authenticate(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !h.permitted(ip) {
		h.logAuthFailure(ip, "", "rate_limited")
		http.Error(w, "too many authentication failures", http.StatusTooManyRequests)
		return
	}
	defer r.Body.Close()
	var request loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&request); err != nil {
		h.failed(ip)
		h.logAuthFailure(ip, "", "invalid_request")
		http.Error(w, "invalid authentication request", 400)
		return
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
	if err != nil || pub.Type() != ssh.KeyAlgoED25519 {
		h.failed(ip)
		h.logAuthFailure(ip, "", "invalid_public_key")
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	fingerprint := ssh.FingerprintSHA256(pub)
	signature, err := base64.RawURLEncoding.DecodeString(request.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		h.failed(ip)
		h.logAuthFailure(ip, fingerprint, "invalid_signature")
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	raw, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		h.failed(ip)
		h.logAuthFailure(ip, fingerprint, "unsupported_public_key")
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	edPub, ok := raw.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		h.failed(ip)
		h.logAuthFailure(ip, fingerprint, "unsupported_public_key")
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	h.mu.Lock()
	state, exists := h.challenges[request.ID]
	authorizedFingerprint, allowed := h.allowed[canonicalKey(pub)]
	valid := exists && time.Now().Before(state.expires) && allowed && ed25519.Verify(edPub, signingPayload(request.ID, state.raw), signature)
	if valid {
		delete(h.challenges, request.ID)
	}
	h.mu.Unlock()
	if !valid {
		h.failed(ip)
		h.logAuthFailure(ip, fingerprint, authFailureReason(exists, allowed, state.expires))
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		h.logAuthFailure(ip, fingerprint, "credential_generation_failed")
		http.Error(w, "authentication unavailable", 500)
		return
	}
	expires := time.Now().Add(sessionTTL)
	hash := sha256.Sum256(credential)
	h.mu.Lock()
	h.sessions[hash] = sessionState{fingerprint: authorizedFingerprint, created: time.Now(), expires: expires}
	h.mu.Unlock()
	if h.log != nil {
		h.log.Info("hub authentication succeeded", "remote_addr", ip, "fingerprint", authorizedFingerprint, "expires_at", expires)
	}
	writeJSON(w, loginResponse{Credential: base64.RawURLEncoding.EncodeToString(credential), ExpiresAt: expires})
}

func authFailureReason(challengeExists, keyAllowed bool, challengeExpires time.Time) string {
	if !challengeExists {
		return "unknown_challenge"
	}
	if !time.Now().Before(challengeExpires) {
		return "expired_challenge"
	}
	if !keyAllowed {
		return "unauthorized_key"
	}
	return "signature_mismatch"
}

func (h *Hub) logAuthFailure(ip, fingerprint, reason string) {
	if h.log == nil {
		return
	}
	if fingerprint == "" {
		h.log.Warn("hub authentication failed", "remote_addr", ip, "reason", reason)
		return
	}
	h.log.Warn("hub authentication failed", "remote_addr", ip, "fingerprint", fingerprint, "reason", reason)
}

func (h *Hub) permitted(ip string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	a := h.failures[ip]
	kept := a[:0]
	for _, at := range a {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	h.failures[ip] = kept
	return len(kept) < 10
}
func (h *Hub) failed(ip string) {
	h.mu.Lock()
	h.failures[ip] = append(h.failures[ip], time.Now())
	h.mu.Unlock()
}

// ValidateSession is suitable for Quack's authentication callback. It never
// accepts the hub's long-lived server token.
func (h *Hub) ValidateSession(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(raw)
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[hash]
	if !ok || !time.Now().Before(s.expires) {
		if ok {
			delete(h.sessions, hash)
		}
		return false
	}
	return true
}

func signingPayload(id string, challenge []byte) []byte {
	return append(append([]byte("ef-hub-auth-v1\n"+id+"\n"), challenge...), '\n')
}
func canonicalKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// ClientConfig identifies a remote hub and optional preferred private key.
type ClientConfig struct {
	Address, KeyPath string
	Insecure         bool
}
type credential struct {
	value   string
	expires time.Time
}

// Client caches a short-lived Quack credential and refreshes it as needed.
type Client struct {
	cfg     ClientConfig
	mu      sync.Mutex
	session credential
}

var clientCache sync.Map // map[string]*Client

// NewClient returns the process-wide cache for this hub/key combination so
// short-lived reporters (including dashboard requests) do not re-login.
func NewClient(cfg ClientConfig) *Client {
	key := fmt.Sprintf("%t\x00%s\x00%s", cfg.Insecure, cfg.Address, cfg.KeyPath)
	created := &Client{cfg: cfg}
	actual, _ := clientCache.LoadOrStore(key, created)
	return actual.(*Client)
}
func (c *Client) Credential(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.value != "" && time.Until(c.session.expires) > 15*time.Second {
		return c.session.value, nil
	}
	value, expiry, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.session = credential{value, expiry}
	return value, nil
}
func (c *Client) Invalidate() { c.mu.Lock(); c.session = credential{}; c.mu.Unlock() }

func (c *Client) login(ctx context.Context) (string, time.Time, error) {
	keys, err := discoverKeys(c.cfg.KeyPath)
	if err != nil {
		return "", time.Time{}, err
	}
	base := gatewayURL(c.cfg.Address, c.cfg.Insecure)
	hc := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/auth/challenge", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	response, err := hc.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request hub challenge: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("request hub challenge: %s", response.Status)
	}
	var challenge Challenge
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&challenge); err != nil {
		return "", time.Time{}, fmt.Errorf("decode hub challenge: %w", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(challenge.Challenge)
	if err != nil || len(raw) != 32 {
		return "", time.Time{}, errors.New("hub returned invalid challenge")
	}
	attempted := make([]string, 0, len(keys))
	for _, signer := range keys {
		pub := signer.PublicKey()
		attempted = append(attempted, ssh.FingerprintSHA256(pub))
		signature, err := signer.Sign(rand.Reader, signingPayload(challenge.ID, raw))
		if err != nil {
			continue
		}
		body, _ := json.Marshal(loginRequest{ID: challenge.ID, PublicKey: canonicalKey(pub), Signature: base64.RawURLEncoding.EncodeToString(signature.Blob)})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/auth", bytes.NewReader(body))
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("authenticate to hub: %w", err)
		}
		if res.StatusCode == http.StatusOK {
			var out loginResponse
			err = json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&out)
			res.Body.Close()
			if err != nil {
				return "", time.Time{}, err
			}
			if out.Credential == "" {
				return "", time.Time{}, errors.New("hub returned an empty session credential")
			}
			return out.Credential, out.ExpiresAt, nil
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			return "", time.Time{}, fmt.Errorf("authenticate to hub: %s", res.Status)
		}
	}
	return "", time.Time{}, fmt.Errorf("no authorized Ed25519 key accepted by hub (attempted %s)", strings.Join(attempted, ", "))
}

func gatewayURL(address string, insecure bool) string {
	address = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "quack://"), "quack:")
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	return scheme + "://" + address
}

func discoverKeys(explicit string) ([]ssh.Signer, error) {
	if runtime.GOOS == "windows" && explicit == "" {
		return nil, errors.New("EF_HUB_ADDR requires --hub-key or EF_HUB_KEY on Windows")
	}
	var keys []ssh.Signer
	seen := map[string]bool{}
	add := func(s ssh.Signer) {
		if s.PublicKey().Type() != ssh.KeyAlgoED25519 {
			return
		}
		fp := ssh.FingerprintSHA256(s.PublicKey())
		if !seen[fp] {
			seen[fp] = true
			keys = append(keys, s)
		}
	}
	if explicit != "" {
		s, err := loadPrivateKey(explicit)
		if err != nil {
			return nil, fmt.Errorf("load hub key: %w", err)
		}
		add(s)
	}
	if runtime.GOOS != "windows" {
		home, err := os.UserHomeDir()
		if err == nil {
			entries, _ := os.ReadDir(filepath.Join(home, ".ssh"))
			for _, entry := range entries {
				if entry.IsDir() || strings.HasSuffix(entry.Name(), ".pub") || entry.Name() == "config" || entry.Name() == "known_hosts" || entry.Name() == "authorized_keys" {
					continue
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Size() > maxKeySize {
					continue
				}
				if s, err := loadPrivateKey(filepath.Join(home, ".ssh", entry.Name())); err == nil {
					add(s)
				}
			}
		}
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				ag := agent.NewClient(conn)
				if ss, err := ag.Signers(); err == nil {
					for _, s := range ss {
						add(s)
					}
				}
				_ = conn.Close()
			}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("no unencrypted Ed25519 private key found; set --hub-key or EF_HUB_KEY")
	}
	return keys, nil
}
func loadPrivateKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeySize {
		return nil, errors.New("private key is too large")
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("private key is not Ed25519")
	}
	return signer, nil
}

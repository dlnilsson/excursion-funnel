package safelog

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerRedactsSecretLikeAttributes(t *testing.T) {
	var output bytes.Buffer
	log := New(&output)
	log.Info("test",
		"authorization", "Bearer sk-test",
		"upstream_url", "https://example.test/v1/messages?access_token=abc",
		"err", errString("request failed with Authorization: Bearer sk-test"))

	text := output.String()
	for _, secret := range []string{"sk-test", "access_token=abc", "Authorization: Bearer"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log output leaked %q: %s", secret, text)
		}
	}
	if count := strings.Count(text, "[REDACTED]"); count < 3 {
		t.Fatalf("redaction count = %d, want at least 3: %s", count, text)
	}
}

func TestRedact(t *testing.T) {
	if got := Redact("request failed with token=secret"); got != "[REDACTED]" {
		t.Fatalf("Redact() = %q", got)
	}
	if got := Redact("ordinary error"); got != "ordinary error" {
		t.Fatalf("Redact() changed safe value to %q", got)
	}
}

type errString string

func (err errString) Error() string { return string(err) }

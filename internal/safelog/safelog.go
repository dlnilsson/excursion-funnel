// Package safelog creates application loggers that redact credential-like
// fields and values.
package safelog

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New returns a text logger that redacts secrets in structured attributes,
// string values, and errors.
func New(out io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if IsSecretKey(attr.Key) {
				attr.Value = slog.StringValue("[REDACTED]")
				return attr
			}
			if attr.Value.Kind() == slog.KindString {
				attr.Value = slog.StringValue(Redact(attr.Value.String()))
			}
			if attr.Value.Kind() == slog.KindAny && attr.Value.Any() != nil && attr.Key == "err" {
				attr.Value = slog.StringValue(Redact(fmt.Sprint(attr.Value.Any())))
			}
			return attr
		},
	}))
}

// Redact replaces a credential-like string with a safe placeholder.
func Redact(value string) string {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "x-api-key", "api_key=", "apikey=", "access_token=", "token=", "secret=", "password="} {
		if strings.Contains(lower, marker) {
			return "[REDACTED]"
		}
	}
	return value
}

// secretKeyMarkers name fields whose value is a credential. A key containing
// any of them is redacted wholesale rather than inspected.
var secretKeyMarkers = []string{"authorization", "api_key", "apikey", "x-api-key", "token", "secret", "password", "cookie"}

// IsSecretKey reports whether a field name identifies a credential. It gates
// both log-attribute redaction and the tool/web payload redaction in
// internal/queue, so the two cannot drift apart.
func IsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range secretKeyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

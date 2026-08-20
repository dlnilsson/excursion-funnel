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
			key := strings.ToLower(attr.Key)
			if secretLikeKey(key) {
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

func secretLikeKey(key string) bool {
	for _, marker := range []string{"authorization", "api_key", "apikey", "x-api-key", "token", "secret", "password", "cookie"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

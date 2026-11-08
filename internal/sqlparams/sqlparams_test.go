package sqlparams

import (
	"strings"
	"testing"
	"time"
)

func TestBind(t *testing.T) {
	tests := []struct {
		name, query string
		args        []any
		want        string
	}{
		{"no parameters", "SELECT 42", nil, "SELECT 42"},
		{"string", "SELECT ?", []any{"O'Reilly ? --"}, "SELECT 'O''Reilly ? --'"},
		{"SQL-like value", "SELECT ?", []any{"'; DROP TABLE requests; --"}, "SELECT '''; DROP TABLE requests; --'"},
		{"types", "SELECT ?, ?, ?, ?, ?", []any{10, int64(-2), true, false, nil}, "SELECT 10, (-2), true, false, NULL"},
		{"negative integer adjacency", "SELECT ?-?", []any{1, -2}, "SELECT 1-(-2)"},
		{"timestamp", "SELECT ?", []any{time.Date(2026, 11, 8, 12, 1, 2, 3000000, time.FixedZone("CET", 3600))}, "SELECT '2026-11-08T12:01:02.003+01:00'::TIMESTAMPTZ"},
		{"quoted SQL", `SELECT '?', 'it''s ?', "a""?", ?`, []any{7}, `SELECT '?', 'it''s ?', "a""?", 7`},
		{"escaped string", `SELECT E'it\'s ?', ?`, []any{7}, `SELECT E'it\'s ?', 7`},
		{"ordinary backslash", `SELECT 'a\', ?`, []any{7}, `SELECT 'a\', 7`},
		{"comments", "SELECT ? /* ? /* nested ? */ ? */ -- ?\r\n, ?", []any{1, 2}, "SELECT 1 /* ? /* nested ? */ ? */ -- ?\r\n, 2"},
		{"line comment at EOF", "SELECT ? -- ?", []any{1}, "SELECT 1 -- ?"},
		{"CR line ending", "SELECT ? -- ?\r, ?", []any{1, 2}, "SELECT 1 -- ?\r, 2"},
		{"dollar in identifier", "SELECT column$tag$, ?", []any{7}, "SELECT column$tag$, 7"},
		{"dollar quotes", `SELECT $$? ' --$$, $tag$? " /*$tag$, ?`, []any{7}, `SELECT $$? ' --$$, $tag$? " /*$tag$, 7`},
		{"question marks in values", "SELECT ?, ?", []any{"?'", "?"}, "SELECT '?''', '?'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Bind(test.query, test.args...)
			if err != nil || got != test.want {
				t.Fatalf("Bind() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestBindRejectsInvalidArgumentsAndSQL(t *testing.T) {
	tests := []struct {
		name, query string
		args        []any
	}{
		{"missing argument", "SELECT ?", nil},
		{"extra argument", "SELECT 1", []any{1}},
		{"unsupported type", "SELECT ?", []any{[]byte("private-value")}},
		{"NUL byte", "SELECT ?", []any{"private-value\x00"}},
		{"numbered parameter", "SELECT ?1", []any{1}},
		{"unclosed string", "SELECT 'private-value ?", nil},
		{"unclosed identifier", `SELECT "private-value ?`, nil},
		{"unclosed comment", "SELECT 1 /* private-value ?", nil},
		{"unclosed dollar quote", "SELECT $body$private-value ?", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Bind(test.query, test.args...)
			if err == nil || got != "" {
				t.Fatalf("Bind() = %q, %v; want no SQL and an error", got, err)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("error exposed query or argument contents")
			}
		})
	}
}

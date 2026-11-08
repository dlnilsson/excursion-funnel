// Package sqlparams renders SQL parameters as literals for DuckDB query plans
// crossing a Quack connection, where bound parameter values can be lost.
package sqlparams

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bind replaces anonymous ? parameters outside SQL strings, identifiers and
// comments. It supports the concrete argument types used by reporting queries:
// strings, timestamps, int, int64, bool and nil. Values are never rescanned as SQL.
func Bind(query string, args ...any) (string, error) {
	var (
		b          strings.Builder
		start, arg int
	)
	b.Grow(len(query) + len(args)*32)
	for i := 0; i < len(query); {
		switch query[i] {
		case '\'', '"':
			escaped := query[i] == '\'' && i > 0 && (query[i-1] == 'e' || query[i-1] == 'E') &&
				(i == 1 || !identifier(query[i-2]))
			end, err := quotedEnd(query, i, escaped)
			if err != nil {
				return "", err
			}
			i = end
			continue
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				if end := strings.IndexAny(query[i:], "\r\n"); end >= 0 {
					i += end + 1
				} else {
					i = len(query)
				}
				continue
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				end, err := commentEnd(query, i)
				if err != nil {
					return "", err
				}
				i = end
				continue
			}
		case '$':
			if i > 0 && (identifier(query[i-1]) || query[i-1] == '$') {
				i++
				continue
			}
			if delimiter := dollarDelimiter(query[i:]); delimiter != "" {
				from := i + len(delimiter)
				end := strings.Index(query[from:], delimiter)
				if end < 0 {
					return "", errors.New("unterminated SQL dollar quote")
				}
				i = from + end + len(delimiter)
				continue
			}
		case '?':
			if i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				return "", errors.New("numbered SQL parameters are unsupported")
			}
			if arg >= len(args) {
				return "", errors.New("SQL has more parameters than arguments")
			}
			b.WriteString(query[start:i])
			if err := writeLiteral(&b, args[arg]); err != nil {
				return "", err
			}
			arg++
			start = i + 1
		}
		i++
	}
	if arg != len(args) {
		return "", errors.New("SQL has more arguments than parameters")
	}
	b.WriteString(query[start:])
	return b.String(), nil
}

func writeLiteral(b *strings.Builder, value any) error {
	switch value := value.(type) {
	case nil:
		b.WriteString("NULL")
	case string:
		if strings.IndexByte(value, 0) >= 0 {
			return errors.New("SQL string argument contains a NUL byte")
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(value, "'", "''"))
		b.WriteByte('\'')
	case time.Time:
		b.WriteByte('\'')
		b.WriteString(value.Format(time.RFC3339Nano))
		b.WriteString("'::TIMESTAMPTZ")
	case int:
		return writeLiteral(b, int64(value))
	case int64:
		// Parentheses prevent a preceding minus from forming a -- comment.
		if value < 0 {
			b.WriteByte('(')
		}
		b.WriteString(strconv.FormatInt(value, 10))
		if value < 0 {
			b.WriteByte(')')
		}
	case bool:
		b.WriteString(strconv.FormatBool(value))
	default:
		return fmt.Errorf("unsupported SQL argument type %T", value)
	}
	return nil
}

func quotedEnd(query string, start int, escaped bool) (int, error) {
	for i := start + 1; i < len(query); i++ {
		if escaped && query[i] == '\\' {
			i++
			continue
		}
		if query[i] == query[start] {
			if i+1 < len(query) && query[i+1] == query[start] {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, errors.New("unterminated SQL quote")
}

func commentEnd(query string, start int) (int, error) {
	depth := 1
	for i := start + 2; i+1 < len(query); i++ {
		switch query[i : i+2] {
		case "/*":
			depth++
			i++
		case "*/":
			depth--
			if depth == 0 {
				return i + 2, nil
			}
			i++
		}
	}
	return 0, errors.New("unterminated SQL comment")
}

func dollarDelimiter(query string) string {
	if len(query) > 1 && query[1] == '$' {
		return "$$"
	}
	if len(query) < 2 || !identifier(query[1]) || query[1] >= '0' && query[1] <= '9' {
		return ""
	}
	for i := 2; i < len(query); i++ {
		if query[i] == '$' {
			return query[:i+1]
		}
		if !identifier(query[i]) {
			break
		}
	}
	return ""
}

func identifier(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

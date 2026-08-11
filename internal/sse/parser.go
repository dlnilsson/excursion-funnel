// Package sse provides a small Server-Sent Events frame splitter for proxy
// telemetry parsing. It is deliberately independent from HTTP so callers can
// feed the same chunks they have already written to the client.
package sse

import (
	"bytes"
	"errors"
)

// Event is one parsed SSE event.
type Event struct {
	Event string
	Data  []byte
}

// Parser incrementally splits SSE frames. It keeps only the current partial
// frame in memory, bounded by limit.
type Parser struct {
	buf   []byte
	limit int
	err   error
}

// NewParser creates a bounded SSE parser.
func NewParser(limit int) *Parser {
	return &Parser{limit: limit}
}

// Feed appends p, parses all complete frames, and calls handle for each one.
func (p *Parser) Feed(b []byte, handle func(Event) error) error {
	if p.err != nil {
		return p.err
	}
	if len(p.buf)+len(b) > p.limit {
		p.err = errors.New("sse frame exceeds capture limit")
		return p.err
	}
	p.buf = append(p.buf, b...)

	for {
		end, delim := frameEnd(p.buf)
		if end < 0 {
			return nil
		}
		frame := p.buf[:end]
		p.buf = p.buf[end+delim:]
		ev := parseFrame(frame)
		if ev.Event == "" && len(ev.Data) == 0 {
			continue
		}
		if err := handle(ev); err != nil {
			p.err = err
			return err
		}
	}
}

// Finalize returns an error if the stream ended with a partial frame.
func (p *Parser) Finalize() error {
	if p.err != nil {
		return p.err
	}
	if len(bytes.TrimSpace(p.buf)) != 0 {
		return errors.New("sse stream ended with partial frame")
	}
	return nil
}

func frameEnd(b []byte) (end, delim int) {
	lf := bytes.Index(b, []byte("\n\n"))
	crlf := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return -1, 0
	case lf >= 0 && (crlf < 0 || lf < crlf):
		return lf, 2
	default:
		return crlf, 4
	}
}

func parseFrame(b []byte) Event {
	var ev Event
	var data []byte
	for len(b) > 0 {
		line, rest, _ := bytes.Cut(b, []byte{'\n'})
		b = rest
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, ok := bytes.Cut(line, []byte{':'})
		if ok {
			value = bytes.TrimPrefix(value, []byte{' '})
		}
		switch {
		case bytes.Equal(field, []byte("event")):
			ev.Event = string(value)
		case bytes.Equal(field, []byte("data")):
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
	if data != nil {
		ev.Data = data
	}
	return ev
}

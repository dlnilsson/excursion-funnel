package sse

import (
	"errors"
	"strings"
	"testing"
)

// collect feeds every chunk in order and returns the events handed to the
// callback, plus the first Feed error.
func collect(t *testing.T, p *Parser, chunks ...string) ([]Event, error) {
	t.Helper()
	var got []Event
	for _, chunk := range chunks {
		if err := p.Feed([]byte(chunk), func(ev Event) error {
			got = append(got, ev)
			return nil
		}); err != nil {
			return got, err
		}
	}
	return got, nil
}

func TestParser_FramesSplitAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name:   "whole frame in one chunk",
			chunks: []string{"event: message_delta\ndata: {\"a\":1}\n\n"},
		},
		{
			// The reason an incremental parser exists: a chunk boundary lands
			// in the middle of a frame, even inside the data payload.
			name:   "split mid payload",
			chunks: []string{"event: message_delta\ndata: {\"a\"", ":1}\n\n"},
		},
		{
			name:   "split between header and data line",
			chunks: []string{"event: message_delta\n", "data: {\"a\":1}\n\n"},
		},
		{
			// Worst case: the frame delimiter itself is torn in half.
			name:   "split inside the delimiter",
			chunks: []string{"event: message_delta\ndata: {\"a\":1}\n", "\n"},
		},
		{
			name:   "one byte at a time",
			chunks: strings.Split("event: message_delta\ndata: {\"a\":1}\n\n", ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collect(t, NewParser(1024), tt.chunks...)
			if err != nil {
				t.Fatalf("Feed() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1: %+v", len(got), got)
			}
			if got[0].Event != "message_delta" {
				t.Fatalf("Event = %q, want message_delta", got[0].Event)
			}
			if string(got[0].Data) != `{"a":1}` {
				t.Fatalf("Data = %q, want {\"a\":1}", got[0].Data)
			}
		})
	}
}

func TestParser_Delimiters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "LF",
			input: "data: one\n\ndata: two\n\n",
			want:  []string{"one", "two"},
		},
		{
			name:  "CRLF",
			input: "data: one\r\n\r\ndata: two\r\n\r\n",
			want:  []string{"one", "two"},
		},
		{
			// A CRLF-delimited frame followed by an LF-delimited one must not
			// make the splitter pick the later delimiter for the first frame.
			name:  "mixed CRLF then LF",
			input: "data: one\r\n\r\ndata: two\n\n",
			want:  []string{"one", "two"},
		},
		{
			name:  "mixed LF then CRLF",
			input: "data: one\n\ndata: two\r\n\r\n",
			want:  []string{"one", "two"},
		},
		{
			name:  "leading blank frame is skipped",
			input: "\n\ndata: one\n\n",
			want:  []string{"one"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collect(t, NewParser(1024), tt.input)
			if err != nil {
				t.Fatalf("Feed() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("events = %d, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if string(got[i].Data) != want {
					t.Fatalf("event %d Data = %q, want %q", i, got[i].Data, want)
				}
			}
		})
	}
}

func TestParser_FrameFields(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEvent string
		wantData  string
	}{
		{
			name:     "multi-line data joined with newline",
			input:    "data: first\ndata: second\n\n",
			wantData: "first\nsecond",
		},
		{
			name:     "no space after colon",
			input:    "data:tight\n\n",
			wantData: "tight",
		},
		{
			name:     "only the first space is trimmed",
			input:    "data:  padded\n\n",
			wantData: " padded",
		},
		{
			name:      "comment lines are ignored",
			input:     ": keep-alive\nevent: ping\ndata: {}\n\n",
			wantEvent: "ping",
			wantData:  "{}",
		},
		{
			name:      "unknown fields are ignored",
			input:     "id: 42\nretry: 100\nevent: ping\ndata: {}\n\n",
			wantEvent: "ping",
			wantData:  "{}",
		},
		{
			name:      "event with no data",
			input:     "event: ping\n\n",
			wantEvent: "ping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collect(t, NewParser(1024), tt.input)
			if err != nil {
				t.Fatalf("Feed() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1: %+v", len(got), got)
			}
			if got[0].Event != tt.wantEvent {
				t.Fatalf("Event = %q, want %q", got[0].Event, tt.wantEvent)
			}
			if string(got[0].Data) != tt.wantData {
				t.Fatalf("Data = %q, want %q", got[0].Data, tt.wantData)
			}
		})
	}
}

// A frame consisting only of comments or blank lines carries nothing, so the
// handler must not be called for it.
func TestParser_EmptyFramesDoNotReachHandler(t *testing.T) {
	got, err := collect(t, NewParser(1024), ": comment only\n\n\n\ndata: real\n\n")
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(got), got)
	}
	if string(got[0].Data) != "real" {
		t.Fatalf("Data = %q, want real", got[0].Data)
	}
}

// The limit bounds the pending partial frame, not the whole stream: a long
// stream of small frames must keep parsing indefinitely.
func TestParser_LimitBoundsPartialFrameNotTotalStream(t *testing.T) {
	p := NewParser(64)
	var count int
	for range 100 {
		if err := p.Feed([]byte("data: x\n\n"), func(Event) error {
			count++
			return nil
		}); err != nil {
			t.Fatalf("Feed() error = %v after %d frames", err, count)
		}
	}
	if count != 100 {
		t.Fatalf("events = %d, want 100", count)
	}
}

func TestParser_LimitLatchesAfterOversizedFrame(t *testing.T) {
	p := NewParser(16)
	handle := func(Event) error { return nil }

	// No delimiter, so this all stays pending and blows the bound.
	err := p.Feed([]byte(strings.Repeat("x", 32)), handle)
	if err == nil {
		t.Fatal("Feed() error = nil, want capture-limit error")
	}

	// The failure is permanent: later well-formed frames are refused too, so
	// the caller cannot silently resume mid-stream on a desynced buffer.
	if err := p.Feed([]byte("data: x\n\n"), handle); err == nil {
		t.Fatal("Feed() after limit error = nil, want the latched error")
	}
	if err := p.Finalize(); err == nil {
		t.Fatal("Finalize() after limit error = nil, want the latched error")
	}
}

func TestParser_HandlerErrorLatches(t *testing.T) {
	p := NewParser(1024)
	sentinel := errors.New("handler boom")

	err := p.Feed([]byte("data: one\n\n"), func(Event) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("Feed() error = %v, want %v", err, sentinel)
	}
	if err := p.Feed([]byte("data: two\n\n"), func(Event) error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("Feed() after handler error = %v, want the latched %v", err, sentinel)
	}
}

func TestParser_Finalize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "clean end after a full frame",
			input: "data: one\n\n",
		},
		{
			name:  "trailing whitespace is not a partial frame",
			input: "data: one\n\n\n",
		},
		{
			name:    "trailing partial frame",
			input:   "data: one\n\ndata: tru",
			wantErr: true,
		},
		{
			name:    "no frames at all",
			input:   "data: never-terminated",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewParser(1024)
			if _, err := collect(t, p, tt.input); err != nil {
				t.Fatalf("Feed() error = %v", err)
			}
			err := p.Finalize()
			if tt.wantErr && err == nil {
				t.Fatal("Finalize() error = nil, want partial-frame error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Finalize() error = %v, want nil", err)
			}
		})
	}
}

package sse

import (
	"strings"
	"testing"
)

func BenchmarkParserFeedSingleFrame(b *testing.B) {
	frame := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":21}}\n\n")
	benchParserFeed(b, frame)
}

func BenchmarkParserFeedMultiLineData(b *testing.B) {
	frame := []byte("event: response.completed\ndata: first\ndata: second\ndata: third\n\n")
	benchParserFeed(b, frame)
}

func BenchmarkParserFeedStreamChunk(b *testing.B) {
	frame := []byte(strings.Repeat("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":21}}\n\n", 16))
	benchParserFeed(b, frame)
}

func BenchmarkParserFeedSplitBytes(b *testing.B) {
	chunks := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":21}}\n\n")
	b.ReportAllocs()
	for b.Loop() {
		p := NewParser(1024)
		for _, c := range chunks {
			if err := p.Feed([]byte{c}, func(Event) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func benchParserFeed(b *testing.B, frame []byte) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		p := NewParser(len(frame) + 16)
		if err := p.Feed(frame, func(Event) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

package availability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var transportLinesFixture = []string{
	"Transport: ECONNRESET",
	"Transport: The socket connection was closed unexpectedly",
	"provider.invalid-output: OpenAI Chat stream ended without finish_reason",
	"provider.transport: Connection lost while reading the response",
	"provider.unknown: Stream error occurred",
}

func TestMatchTransportMatchesEachTransportError(t *testing.T) {
	for _, l := range transportLinesFixture {
		got, ok := MatchTransport("earlier\n" + l)
		if !ok || got != l {
			t.Errorf("MatchTransport(%q) = %q, %v", l, got, ok)
		}
	}
}

func TestMatchTransportLeavesRateLimitAlone(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", "opencode-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if got, ok := MatchTransport(l); ok {
			t.Errorf("MatchTransport(%q) = %q, want no match", l, got)
		}
	}
	for _, l := range []string{
		"provider.rate-limit: Error 429: You have reached your weekly limit.",
		"provider.quota: This request requires more credits, or fewer max_tokens.",
	} {
		if got, ok := MatchTransport(l); ok {
			t.Errorf("MatchTransport(%q) = %q, want no match", l, got)
		}
	}
}

func TestMatchTransportLeavesOutageAlone(t *testing.T) {
	if got, ok := MatchTransport("Transport: ECONNRESET status 503"); ok {
		t.Errorf("MatchTransport = %q, want no match: an outage line is not transport", got)
	}
}

func TestMatchTransportTakesLastLine(t *testing.T) {
	text := "Transport: ECONNRESET\nprovider.unknown: Stream error occurred\nnothing here"
	got, ok := MatchTransport(text)
	if !ok || got != "provider.unknown: Stream error occurred" {
		t.Errorf("MatchTransport = %q, %v, want the last matching line", got, ok)
	}
}

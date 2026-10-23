package transcript

import (
	"reflect"
	"testing"
)

func TestTransportLinesReadsOpencodeErrorEvents(t *testing.T) {
	cases := []struct{ typ, msg, want string }{
		{"Transport", "ECONNRESET", "Transport: ECONNRESET"},
		{"Transport", "The socket connection was closed unexpectedly", "Transport: The socket connection was closed unexpectedly"},
		{"provider.invalid-output", "OpenAI Chat stream ended without finish_reason", "provider.invalid-output: OpenAI Chat stream ended without finish_reason"},
		{"provider.transport", "Connection lost while reading the response", "provider.transport: Connection lost while reading the response"},
		{"provider.unknown", "Stream error occurred", "provider.unknown: Stream error occurred"},
		{"", "bare message", "bare message"},
	}
	for _, c := range cases {
		line := `{"type":"error","error":{"type":"` + c.typ + `","message":"` + c.msg + `"}}`
		if got := TransportLines("opencode", []byte(line)); !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("TransportLines(%s) = %q, want %q", line, got, c.want)
		}
	}
}

func TestTransportLinesIgnoresModelText(t *testing.T) {
	for _, line := range []string{
		`{"type":"text","part":{"type":"text","text":"ECONNRESET happened"}}`,
		`{"type":"tool_use","part":{"type":"tool","state":{"status":"completed","output":"ECONNRESET"}}}`,
	} {
		if got := TransportLines("opencode", []byte(line)); got != nil {
			t.Errorf("TransportLines(%s) = %q, want nothing", line, got)
		}
	}
}

func TestTransportLinesIgnoresOtherKinds(t *testing.T) {
	line := `{"type":"error","message":"ECONNRESET","error":{"message":"ECONNRESET"}}`
	for _, k := range []string{"claude", "agy", "codex"} {
		if got := TransportLines(k, []byte(line)); got != nil {
			t.Errorf("TransportLines(%s) = %q, want nothing", k, got)
		}
	}
}

func TestTransportLinesReturnsNonJSONStderr(t *testing.T) {
	if got := TransportLines("opencode", []byte("read ECONNRESET")); !reflect.DeepEqual(got, []string{"read ECONNRESET"}) {
		t.Errorf("got %q", got)
	}
}

package syncworker

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// TestTransportMarksADroppedConnection pins the dropped-connection shapes the
// driver reports as text, and that a refused statement or a quota refusal never
// reads as one.
func TestTransportMarksADroppedConnection(t *testing.T) {
	t.Parallel()
	cases := map[string]RefusalCode{
		`sync engine: push: Post "https://relevo-x.turso.io/v2/pipeline": EOF`:             CodeTransport,
		`sync engine: pull: Post "https://relevo-x.turso.io/pull-updates": EOF`:            CodeTransport,
		`Post "https://relevo-x.turso.io/v2/pipeline": read tcp: connection reset by peer`: CodeTransport,
		`write tcp 10.0.0.2:443: write: broken pipe`:                                       CodeTransport,
		`sync engine: pull: unexpected EOF`:                                                CodeTransport,
		`turso: failed to execute sql: Post "https://x.turso.io/v2/pipeline": EOF`:         CodeRemote,
		`turso: failed to execute sql: connection reset by peer`:                           CodeRemote,
		`sync engine: push failed: BLOCKED: monthly write limit: broken pipe`:              CodeQuota,
		`sync engine: pull: some other fault`:                                              "",
	}
	for text, want := range cases {
		if got := RefusalCodeOf(markTransferRefusal(errors.New(text))); got != want {
			t.Errorf("%q marked %q, want %q", text, got, want)
		}
	}
}

// TestTransportIgnoresAGoLayerEOF pins that an io.EOF in the chain is not a
// dropped connection by itself: a local file read ends the same way, and the
// driver's lost connection reaches the worker only as text.
func TestTransportIgnoresAGoLayerEOF(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("read replica: %w", io.EOF)
	if got := RefusalCodeOf(markTransferRefusal(err)); got == CodeTransport {
		t.Errorf("%v marked %q", err, got)
	}
}

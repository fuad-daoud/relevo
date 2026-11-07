package syncworker

import (
	"context"
	"database/sql"
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

// bootstrapFailDriver is a replica driver whose bootstrap fails with the
// driver's own text, the way a remote that drops the connection mid-download
// does.
type bootstrapFailDriver struct {
	*localDriver
	err error
}

func (d *bootstrapFailDriver) Open(context.Context, Spec) (*sql.DB, error) { return nil, d.err }

// TestOpenMarksADroppedConnection pins that a bootstrap the remote dropped
// reads as a lost connection through the backend's own Open, while an
// unreadable replica or any other open failure stays unclassed for the
// corrupt-replica check.
func TestOpenMarksADroppedConnection(t *testing.T) {
	t.Parallel()
	cases := map[string]RefusalCode{
		`syncworker: bootstrap relevo-sync.db: turso: error: sync engine operation failed: database sync engine error: Post "https://x.turso.io/pull-updates": EOF`: CodeTransport,
		`syncworker: bootstrap relevo-sync.db: turso: error: database is corrupt: range 25445.. out of bounds`:                                                      "",
		`syncworker: bootstrap relevo-sync.db: permission denied`:                                                                                                   "",
	}
	for text, want := range cases {
		b := NewTursoBackend(&bootstrapFailDriver{localDriver: &localDriver{path: tempReplica(t)}, err: errors.New(text)})
		err := b.Open(Spec{URL: "https://x.turso.io"})
		if got := RefusalCodeOf(err); got != want {
			t.Errorf("Open over %q marked %q, want %q", text, got, want)
		}
	}
}

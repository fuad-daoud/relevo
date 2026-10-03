//go:build unix

package client_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// probesPerRun is how many back-to-back probes one stability run makes. The
// window a drain leaves behind is narrow enough that a single probe proves
// little, but the run must stay short: the count is only eventually exact, and
// a long run would report that as a failure of the property it is sampling.
const probesPerRun = 16

// afterWelcome is what a probe left on the wire once its handshake was
// answered: the frame it sent next, or the error that ended the read instead.
type afterWelcome struct {
	kind byte
	err  error
}

// greetsThenHolds answers one handshake, reads the frame that follows it, and
// reports both before holding the socket open until release is closed. Holding
// it is what makes the wait observable: nothing can end the connection while the
// owner still has it.
func greetsThenHolds(l net.Listener, drained chan<- afterWelcome, release <-chan struct{}) {
	nc, err := l.Accept()
	if err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	defer func() { _ = nc.Close() }()
	w := wire.NewConn(nc)
	frame, err := w.Read()
	if err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	var h wire.Hello
	if _, err := wire.Decode(frame, &h); err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	payload, err := wire.Encode(wire.KindWelcome, &wire.Welcome{
		Header:  wire.Header{Type: wire.TypeWelcome},
		Proto:   wire.Proto,
		Version: wire.Version,
	}, nil)
	if err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	if err := w.Write(payload); err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	frame, err = w.Read()
	if err != nil {
		drained <- afterWelcome{err: err}
		return
	}
	kind, err := wire.Kind(frame)
	drained <- afterWelcome{kind: kind, err: err}
	<-release
}

// TestInfoDrainsItsProbeBeforeReturning pins that a probe says it is done and
// waits for the owner to close the socket, so its connection is not still being
// counted by the time the next caller asks how busy the owner is. The owner
// reads the frame after the welcome and requires it to be a close; it holds the
// socket open until the test releases it, so a probe that closed without saying
// so cannot pass here by accident, and neither can one that returns before the
// owner is finished with it.
func TestInfoDrainsItsProbeBeforeReturning(t *testing.T) {
	l, sock := shortListener(t)
	drained := make(chan afterWelcome, 1)
	release := make(chan struct{})
	go greetsThenHolds(l, drained, release)

	returned := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := client.Info(ctx, sock)
		returned <- err
	}()

	got := <-drained
	if got.err != nil {
		t.Fatalf("the probe did not tell the owner it was done: %v", got.err)
	}
	if got.kind != wire.KindClose {
		t.Fatalf("frame after the welcome = kind %d, want close (%d)", got.kind, wire.KindClose)
	}
	select {
	case err := <-returned:
		t.Fatalf("Info returned %v while the owner still held the socket open", err)
	default:
	}

	close(release)
	if err := <-returned; err != nil {
		t.Fatalf("Info: %v", err)
	}
}

// TestInfoConnsCountsOnlyItsOwnConnection pins that back-to-back probes report a
// stable count: each answer names the connections open when the owner built it,
// so a probe never inherits the previous probe's connection still on its way
// out. A fire-and-forget close makes the count climb across the run instead.
func TestInfoConnsCountsOnlyItsOwnConnection(t *testing.T) {
	sock, _ := startOwner(t)

	for i := 0; i < probesPerRun; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		in, err := client.Info(ctx, sock)
		cancel()
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		if in.Conns != 1 {
			t.Fatalf("probe %d reported %d live connections, want 1: an earlier probe's connection outlived it", i, in.Conns)
		}
	}
}

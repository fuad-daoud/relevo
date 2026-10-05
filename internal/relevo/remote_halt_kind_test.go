package relevo

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// serverHaltQuotingTheMarker is a halt reason a server can send verbatim: the
// builder's own failure lines reach the client as view.Halt, and a builder whose
// output mentions the unreachable episode's marker produces this text.
const serverHaltQuotingTheMarker = "zen unreachable for 31m0s; round 1 may still be running there"

// TestServerHaltQuotingTheMarkerStaysPut pins that the unreachable episode is
// recognised by its own field and not by its text. The needs-you clear runs on
// every view that arrives, so a halt whose text quotes the marker was cleared by
// the first answering view -- which took the server's reason away, answered with
// the server's reason instead, and re-halting every poll after that.
//
// The binding is left on the server's halt, and the server's halt is told once:
// two polls, one entry, the same shape a non-quoting server halt already had.
func TestServerHaltQuotingTheMarkerStaysPut(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{
		RoundState: remote.RoundNeedsYou,
		Halt:       serverHaltQuotingTheMarker,
	}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 2; i++ {
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile poll %d: %v", i, err)
		}
		if got.State != store.StateNeedsYou {
			t.Fatalf("poll %d: State = %q, want %q", i, got.State, store.StateNeedsYou)
		}
		if got.Halt != serverHaltQuotingTheMarker {
			t.Fatalf("poll %d: Halt = %q, want the server's reason left in place", i, got.Halt)
		}
		if got.RemoteHaltKind != "" {
			t.Fatalf("poll %d: RemoteHaltKind = %q, want empty: this halt is the server's", i, got.RemoteHaltKind)
		}
	}

	if halts := haltEntriesFor(t, rt, b.Name); len(halts) != 1 {
		t.Errorf("halt entries = %d, want exactly 1 across two polls of the same halt", len(halts))
	}
}

// TestUnreachableHaltStillCarriesItsKind pins the write half: the halt this file
// opens is the one that records the kind, so the clear path can tell it from a
// halt the server sent.
//
// Mutation target: drop the stamp in applyRemoteUnreachable and this reads empty,
// and with the predicate on the field the unreachable episode stops clearing at
// all -- the binding stays NEEDS YOU on a round a human can watch move.
func TestUnreachableHaltStillCarriesItsKind(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := unreachableHaltedBinding(t, rt, b)
	if got.RemoteHaltKind != store.HaltKindUnreachable {
		t.Fatalf("RemoteHaltKind = %q, want %q on the halt this file writes", got.RemoteHaltKind, store.HaltKindUnreachable)
	}
}

// TestClearedUnreachableHaltDropsItsKind pins the kind is cleared with the text.
// Left behind, the field would answer unreachableHalted for a binding with no
// halt at all, and a server that drops out again would clear nothing on the
// tick that ought to reopen the episode.
func TestClearedUnreachableHaltDropsItsKind(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got := unreachableHaltedBinding(t, rt, b)

	fr.getBindingErr = nil
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	fr.roundFileFromResp = io.NopCloser(strings.NewReader("builder log line 1\n"))

	got, err := reconcile(t, at(rt, 31*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.RemoteHaltKind != "" {
		t.Errorf("RemoteHaltKind = %q, want empty once the halt is cleared", got.RemoteHaltKind)
	}
}

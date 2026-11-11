package verb

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/store/storetest"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// tickTestKV is the machine-local marker surface NoteRoundsOpen reads: only
// the enabled mark matters, and only Enabled=true is ever answered.
type tickTestKV struct{ enabled bool }

func (k tickTestKV) KVGet(key string) ([]byte, bool, error) {
	if k.enabled && key == relevosync.KeyEnabled {
		return []byte("true"), true, nil
	}
	return nil, false, nil
}

func (k tickTestKV) KVPut(key string, value []byte) error { return nil }

func (k tickTestKV) KVDelete(key string) error { return nil }

var _ db.KV = tickTestKV{}

// seedTickBinding saves one binding with round and state and appends its
// prompt, closing it with a report when closed is set.
func seedTickBinding(t *testing.T, st *store.Store, cwd, name string, round int, state store.State, closed bool) {
	t.Helper()
	if err := st.Save(store.Binding{Name: name, CWD: cwd, Round: round, State: state}); err != nil {
		t.Fatalf("Save(%s): %v", name, err)
	}
	prompt := store.LogEntry{Round: round, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true}
	if err := st.AppendLog(name, prompt); err != nil {
		t.Fatalf("AppendLog prompt(%s): %v", name, err)
	}
	if closed {
		report := store.LogEntry{Round: round, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true}
		if err := st.AppendLog(name, report); err != nil {
			t.Fatalf("AppendLog report(%s): %v", name, err)
		}
	}
}

// TestNoteRoundsOpenSkipsDoneBindingsAndKeepsPaused pins the idle count's one
// saving: a done binding's log is never read, while a paused binding keeps
// its mid-round open count like any live one. A live binding whose round
// closed is read but not counted, proving the count still consults the log.
func TestNoteRoundsOpenSkipsDoneBindingsAndKeepsPaused(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	seedTickBinding(t, st, t.TempDir(), "done-closed", 1, store.StateDone, true)
	seedTickBinding(t, st, t.TempDir(), "done-open", 1, store.StateDone, false)
	seedTickBinding(t, st, t.TempDir(), "paused-open", 1, store.StatePaused, false)
	seedTickBinding(t, st, t.TempDir(), "active-open", 2, store.StateActive, false)
	seedTickBinding(t, st, t.TempDir(), "active-closed", 1, store.StateActive, true)

	bindings, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	reads := map[string]int{}
	st.SetReadLogHook(func(name string) { reads[name]++ })

	runner := &relevosync.Runner{Local: tickTestKV{enabled: true}}
	NoteRoundsOpen(runner, st, bindings)

	got, known := runner.RoundsOpen()
	if !known || got != 2 {
		t.Errorf("open rounds = %d, known = %v, want 2, true", got, known)
	}
	for name, want := range map[string]int{
		"done-closed": 0, "done-open": 0, "paused-open": 1, "active-open": 1, "active-closed": 1,
	} {
		if reads[name] != want {
			t.Errorf("log reads for %s = %d, want %d", name, reads[name], want)
		}
	}
	t.Logf("log reads during NoteRoundsOpen: %v", reads)
}

// TestARemoteRoundPromptedOnItsServerIsInFlight pins the client side of the
// idle count: a chain started on a server sends its prompts there, so the
// member's log here holds no prompt for a round that is running. A remote
// binding's unreported current round counts; a reported one and a binding
// with no round yet do not.
func TestARemoteRoundPromptedOnItsServerIsInFlight(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	remote := store.Endpoint{Mode: store.ModeRemote}
	for _, b := range []store.Binding{
		{Name: "served-running", CWD: t.TempDir(), Round: 1, State: store.StateActive, Builder: remote},
		{Name: "served-reported", CWD: t.TempDir(), Round: 1, State: store.StateActive, Builder: remote},
		{Name: "served-unsent", CWD: t.TempDir(), Round: 0, State: store.StateActive, Builder: remote},
	} {
		if err := st.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", b.Name, err)
		}
	}
	switched := store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true}
	if err := st.AppendLog("served-running", switched); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	report := store.LogEntry{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true}
	if err := st.AppendLog("served-reported", report); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	bindings, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := CountOpenRounds(st, bindings); got != 1 {
		t.Errorf("open rounds = %d, want 1 (only served-running)", got)
	}
}

// TestServedRoundsCountOnTheServer pins the server side: the daemon's own
// store lists no client's binding, so an open round served to a client must
// still count, including on a daemon with no binding of its own.
func TestServedRoundsCountOnTheServer(t *testing.T) {
	t.Parallel()

	st := storetest.New(t)
	d, err := st.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	clientA := store.NewShared(st.Root(), "client-a", d)
	clientB := store.NewShared(st.Root(), "client-b", d)
	seedTickBinding(t, clientA, t.TempDir(), "a-open", 1, store.StateActive, false)
	seedTickBinding(t, clientA, t.TempDir(), "a-closed", 1, store.StateActive, true)
	seedTickBinding(t, clientB, t.TempDir(), "b-open", 2, store.StateActive, false)

	runner := &relevosync.Runner{Local: tickTestKV{enabled: true}}
	NoteRoundsOpen(runner, st, nil)

	got, known := runner.RoundsOpen()
	if !known || got != 2 {
		t.Errorf("open rounds = %d, known = %v, want 2 (a-open and b-open)", got, known)
	}
}

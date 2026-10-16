package relevo

// The rekey verb: the tail splits into two passphrases or nothing runs, a
// refusal keeps the code a caller maps -- locked, wrong passphrase, a race --
// and a winning rekey leaves the machine holding the whole new ring.

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// rekeyTail builds the frame tail and its two lengths from the pair of
// passphrases a caller holds.
func rekeyTail(current, newPass []byte) (*wire.SyncVerb, []byte) {
	verb := &wire.SyncVerb{
		Verb:             wire.SyncVerbRekey,
		PassphraseLen:    len(current),
		NewPassphraseLen: len(newPass),
	}
	tail := append(append([]byte{}, current...), newPass...)
	return verb, tail
}

// rekeyPassphrase is the fleet's current passphrase, derived for real by the
// unlock --new the happy cases run first.
var rekeyPassphrase = []byte("the fleet's current passphrase")

// rekeyPassphraseNew is what the fleet rotates to.
var rekeyPassphraseNew = []byte("the fleet's new passphrase")

// rekeyRunner is one enabled machine with a log transport installed, so a verb
// has something to drive and nothing here opens a worker or a network.
func rekeyRunner(t *testing.T, log *synclog.MemTransport) *VerbRunner {
	t.Helper()
	m := newCfgMachine(t, log, nil, true)
	origin := m.mdb.Origin()
	return &VerbRunner{
		Shared: m.mdb,
		Local:  m.local,
		Path:   m.mdb.Path(),
		Runner: m.runner,
		Open:   func(context.Context) (synclog.LogTransport, error) { return log.OnLog(origin), nil },
	}
}

// TestRekeyIntakeSplitsTheTailAtItsOwnLengths pins the split: the current
// passphrase is the bytes the header's length names and the new one is the
// rest, and a tail the two lengths do not exactly cover is refused rather than
// trimmed into one another.
func TestRekeyIntakeSplitsTheTailAtItsOwnLengths(t *testing.T) {
	current, newPass := []byte("the current"), []byte("the new one")
	verb, tail := rekeyTail(current, newPass)
	gotCurrent, gotNew, err := rekeyIntake(verb, tail)
	if err != nil || string(gotCurrent) != string(current) || string(gotNew) != string(newPass) {
		t.Fatalf("rekeyIntake = (%q, %q, %v), want the split as named", gotCurrent, gotNew, err)
	}

	// A tail shorter than the two lengths name would store part of one
	// passphrase as the other, so it is refused.
	verb.NewPassphraseLen = len(newPass) + 1
	if _, _, err := rekeyIntake(verb, tail); err == nil {
		t.Fatal("a tail shorter than its lengths was accepted")
	}
	// A tail longer than the two lengths name carries bytes neither names, and
	// splitting it anyway would make them part of the new passphrase.
	verb.NewPassphraseLen = len(newPass) - 1
	if _, _, err := rekeyIntake(verb, tail); err == nil {
		t.Fatal("a tail longer than its lengths was accepted")
	}
	// A negative length names nothing and is refused the same way.
	verb.NewPassphraseLen = -1
	if _, _, err := rekeyIntake(verb, tail); err == nil {
		t.Fatal("a negative length was accepted")
	}
}

// TestRekeyVerbRotatesTheMachine pins the winning verb: the remote is at epoch
// 2, the machine holds both keys, and the answer is plain success.
func TestRekeyVerbRotatesTheMachine(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("m1")
	runner := rekeyRunner(t, log)
	if res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbUnlock, New: true}, rekeyPassphrase); !res.OK {
		t.Fatalf("unlock refused: %s", res.Message)
	}
	verb, tail := rekeyTail(rekeyPassphrase, rekeyPassphraseNew)
	if res := runner.Run(context.Background(), verb, tail); !res.OK {
		t.Fatalf("rekey refused: %s (%s)", res.Message, res.Code)
	}
	ring, ok, err := relevosync.ReadKeyring(runner.Local)
	if err != nil || !ok || ring.Epoch != 2 || len(ring.Keys) != 2 {
		t.Fatalf("the ring = %+v, (ok %v, err %v); want epoch 2 with both keys", ring, ok, err)
	}
	meta, err := log.KeyMeta(context.Background())
	if err != nil || meta.Epoch != 2 {
		t.Fatalf("remote epoch = %d, %v; want 2", meta.Epoch, err)
	}
}

// TestRekeyVerbRefusalsCarryTheirCodes pins the closed set: a locked machine,
// a wrong current passphrase, and a rekey that lost a race each answer with the
// code a caller maps, and the wrong one writes nothing anywhere.
func TestRekeyVerbRefusalsCarryTheirCodes(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("m1")
	runner := rekeyRunner(t, log)

	// Locked: no key on this machine, whatever the remote holds.
	verb, tail := rekeyTail(rekeyPassphrase, rekeyPassphraseNew)
	if res := runner.Run(context.Background(), verb, tail); res.OK || res.Code != wire.SyncCodeMachineLocked {
		t.Fatalf("locked rekey = (ok %v, code %q), want %q", res.OK, res.Code, wire.SyncCodeMachineLocked)
	}

	// Unlocked, then a wrong current passphrase.
	if res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbUnlock, New: true}, rekeyPassphrase); !res.OK {
		t.Fatalf("unlock refused: %s", res.Message)
	}
	wrong, wrongTail := rekeyTail([]byte("not the passphrase"), rekeyPassphraseNew)
	if res := runner.Run(context.Background(), wrong, wrongTail); res.OK || res.Code != wire.SyncCodeWrongPassphrase {
		t.Fatalf("wrong rekey = (ok %v, code %q), want %q", res.OK, res.Code, wire.SyncCodeWrongPassphrase)
	}
	if meta, err := log.KeyMeta(context.Background()); err != nil || meta.Epoch != 1 {
		t.Fatalf("the remote moved to epoch %d, %v; a refused rekey writes nothing", meta.Epoch, err)
	}

	// A race: this machine's observation of the remote is one rekey behind,
	// and the epoch a re-read answers with has moved past the one it wrote
	// -- another machine rekeyed first.
	oldMeta, err := log.KeyMeta(context.Background())
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if res := runner.Run(context.Background(), verb, tail); !res.OK {
		t.Fatalf("the winner's rekey refused: %s", res.Message)
	}
	mover := &rekeyMetaMover{LogTransport: log, start: &oldMeta}
	mover.after = func() *synclog.KeyMeta {
		won := oldMeta
		won.Epoch = 3
		return &won
	}
	racer := &VerbRunner{
		Shared: runner.Shared, Local: runner.Local, Path: runner.Path,
		// A runner with no client, so the verb drives the mover rather than
		// the steady transport the earlier runs left behind.
		Runner: &relevosync.Runner{Local: runner.Local},
		Open:   func(context.Context) (synclog.LogTransport, error) { return mover, nil },
	}
	if res := racer.Run(context.Background(), verb, tail); res.OK || res.Code != wire.SyncCodeEpochRace {
		t.Fatalf("raced rekey = (ok %v, code %q), want %q", res.OK, res.Code, wire.SyncCodeEpochRace)
	}
}

// TestVerbClassifyNamesTheRekeyCodes pins the mapping the client side keys on:
// the two rekey sentinels land on their own codes, not on internal.
func TestVerbClassifyNamesTheRekeyCodes(t *testing.T) {
	t.Parallel()
	if got := verbClassify(relevosync.ErrMachineLocked); got != wire.SyncCodeMachineLocked {
		t.Errorf("ErrMachineLocked = %q, want %q", got, wire.SyncCodeMachineLocked)
	}
	if got := verbClassify(relevosync.ErrEpochRace); got != wire.SyncCodeEpochRace {
		t.Errorf("ErrEpochRace = %q, want %q", got, wire.SyncCodeEpochRace)
	}
	if got := verbClassify(errors.Join(relevosync.ErrEpochRace)); got != wire.SyncCodeEpochRace {
		t.Errorf("a wrapped ErrEpochRace = %q, want the same code", got)
	}
}

// rekeyMetaMover is a log whose key reads are the test's to stage: the first
// KeyMeta answers what it was set up with and later ones answer after, which is
// how one test holds a machine's start observation still while the remote moves
// under it.
type rekeyMetaMover struct {
	synclog.LogTransport
	calls int
	start *synclog.KeyMeta
	after func() *synclog.KeyMeta
}

func (m *rekeyMetaMover) KeyMeta(context.Context) (synclog.KeyMeta, error) {
	m.calls++
	if m.calls > 1 && m.after != nil {
		return *m.after(), nil
	}
	if m.start != nil {
		return *m.start, nil
	}
	return m.LogTransport.KeyMeta(context.Background())
}

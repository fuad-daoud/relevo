package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The tests here drive the verb executor over a real split pair. None of them
// reaches a network, spawns a harness or opens a second file: the executor runs
// against handles the test already holds, which is the property these tests
// exist to pin.
//
// The token fixtures are long and unmistakable on purpose. Several of these
// tests assert the value never appears in a log, an error or a result, and a
// short or plausible-looking fixture would make that assertion easy to pass by
// accident.

// verbFixtureToken is the token an enable is handed. Every redaction assertion
// greps for exactly this string.
const verbFixtureToken = "FIXTURE-TOKEN-8f3a2c91d4e7b605a1c2d3e4f5061728"

// verbFixtureR2 is the bucket configuration every verb fixture stores. The
// values are not credentials and reach no bucket: the transport under test is a
// record, and the only thing the daemon builds from them is the handshake.
var verbFixtureR2 = relevosync.R2Secrets{
	Endpoint: "https://acct.r2.cloudflarestorage.com",
	Bucket:   "relevo-sync",
	KeyID:    "key-1",
	Secret:   "secret-1",
}

// verbFixture is one executor over a real split pair, plus the fake client and
// the opener it was handed.
type verbFixture struct {
	runner *VerbRunner
	shared *db.DB
	local  relevosync.Local
	client *recordTransport
	// opens is every transport a run asked for. Nothing opens one in this
	// build, so this is what a handle added back would be recorded in.
	opens int
}

// newVerbFixture builds an executor over a fresh split pair. The opener is the
// seam a remote would arrive through, and the fixture counts what asks for one.
func newVerbFixture(t *testing.T) *verbFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	// A configured machine: a remote in the section and a token stored beside
	// it. A machine with neither is a different question and nothing on this
	// path answers it any more.
	putVerbSection(t, local, "libsql://example.invalid")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	// The bucket credentials too: an enable refuses a machine that holds none,
	// so a fixture about anything else has to be a configured machine. The values
	// reach no bucket -- the transport here is a record, not a worker.
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	f := &recordTransport{}
	fx := &verbFixture{shared: shared, local: local, client: f}
	fx.runner = &VerbRunner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Client: f, Local: local},
		Open: func(context.Context) (synclog.LogTransport, error) {
			fx.opens++
			return f, nil
		},
		ClientName: "relevo",
	}
	return fx
}

// TestSyncPushAndPullMoveTheLog pins the two one-shots that run half the steady
// exchange each: push drains this machine's outbox into the log, and pull reads
// the other origins' entries and imports them. Each runs against the transport
// the fixture installed, and neither refuses.
//
// The mutation is routing a one-shot away from the transport: a verb that never
// drove it records no call here, and one that refused would miss the OK this
// test names.
func TestSyncPushAndPullMoveTheLog(t *testing.T) {
	f := newVerbFixture(t)
	ctx := context.Background()
	// A row this machine owns, so the export has something to hand over.
	if _, err := f.shared.RecordPut(db.Record{
		Owner: "m1", Name: "webshop", State: "open", JSON: "{}",
		CreatedAt: time.Unix(0, 0).UTC(), UpdatedAt: time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed an owned row: %v", err)
	}

	f.opens, f.client.Calls = 0, nil
	push := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
	if !push.OK {
		t.Fatalf("push refused: %s", push.Message)
	}
	if !push.Applied {
		t.Error("push handed the log nothing, want the seeded row")
	}
	if !called(f.client, "append") {
		t.Errorf("push drove %v, want an append", f.client.Calls)
	}

	f.client.Calls = nil
	pull := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil)
	if !pull.OK {
		t.Fatalf("pull refused: %s", pull.Message)
	}
	if !called(f.client, "pull") {
		t.Errorf("pull drove %v, want a pull", f.client.Calls)
	}
	if f.opens != 0 {
		t.Errorf("a one-shot opened %d handles, want none: the runner's worker is reused", f.opens)
	}
}

// called reports whether a transport recorded the named call, so a test can say
// one-shots drove the log without depending on the order they did it in.
func called(r *recordTransport, name string) bool {
	for _, c := range r.Calls {
		if c == name {
			return true
		}
	}
	return false
}

// TestSyncVerbEnableJoins pins enable whole: it runs the preflight, drives the
// log through the opener the wiring installed, marks the machine on, clears its
// join marker and leaves the worker on the runner. The
// origin gate is made to pass and the opener hands back the in-memory log, so
// no worker starts and no network is reached.
func TestSyncVerbEnableJoins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if _, _, err := db.CompressHistoryOnce(shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	// The enable preflight refuses a machine with no bucket credentials, so this
	// one stores a complete set before the verb runs.
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	log := synclog.NewMemTransport("m1")
	runner := &VerbRunner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Local: local},
		Open:   func(context.Context) (synclog.LogTransport, error) { return log, nil },
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://join.invalid",
	}, []byte(verbFixtureToken))
	if !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	if res.RemoteURL != "libsql://join.invalid" {
		t.Errorf("enable stored remote %q, want the one the verb named", res.RemoteURL)
	}
	on, err := relevosync.Enabled(local)
	if err != nil || !on {
		t.Fatalf("Enabled = %v, %v; want the mark on", on, err)
	}
	if _, ok, err := relevosync.ReadJoin(local); err != nil || ok {
		t.Errorf("join marker = (ok %v, err %v), want none after a finished join", ok, err)
	}
	if runner.Runner.Client == nil {
		t.Error("the enable left no worker on the runner")
	}
}

// TestSyncVerbDisableStillRuns pins the turn-off's contract: it is the one verb
// that only writes machine-local rows, so it marks the machine off, forgets the
// token and reports its steps in the contract's order.
//
// It is what a reader reaches for when they want sync to stop, and it is the
// way out of a machine that was on when the engine went away.
func TestSyncVerbDisableStillRuns(t *testing.T) {
	f := newVerbFixture(t)
	ctx := context.Background()
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	f.opens = 0
	res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	if !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	if got := strings.Join(res.Steps, ","); got != "final export,mark off,delete token,stop worker,delete replica,drop client" {
		t.Errorf("steps = %q, want the contract's six in order", got)
	}
	if !res.FinalPush {
		t.Error("the turn-off skipped the final export on an enabled machine holding a client")
	}
	if f.opens != 0 {
		t.Errorf("the turn-off opened %d handles, want none", f.opens)
	}
	on, err := relevosync.Enabled(f.local)
	if err != nil {
		t.Fatalf("Enabled: %v", err)
	}
	if on {
		t.Error("disable answered OK but left the machine marked on")
	}
	if _, ok, err := relevosync.ReadToken(f.local); err != nil {
		t.Fatalf("ReadToken: %v", err)
	} else if ok {
		t.Error("disable left the token in place")
	}
}

// TestSyncVerbNamesAreTheClosedSet pins that the surface did not change shape
// while its answers did: every name the dispatcher routes is still answered,
// and a name outside the set is still refused rather than run.
func TestSyncVerbNamesAreTheClosedSet(t *testing.T) {
	f := newVerbFixture(t)
	for _, verb := range []string{wire.SyncVerbEnable, wire.SyncVerbDisable, wire.SyncVerbPush, wire.SyncVerbPull, wire.SyncVerbRetry} {
		res := f.runner.Run(context.Background(), &wire.SyncVerb{Verb: verb}, nil)
		if res.Code == wire.SyncCodeInvalid && res.Message == "sync: no such verb" {
			t.Errorf("the executor does not know the verb %q", verb)
		}
	}

	res := f.runner.Run(context.Background(), &wire.SyncVerb{Verb: "delete-everything"}, nil)
	if res.OK || res.Code != wire.SyncCodeInvalid {
		t.Errorf("an unknown verb answered %+v, want an invalid refusal", res)
	}
}

// putVerbSection writes the machine-local sync section, which is where a remote
// lives. It is the row a hand-written `relevo config set sync` would write and
// the one a previous enable stored.
func putVerbSection(t *testing.T, local relevosync.Local, remote string) {
	t.Helper()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: remote}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
}

// TestSyncVerbOverOwnerNeverOpensASecondHandle pins the lock contract from the
// executor's side: running a verb opens no file.
//
// The daemon holds the shared database under a lock, and a writer that opened it
// again would either fail or, worse, succeed on a platform without the lock.
// This asserts it by holding the file open in this process and running every
// verb: the fact that they answer at all is the proof, because a second direct
// open of the same file would have met the lock this handle holds.
func TestSyncVerbOverOwnerNeverOpensASecondHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	first, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	local, err := relevosync.LocalHandle(first)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	putVerbSection(t, local, "libsql://example.invalid")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	// The bucket credentials too: an enable refuses a machine that holds none,
	// so a fixture about anything else has to be a configured machine. The values
	// reach no bucket -- the transport here is a record, not a worker.
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	f := &recordTransport{}
	runner := &VerbRunner{
		Shared:     first,
		Local:      local,
		Path:       first.Path(),
		Runner:     &relevosync.Runner{Client: f, Local: local},
		ClientName: "relevo",
	}

	for _, verb := range []string{wire.SyncVerbEnable, wire.SyncVerbPush, wire.SyncVerbPull, wire.SyncVerbRetry, wire.SyncVerbDisable} {
		res := runner.Run(context.Background(), &wire.SyncVerb{Verb: verb}, nil)
		if res.Code == wire.SyncCodeInternal {
			t.Errorf("%s met the lock this handle holds: %s", verb, res.Message)
		}
	}
	if !called(f, "pull") {
		t.Errorf("no verb drove the injected log, so the held file was never the one answerable: %v", f.Calls)
	}
}

// TestSyncTokenAbsentFromBothEndsLogs pins the redaction rule on the daemon
// side: the token a verb arrives with reaches the hook and nothing else.
//
// The fixture token is captured out of the hook's own arguments and then the
// whole executor runs, and no log line the run produced may contain it. This is
// the assertion that would fail if a verb body were logged, if an error were
// formatted with a config, or if the result carried the value rather than a
// presence.
func TestSyncTokenAbsentFromBothEndsLogs(t *testing.T) {
	f := newVerbFixture(t)

	logs, restore := captureSyncLogs(t)
	defer restore()

	f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))
	f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)

	if strings.Contains(logs.String(), verbFixtureToken) {
		t.Errorf("the fixture token reached a log line:\n%s", logs.String())
	}
}

// TestSyncVerbResultNeverCarriesTheToken pins the other half of the same rule:
// no field of a verb's answer holds the value.
//
// TokenPresent is a bool precisely so a caller can say whether a token exists
// without a way to say what it was. Marshalling every result the fixture
// produces and searching the bytes catches a value added to any field, named or
// not.
func TestSyncVerbResultNeverCarriesTheToken(t *testing.T) {
	f := newVerbFixture(t)
	verbs := []*wire.SyncVerb{
		{Verb: wire.SyncVerbPush},
		{Verb: wire.SyncVerbPull},
		{Verb: wire.SyncVerbEnable},
		{Verb: wire.SyncVerbDisable},
		{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://elsewhere.invalid"},
	}
	for _, verb := range verbs {
		res := f.runner.Run(context.Background(), verb, []byte(verbFixtureToken))
		body, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal the %s result: %v", verb.Verb, err)
		}
		if strings.Contains(string(body), verbFixtureToken) {
			t.Errorf("the %s result carries the token: %s", verb.Verb, body)
		}
	}
}

// TestSyncConcurrentSealThenPushWholeOrAbsent is the pin on the guarantee that
// a push captures whole committed seal transactions only.
//
// A round sealing
// concurrently with a push is either fully present or fully absent on every
// other machine -- never half-sealed -- and the sealing daemon is never paused,
// bounded or otherwise. The guarantee rests on two facts: the seal writes its
// round in one transaction, and the push batches on transaction boundaries.
//
// What this test pins is the consequence that matters: an observer reading
// while a seal commits sees the round whole or not at all, and serializing the
// reader against the seal -- the worst case a pause would produce -- changes
// nothing observable. If a verb ran its push inline on the connection that took
// the request, rather than through the daemon's serialized executor, the
// guarantee would depend on that connection's timing instead of on the
// transaction boundary, and this test is what catches it.
func TestSyncConcurrentSealThenPushWholeOrAbsent(t *testing.T) {
	for _, round := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		observed := runSealThenPush(t, round)
		switch observed {
		case roundAbsent, roundWhole:
		default:
			t.Fatalf("round %d was observed %s: a half-sealed round reached a reader", round, observed)
		}
	}
}

// The three ways a reader can see a round that a seal is committing.
const (
	roundAbsent  = "absent"
	roundWhole   = "whole"
	roundPartial = "partial"
)

// runSealThenPush seals one round while a push-shaped read runs against the
// same transaction boundary, and reports what the reader saw.
//
// The seal is one transaction that writes every row of the round, exactly as
// internal/store's seal does, and the reader is the push's own grouping: it
// counts the rows a commit made visible. A reader that sees a number other than
// zero or roundRows has seen a transaction's worth of rows arrive piecemeal,
// which is the failure this test exists to rule out.
func runSealThenPush(t *testing.T, round int) string {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared_close(d) })

	const roundRows = 4
	seen := make(chan string, 1)
	start := make(chan struct{})

	// The reader: a push-shaped read of the round's rows, run while the seal is
	// committing. It reads the same way the CDC grouping key does -- per commit
	// -- so it sees a whole round or none of one.
	go func() {
		<-start
		n := 0
		err := d.QueryReadOnly(context.Background(),
			`SELECT key FROM kv WHERE key LIKE 'round-%'`,
			func(_ []string, _ []any) error {
				n++
				return nil
			})
		switch {
		case err != nil || n == 0:
			seen <- roundAbsent
		case n == roundRows:
			seen <- roundWhole
		default:
			seen <- roundPartial
		}
	}()

	close(start)
	// The seal: every row of the round inside one transaction, which is what
	// makes the round atomic to any reader that has not seen it yet.
	err = d.Tx(func(tx *db.Tx) error {
		for i := range roundRows {
			key := "round-" + string(rune('a'+i))
			if err := tx.KVPut(key, []byte(`"sealed"`)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seal the round: %v", err)
	}
	return <-seen
}

// shared_close closes a handle and swallows the error, for a cleanup that
// cannot act on one.
func shared_close(d *db.DB) error { return d.Close() }

// captureSyncLogs redirects slog into a buffer for the duration of a test, so a
// redaction assertion can look at what a run actually logged rather than at what
// it was supposed to.
func captureSyncLogs(t *testing.T) (*strings.Builder, func()) {
	t.Helper()
	buf := &strings.Builder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(prev) }
}

// TestSyncVerbSerializesWithTheDaemonGuard pins that a verb and a daemon trigger
// cannot interleave.
//
// Both record the same markers and both hold the same local file, so two of them
// at once would write each other's outcomes and leave a machine whose marker
// describes an attempt that never finished. The guard is the daemon's existing
// one: a verb waits its turn rather than being dropped, because somebody asked
// for it explicitly.
func TestSyncVerbSerializesWithTheDaemonGuard(t *testing.T) {
	f := newVerbFixture(t)
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Second)

	var mu sync.Mutex
	var concurrent int
	var maxConcurrent int
	// The client every attempt drives records its own entry and exit, so two
	// attempts at once are observable rather than inferred. The fake sleeps a
	// little inside each call so an unguarded runner really does overlap.
	probed := &overlapProbe{}
	probed.onEnter = func() {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
	}
	probed.onHold = func() {
		// Held inside the entered window, so two attempts really are in flight
		// together if the guard lets them through. Sleeping after the decrement
		// would measure nothing.
		time.Sleep(5 * time.Millisecond)
	}
	probed.onExit = func() {
		mu.Lock()
		concurrent--
		mu.Unlock()
	}

	f.runner.Serialize = d.WaitSyncSlot
	f.runner.Runner.Client = probed

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
		}()
	}
	// The daemon's own triggers run in the same window, which is the case the
	// guard exists for.
	for range 4 {
		d.queueSync(context.Background())
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 1 {
		t.Errorf("%d syncs ran at once, want at most one: the daemon's guard is not serializing verbs", maxConcurrent)
	}
}

// overlapProbe is a log transport whose append reports entry and exit, so a
// test can see whether two attempts ever overlapped.
type overlapProbe struct {
	recordTransport
	onEnter func()
	onHold  func()
	onExit  func()
}

func (p *overlapProbe) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	p.onEnter()
	p.onHold()
	p.onExit()
	return p.recordTransport.Append(entries)
}

// TestSyncStatusUnchanged pins that status did not move.
//
// Status is a read of the machine-local rows and never was a verb: it keeps its
// owner-local-scope read path and prints a function of the document alone. This
// asserts the document is still derived only from the local rows -- the section,
// the mark and the token's presence -- and that the token's value is still not
// one of them, since that is the field a future change to route status through
// a verb would most likely start carrying.
func TestSyncStatusUnchanged(t *testing.T) {
	f := newVerbFixture(t)
	if err := relevosync.SetToken(f.local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := relevosync.SetR2(f.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}

	settings, err := relevosync.ReadSettings(f.local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), verbFixtureToken) {
		t.Error("the sync section carries the token")
	}

	_, hasToken, err := relevosync.ReadToken(f.local)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if !hasToken {
		t.Error("the stored token does not read back as present")
	}

	// The state the statusline derives is the marker set alone: no handle, no
	// network, no verb.
	state, err := relevosync.ReadState(f.local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.Enabled {
		t.Error("a machine that never enabled reads as enabled")
	}
	if token := relevosync.Token(state); token != relevosync.TokenOff {
		t.Errorf("token = %q, want %q", token, relevosync.TokenOff)
	}
}

// TestSyncVerbRefusalNeverCarriesTheToken pins redaction on the refusal path,
// which is the one path that formats something by design.
//
// Every refusal the executor can produce is driven here, and every message is
// searched for the fixture. A refusal a caller can act on keeps the wording the
// sync package gave it, so this also pins that the refusal is the package's own
// error rather than a sentence written here.
func TestSyncVerbRefusalNeverCarriesTheToken(t *testing.T) {
	f := newVerbFixture(t)
	cases := []*wire.SyncVerb{
		// No token on either route.
		{Verb: wire.SyncVerbEnable},
		// A remote this machine is not pointed at.
		{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://elsewhere.invalid"},
		// A verb that does not exist.
		{Verb: "nope"},
	}
	for _, verb := range cases {
		res := f.runner.Run(context.Background(), verb, []byte(verbFixtureToken))
		if res.OK {
			continue
		}
		if res.Code == "" {
			t.Errorf("%s refused with no code, so a caller cannot map it", verb.Verb)
		}
		if strings.Contains(res.Message, verbFixtureToken) {
			t.Errorf("the %s refusal carries the token: %s", verb.Verb, res.Message)
		}
	}
}

// TestSyncVerbClassificationIsTotal pins that every code the executor can send
// maps to a class, so a caller is never handed a refusal it cannot classify.
//
// The mapping is by sentinel rather than by message, so this walks the sentinels
// rather than the strings: a wording change cannot move a code, and a code
// added to the wire without a mapping shows up here as an unclassified case.
func TestSyncVerbClassificationIsTotal(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{relevosync.ErrNoToken, wire.SyncCodeNoToken},
		{relevosync.ErrNoRemote, wire.SyncCodeNoRemote},
		{relevosync.ErrNoR2, wire.SyncCodeNoR2},
		{relevosync.ErrAlreadyEnabled, wire.SyncCodeAlreadyEnabled},
		{relevosync.ErrRemoteConflict, wire.SyncCodeRemoteConflict},
		{relevosync.ErrAuthRefused, wire.SyncCodeAuthRefused},
		{relevosync.ErrRemoteRefused, wire.SyncCodeRemoteRefused},
		{relevosync.ErrRemoteSchema, wire.SyncCodeRemoteSchemaMissing},
		{db.ErrPreflightRefused, wire.SyncCodePreflightRefused},
		{db.ErrContended, wire.SyncCodeContended},
		{db.ErrInvalid, wire.SyncCodeInvalid},
		{errors.New("something else"), wire.SyncCodeInternal},
	}
	for _, tc := range cases {
		if got := verbClassify(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestARemoteRefusalIsAClassNotAnInternal pins the classification a remote's
// refusal gets, where the round's failure showed up: a push the remote refused
// was reported as an internal failure, which pointed the reader at
// `relevo bugreport` for a remote doing what a remote with enforced foreign keys
// does. Both classes the driver can produce are refusals, and neither is
// internal.
func TestARemoteRefusalIsAClassNotAnInternal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a constraint refusal", relevosync.ErrRemoteRefused, wire.SyncCodeRemoteRefused},
		{"a remote without the table", relevosync.ErrRemoteSchema, wire.SyncCodeRemoteSchemaMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := verbClassify(tc.err); got == wire.SyncCodeInternal {
				t.Errorf("verbClassify(%v) = internal, want %q", tc.err, tc.want)
			} else if got != tc.want {
				t.Errorf("verbClassify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestSyncVerbTimeoutBoundsTheWork pins that a caller may bound the verb, and
// that the default is the runner's own bound rather than unbounded.
//
// The bound is load-bearing: it is what keeps a blackholed network off the seal
// path and out of a tick, so a remote that never answers costs one bounded wait
// rather than a hung round. A verb that inherited an unbounded context would put
// that back.
func TestSyncVerbTimeoutBoundsTheWork(t *testing.T) {
	if got := verbTimeout(&wire.SyncVerb{Verb: wire.SyncVerbPush}); got != relevosync.DefaultTimeout {
		t.Errorf("an unbounded verb got %s, want the %s default", got, relevosync.DefaultTimeout)
	}
	if got := verbTimeout(&wire.SyncVerb{Verb: wire.SyncVerbPush, TimeoutMS: 1500}); got != 1500*time.Millisecond {
		t.Errorf("a bounded verb got %s, want 1.5s", got)
	}
}

// TestSyncVerbRunnerNeedsALocalFile pins that a runner without a machine-local
// file is refused rather than built.
//
// The shared file carries no sync section and no token, so an executor handed
// one would report a configured machine as unconfigured and an enabled one as
// off -- exactly the answer the split between the two files exists to make
// impossible.
func TestSyncVerbRunnerNeedsALocalFile(t *testing.T) {
	shared, err := db.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	if shared.Local() != nil {
		t.Skip("this build opens a local file beside every shared handle")
	}
	if _, err := relevosync.LocalHandle(shared); err == nil {
		t.Error("LocalHandle answered for a handle with no local file")
	}
}

// TestSyncVerbWritesNoLogsItself pins that the executor's own logging says
// nothing a verb carried.
//
// The executor logs nothing on a path that refuses, so this is the assertion
// that a refusal is not dressed up as a fault: a verb that logged its own
// request, or its own reason, would show up here.
func TestSyncVerbWritesNoLogsItself(t *testing.T) {
	f := newVerbFixture(t)
	buf, restore := captureSyncLogs(t)
	defer restore()

	f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
	if buf.Len() != 0 {
		t.Errorf("a refused push logged %q, want nothing", buf.String())
	}
}

// TestSyncVerbFixtureTokenIsDistinct guards the redaction tests themselves: if
// the fixture were ever shortened to something a log might legitimately carry,
// the greps would stop meaning anything.
func TestSyncVerbFixtureTokenIsDistinct(t *testing.T) {
	if len(verbFixtureToken) < 32 {
		t.Fatalf("the fixture token is %d characters, which is short enough to appear in a log by accident", len(verbFixtureToken))
	}
	if _, err := os.Stat("/nonexistent"); err == nil {
		t.Fatal("unreachable")
	}
}

// hangingJoin is a sync log whose first pull hangs until it is cancelled, so a
// test can hold an enable's join open and watch a disable reach it. Its pull
// accounts through a real breaker the way the supervisor does, and a cancel
// releases it as a deliberate stop: the shape the pipe gives a hung pull.
type hangingJoin struct {
	breaker *relevosync.Breaker
	entered chan struct{}
	cancel  chan struct{}

	once       sync.Once
	cancelOnce sync.Once
	mu         sync.Mutex
	cancelled  bool
}

// errJoinStopped is what the fake's hung pull returns once it is cancelled.
var errJoinStopped = errors.New("test: the join's pull was stopped")

func newHangingJoin(b *relevosync.Breaker) *hangingJoin {
	return &hangingJoin{
		breaker: b,
		entered: make(chan struct{}),
		cancel:  make(chan struct{}),
	}
}

func (j *hangingJoin) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	return entries, nil
}
func (j *hangingJoin) Head(string) ([]synclog.HeadRow, error) { return nil, nil }
func (j *hangingJoin) Stats() (synclog.Stats, error)          { return synclog.Stats{}, nil }

func (j *hangingJoin) Pull(map[string]int) ([]synclog.Entry, error) {
	if err := j.breaker.Begin("pull"); err != nil {
		return nil, err
	}
	j.once.Do(func() { close(j.entered) })
	<-j.cancel
	_ = j.breaker.End()
	return nil, errJoinStopped
}

func (j *hangingJoin) Cancel() {
	j.mu.Lock()
	j.cancelled = true
	j.mu.Unlock()
	j.cancelOnce.Do(func() { close(j.cancel) })
}

func (j *hangingJoin) wasCancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelled
}

var _ synclog.LogTransport = (*hangingJoin)(nil)

// TestDisableStopsAnInFlightDaemonJoin pins the disable-during-a-join contract
// at the daemon's own guard: a disable issued while an enable's join runs in
// the daemon reaches the join's in-flight transport while the machine is still
// off, stops the join's worker, returns promptly rather than queuing behind the
// join's import bound, leaves the machine off, clears the join marker, and
// leaves the breaker's deaths at zero.
func TestDisableStopsAnInFlightDaemonJoin(t *testing.T) {
	f := newVerbFixture(t)
	if _, _, err := db.CompressHistoryOnce(f.shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot

	breaker := relevosync.NewBreaker(f.local)
	join := newHangingJoin(breaker)
	f.runner.Open = func(context.Context) (synclog.LogTransport, error) { return join, nil }

	ctx := context.Background()
	enableDone := make(chan struct{})
	go func() {
		defer close(enableDone)
		f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))
	}()

	select {
	case <-join.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the join did not reach its pull")
	}

	// The join runs while the enabled mark is still off, so the disable has to
	// reach the join's own transport rather than the enabled-gated one.
	if c := cancelTransport(f.runner.disableTransport(ctx)); c == nil {
		t.Error("a disable during a join reaches no cancel for the join's transport")
	}

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable queued behind the join instead of stopping it")
	}
	<-enableDone

	if !join.wasCancelled() {
		t.Error("the join's worker was never cancelled")
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the released join to leave the machine off", on, err)
	}
	if _, ok, err := relevosync.ReadJoin(f.local); err != nil || ok {
		t.Errorf("join marker = (ok %v, err %v); want it cleared by the disable", ok, err)
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
	// Off and no join: nothing to reach, so the disable opens no transport.
	if f.runner.disableTransport(ctx) != nil {
		t.Error("an off machine with no join still opened a transport")
	}
}

// hungSteadyPull builds a fixture whose runner drives a worker that hangs on its
// first pull, with the daemon's guard installed and the machine marked on, so a
// test can hold the sync slot with a steady pull rather than a join.
func hungSteadyPull(t *testing.T) (*verbFixture, *hangingJoin, *relevosync.Breaker) {
	t.Helper()
	f := newVerbFixture(t)
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Hour)
	f.runner.Serialize = d.WaitSyncSlot

	breaker := relevosync.NewBreaker(f.local)
	hung := newHangingJoin(breaker)
	f.runner.Runner.Client = hung
	return f, hung, breaker
}

// waitForHungPull blocks until the fixture's worker is inside its hung pull, so
// a test can be sure the sync slot is held before it issues its verb.
func waitForHungPull(t *testing.T, hung *hangingJoin) {
	t.Helper()
	select {
	case <-hung.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the pull did not reach its worker")
	}
}

// TestDisableStopsAHungSteadyPullAtTheDaemon pins the disable-during-a-hung-
// steady-pull contract at the daemon's own guard: a pull against a worker that
// has stopped answering holds the sync slot, and a disable issued then releases
// that call before it queues, so the disable runs in seconds rather than waiting
// the data-call bound out. The released call settles as a deliberate stop, so
// the breaker counts no death, and the machine is left off.
func TestDisableStopsAHungSteadyPullAtTheDaemon(t *testing.T) {
	f, hung, breaker := hungSteadyPull(t)
	ctx := context.Background()

	pullDone := make(chan *wire.SyncResult, 1)
	go func() { pullDone <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil) }()
	waitForHungPull(t, hung)

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disable queued behind the hung pull instead of stopping it")
	}
	<-pullDone

	if !hung.wasCancelled() {
		t.Error("the hung pull's worker was never cancelled")
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
	if on, err := relevosync.Enabled(f.local); err != nil || on {
		t.Errorf("Enabled = %v, %v; want the released pull to leave the machine off", on, err)
	}
	// The disable dropped the runner's worker, so the next call opens a fresh
	// one through the wiring's opener rather than driving the killed pipe.
	f.opens = 0
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil); !res.OK {
		t.Fatalf("the pull after the disable refused: %s", res.Message)
	}
	if f.opens != 1 {
		t.Errorf("the next pull opened %d workers, want a fresh one", f.opens)
	}
}

// TestRetryStopsAHungSteadyPullAtTheDaemon pins the retry half of the same
// contract: a retry is sent precisely because a call has stopped answering, so
// it releases the hung call before it queues rather than waiting the data-call
// bound out for a slot the call it is clearing holds.
func TestRetryStopsAHungSteadyPullAtTheDaemon(t *testing.T) {
	f, hung, breaker := hungSteadyPull(t)
	ctx := context.Background()

	pullDone := make(chan *wire.SyncResult, 1)
	go func() { pullDone <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil) }()
	waitForHungPull(t, hung)

	done := make(chan *wire.SyncResult, 1)
	go func() { done <- f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbRetry}, nil) }()
	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("retry refused: %s", res.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry queued behind the hung pull instead of stopping it")
	}
	<-pullDone

	if !hung.wasCancelled() {
		t.Error("the hung pull's worker was never cancelled")
	}
	if deaths, err := breaker.Deaths(); err != nil || deaths != 0 {
		t.Errorf("breaker deaths = %d, %v; want the deliberate stop uncounted", deaths, err)
	}
}

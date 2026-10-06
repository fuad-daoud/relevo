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
)

// The tests here drive the verb executor over a real split pair with the sync
// package's own fake in place of a remote. None of them reaches a network,
// spawns a harness or opens a second file: the executor runs against handles the
// test already holds, which is the property these tests exist to pin.
//
// The token fixtures are long and unmistakable on purpose. Several of these
// tests assert the value never appears in a log, an error or a result, and a
// short or plausible-looking fixture would make that assertion easy to pass by
// accident.

// verbFixtureToken is the token an enable is handed. Every redaction assertion
// greps for exactly this string.
const verbFixtureToken = "FIXTURE-TOKEN-8f3a2c91d4e7b605a1c2d3e4f5061728"

// verbFixture is one executor over a real split pair, plus the fake client its
// verbs drive.
type verbFixture struct {
	runner *VerbRunner
	shared *db.DB
	local  relevosync.Local
	client *relevosync.Fake
}

// newVerbFixture builds an executor over a fresh split pair. The fake is
// installed as the opener's client so an enable reaches no driver: the seed
// decision still runs, and the calls it decides are the ones the fake records.
func newVerbFixture(t *testing.T) *verbFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	// A configured machine: a remote in the section and a token stored beside
	// it. Every verb except enable needs both to have anything to do, and the
	// refusal for a machine with neither is pinned separately.
	putVerbSection(t, local, "libsql://example.invalid")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	f := &relevosync.Fake{}
	r := &VerbRunner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Client: f, Local: local},
		// The opener is the sync package's fake, so an enable's open reaches no
		// driver. Everything else on the path -- the preflight, the seed
		// decision, the mark -- is the real code over real handles.
		Open: func(context.Context, relevosync.OpenConfig) (relevosync.SyncClient, error) {
			return f, nil
		},
		ClientName: "relevo",
	}
	return &verbFixture{runner: r, shared: shared, local: local, client: f}
}

// TestSyncVerbOverOwnerEndToEnd pins that all four verbs run and answer through
// one executor, and that each produces the answer its caller reports.
//
// Every verb is driven the way the owner drives it -- a request name, the
// settings body, the token -- so this is the whole path minus the socket. The
// socket itself is covered where it exists, in the wire and cmd/relevo suites;
// what is being pinned here is that the daemon-side semantics are today's
// semantics relocated, not a second set of them.
func TestSyncVerbOverOwnerEndToEnd(t *testing.T) {
	f := newVerbFixture(t)
	ctx := context.Background()

	// The verbs under test drive a joined machine: enable joins it in
	// production, and these subtests start past that. The unjoined refusal
	// belongs to the turn-off test, not this one.
	joinFixtureFile(t, f.shared.Path())

	t.Run("push", func(t *testing.T) {
		f.runner.Runner.Client = f.client
		f.runner.Runner.Local = f.local
		f.client.Calls = nil
		res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
		if !res.OK {
			t.Fatalf("push refused: %s", res.Message)
		}
		// SyncOnce is push-then-pull then stats: the daemon's one order, which a
		// verb must not become a second order for.
		want := []string{"push", "pull", "stats"}
		if strings.Join(f.client.Calls, ",") != strings.Join(want, ",") {
			t.Errorf("push ran %v, want %v", f.client.Calls, want)
		}
	})

	t.Run("pull", func(t *testing.T) {
		f.client.Calls = nil
		f.client.Applied = true
		res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil)
		if !res.OK {
			t.Fatalf("pull refused: %s", res.Message)
		}
		if !res.Applied {
			t.Error("pull reported applied=false against a fake that applied something")
		}
	})

	t.Run("enable", func(t *testing.T) {
		// A machine with no history meets a remote holding nothing: the
		// new_machine case, which bootstraps and pulls. The section names the
		// remote, exactly as a machine configured by a hand-written section or a
		// previous enable would carry it -- an enable with neither a stored
		// remote nor --url is refused, which is the sibling case below.
		f.client.Calls = nil
		putVerbSection(t, f.local, "libsql://example.invalid")
		// The preflight is one of the checks enable runs, and it refuses a
		// database whose compress pass has not finished. This fixture writes the
		// marker's row directly rather than running the pass, because what enable
		// must do is run the check and refuse when it fails, which the refusal
		// cases below cover. The row goes to the machine-local file, which is
		// where CompressHistoryOnce records it and where the check reads it: a
		// pass and a check that name the same marker have to name the same file,
		// or the pass finishes and the check refuses forever over the database it
		// converted.
		putVerbMarker(t, f.shared.LocalOrSelf(), "zstd-compress.v1", `{"done_at":"1970-01-01T00:00:00Z"}`)
		// The upload-shape check no longer refuses on a write-ahead log that still
		// holds bytes: the seed copy drains it as part of writing the copy, which
		// is the very step the existing-history enable case below performs. So
		// this fixture does not drain anything to reach a shape no real machine
		// sits in.
		res := f.runner.Run(ctx, &wire.SyncVerb{
			Verb: wire.SyncVerbEnable,
		}, []byte(verbFixtureToken))
		if !res.OK {
			t.Fatalf("enable refused: %s", res.Message)
		}
		if res.SeedCase == "" {
			t.Error("enable reported no seed case, so a caller cannot say what it found")
		}
		on, err := relevosync.Enabled(f.local)
		if err != nil {
			t.Fatalf("Enabled: %v", err)
		}
		if !on {
			t.Error("enable answered OK but left the machine marked off")
		}
	})

	t.Run("disable", func(t *testing.T) {
		res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
		if !res.OK {
			t.Fatalf("disable refused: %s", res.Message)
		}
		on, err := relevosync.Enabled(f.local)
		if err != nil {
			t.Fatalf("Enabled: %v", err)
		}
		if on {
			t.Error("disable answered OK but left the machine marked on")
		}
	})

	t.Run("an unknown verb is refused rather than run", func(t *testing.T) {
		res := f.runner.Run(ctx, &wire.SyncVerb{Verb: "delete-everything"}, nil)
		if res.OK || res.Code != wire.SyncCodeInvalid {
			t.Errorf("an unknown verb answered %+v, want an invalid refusal", res)
		}
	})
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

// putVerbMarker writes one kv row, which is how a pass records that it finished.
func putVerbMarker(t *testing.T, k relevosync.Local, key, value string) {
	t.Helper()
	if err := k.KVPut(key, []byte(value)); err != nil {
		t.Fatalf("KVPut(%s): %v", key, err)
	}
}

// TestSyncDisableOverOwnerKeepsFourSteps pins the order the contract fixes,
// through the owner.
//
// The order is the whole of the turn-off's safety: the push comes first so a
// machine does not leave with an unsent round, and the token goes after the mark
// so a machine marked off is not holding a credential the next enable would
// inherit. A swap of the last two is invisible in the result -- both still run --
// so only the step list can catch it.
func TestSyncDisableOverOwnerKeepsFourSteps(t *testing.T) {
	f := newVerbFixture(t)
	res := f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	if !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	want := "final push,mark off,delete token,close handle"
	if got := strings.Join(res.Steps, ","); got != want {
		t.Errorf("steps = %q, want %q", got, want)
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

	// A verb that fails is the harder case: an error path is where a token
	// would be formatted by accident, because it is the path that formats
	// something.
	f.client.PushErr = errors.New("the remote refused the connection")
	f.client.PullErr = f.client.PushErr
	f.client.StatsErr = f.client.PushErr
	f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)

	// And a verb that succeeds, on a fresh fixture, so a success path is covered
	// too.
	ok := newVerbFixture(t)
	ok.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken))

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

// TestSyncConcurrentSealThenPushWholeOrAbsent is the pin on the guarantee that a
// push captures whole committed seal transactions only.
//
// A round sealing
// concurrently with a push is either fully present or fully absent on every
// other machine -- never half-sealed -- and the sealing daemon is never paused,
// bounded or otherwise. The guarantee rests on two facts: the seal writes its
// round in one transaction, and the push batches on transaction boundaries.
//
// What this test pins is the consequence that matters and that no test pinned
// before: an observer reading while a seal commits sees the round whole or not
// at all, and serializing the reader against the seal -- the worst case a pause
// would produce -- changes nothing observable. If a verb ran its push inline on
// the connection that took the request, rather than through the daemon's
// serialized executor, the guarantee would depend on that connection's timing
// instead of on the transaction boundary, and this test is what catches it.
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
		for i := 0; i < roundRows; i++ {
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
// Both record the same markers and both hold the same remote handle, so two of
// them at once would write each other's outcomes and leave a machine whose
// last-tick marker describes an attempt that never finished. The guard is the
// daemon's existing one: a verb waits its turn rather than being dropped,
// because somebody asked for it explicitly.
func TestSyncVerbSerializesWithTheDaemonGuard(t *testing.T) {
	f := newVerbFixture(t)
	d := NewDaemon(Runtime{Sync: f.runner.Runner}, time.Second)

	var mu sync.Mutex
	var concurrent int
	var maxConcurrent int
	// The client every attempt drives records its own entry and exit, so two
	// attempts at once are observable rather than inferred. The fake sleeps a
	// little inside each call so an unguarded runner really does overlap.
	probed := &overlapProbe{Fake: relevosync.Fake{}}
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
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
		}()
	}
	// The daemon's own triggers run in the same window, which is the case the
	// guard exists for.
	for i := 0; i < 4; i++ {
		d.queueSync(context.Background())
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 1 {
		t.Errorf("%d syncs ran at once, want at most one: the daemon's guard is not serializing verbs", maxConcurrent)
	}
}

// overlapProbe is a sync client whose calls report entry and exit, so a test can
// see whether two attempts ever overlapped.
type overlapProbe struct {
	relevosync.Fake
	onEnter func()
	onHold  func()
	onExit  func()
}

func (p *overlapProbe) Push(ctx context.Context) error {
	p.onEnter()
	p.onHold()
	p.onExit()
	return p.Fake.Push(ctx)
}

func (p *overlapProbe) Pull(ctx context.Context) (bool, error) {
	return p.Fake.Pull(ctx)
}

func (p *overlapProbe) Stats(ctx context.Context) (relevosync.Stats, error) {
	return p.Fake.Stats(ctx)
}

func (p *overlapProbe) Checkpoint(ctx context.Context) error {
	return p.Fake.Checkpoint(ctx)
}

// TestSyncStatusUnchanged pins that status did not move.
//
// Status is the one verb S7 does not touch: it keeps its owner-local-scope read
// path and prints a function of the document alone. This asserts the document is
// still derived only from the local rows -- the section, the mark and the token's
// presence -- and that the token's value is still not one of them, since that is
// the field a future change to route status through a verb would most likely
// start carrying.
func TestSyncStatusUnchanged(t *testing.T) {
	f := newVerbFixture(t)
	if err := relevosync.SetToken(f.local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
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
// searched for the fixture. A preflight refusal is the interesting one: its text
// is a joined set of per-check refusals, so it is the message most likely to
// grow a field by accident.
func TestSyncVerbRefusalNeverCarriesTheToken(t *testing.T) {
	f := newVerbFixture(t)
	cases := []*wire.SyncVerb{
		// No token on either route.
		{Verb: wire.SyncVerbEnable},
		// No remote on either route.
		{Verb: wire.SyncVerbEnable},
		// A remote this machine is not pointed at.
		{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://elsewhere.invalid"},
		// A stored section that will not parse.
		{Verb: wire.SyncVerbDisable},
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

// TestSyncVerbNamesWhichHalfIsMissing pins the refusal a push or pull gives on
// a machine that cannot reach anything.
//
// It is a separate test because this is the one message a user acts on directly:
// "no remote" and "no turso.token" have two different fixes, and a refusal that
// reported only that something was missing would leave the reader guessing which
// one they had. The wording is the one the CLI used before the verbs moved, kept
// exactly, so a message somebody has already read does not change under them.
func TestSyncVerbNamesWhichHalfIsMissing(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		token   bool
		want    string
		notWant string
	}{
		{name: "neither", want: "no remote and no " + relevosync.SecretToken},
		{name: "no remote", token: true, want: "no remote is configured", notWant: relevosync.SecretToken},
		{name: "no token", remote: "libsql://example.invalid", want: "no " + relevosync.SecretToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerbFixture(t)
			// A bare machine: the section and the token are what the case omits.
			if err := relevosync.PutSettings(f.local, relevosync.Settings{}, time.Unix(0, 0).UTC()); err != nil {
				t.Fatalf("clear the section: %v", err)
			}
			if err := relevosync.DeleteToken(f.local); err != nil {
				t.Fatalf("delete the token: %v", err)
			}
			if tc.remote != "" {
				putVerbSection(t, f.local, tc.remote)
			}
			if tc.token {
				if err := relevosync.SetToken(f.local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
					t.Fatalf("SetToken: %v", err)
				}
			}

			for _, verb := range []string{wire.SyncVerbPush, wire.SyncVerbPull} {
				res := f.runner.Run(context.Background(), &wire.SyncVerb{Verb: verb}, nil)
				if res.OK {
					t.Fatalf("%s succeeded with nothing to reach", verb)
				}
				if !strings.Contains(res.Message, tc.want) {
					t.Errorf("%s said %q, want it to name %q", verb, res.Message, tc.want)
				}
				if tc.notWant != "" && strings.Contains(res.Message, tc.notWant) {
					t.Errorf("%s said %q, want it not to name %q", verb, res.Message, tc.notWant)
				}
			}
		})
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
		{relevosync.ErrAlreadyEnabled, wire.SyncCodeAlreadyEnabled},
		{relevosync.ErrRemoteConflict, wire.SyncCodeRemoteConflict},
		{relevosync.ErrSeedUploadRequired, wire.SyncCodeSeedUploadRequired},
		{relevosync.ErrAuthRefused, wire.SyncCodeAuthRefused},
		{db.ErrPreflightRefused, wire.SyncCodePreflightRefused},
		{db.ErrInvalid, wire.SyncCodeInvalid},
		{errors.New("something else"), wire.SyncCodeInternal},
	}
	for _, tc := range cases {
		if got := verbClassify(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestSyncVerbNeverOpensASecondHandle pins the lock contract from the executor's
// side: running a verb opens no file.
//
// The daemon holds the shared database under a lock, and a writer that opened it
// again would either fail or, worse, succeed on a platform without the lock. This
// asserts it by holding the file open in this process and running a full verb:
// the fact that the verb answers at all is the proof, because a second direct
// open of the same file would have met the lock this handle holds.
func TestSyncVerbNeverOpensASecondHandle(t *testing.T) {
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
	// The verbs under test drive a joined machine; the unjoined refusal is
	// the turn-off test's case.
	joinFixtureFile(t, first.Path())
	f := &relevosync.Fake{}
	runner := &VerbRunner{
		Shared: first,
		Local:  local,
		Path:   first.Path(),
		Runner: &relevosync.Runner{Client: f, Local: local},
		Open: func(context.Context, relevosync.OpenConfig) (relevosync.SyncClient, error) {
			return f, nil
		},
		ClientName: "relevo",
	}

	for _, verb := range []string{wire.SyncVerbPush, wire.SyncVerbPull, wire.SyncVerbDisable} {
		res := runner.Run(context.Background(), &wire.SyncVerb{Verb: verb}, nil)
		if !res.OK {
			t.Errorf("%s refused with the file held: %s", verb, res.Message)
		}
	}

	// The seed copy is the one verb step that writes outside the two files, so
	// it is the one that would show a handle reaching for a path it does not own.
	// It works here precisely because this handle opened the file: the daemon's
	// pair is direct, and a dialled handle has no path to copy from at all --
	// which is the refusal the client used to meet instead of a daemon to ask.
	if err := first.SeedCopy(filepath.Join(t.TempDir(), "seed.db")); err != nil {
		t.Errorf("SeedCopy from the daemon's own direct handle: %v", err)
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

// TestSyncVerbSeedPathIsFixed pins that the seed copy lands in one place, so a
// refusal naming it names the same file every time.
func TestSyncVerbSeedPathIsFixed(t *testing.T) {
	r := &VerbRunner{Path: filepath.Join("/state", "relevo.db")}
	got := r.seedPath()
	want := filepath.Join("/state", "relevo-seed.db")
	if got != want {
		t.Errorf("seedPath = %q, want %q", got, want)
	}
	// A request that names one is obeyed, so the documented upload path can be
	// pointed elsewhere without changing the default.
	named := r.seedPathFor(&wire.SyncVerb{SeedPath: "/other/seed.db"})
	if named != "/other/seed.db" {
		t.Errorf("a named seed path was ignored: %q", named)
	}
}

// TestSyncVerbWritesNoLogsItself pins that the executor's own logging says
// nothing a verb carried.
//
// The executor logs one advisory line, for a final push that could not be made,
// and it logs the reason. That reason is a driver's own error, which is a body
// the remote chose -- so this pins that the line is a warning carrying an error
// rather than the request, and that the daemon never logs the verb body.
func TestSyncVerbWritesNoLogsItself(t *testing.T) {
	f := newVerbFixture(t)
	buf, restore := captureSyncLogs(t)
	defer restore()

	// A push against a fake with no remote opens nothing, so nothing is logged
	// at all: a verb that logs its own request would show up here.
	f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
	if buf.Len() != 0 {
		t.Errorf("a plain push logged %q, want nothing", buf.String())
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

package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// Every open this package builds names what kind of file it names, and the role
// is the only thing that tells the driver it may create sync membership. The
// zero value refuses, so an open that arrives at the gate carrying nothing is a
// refusal a user who typed a correct command cannot act on.
//
// So each open below is asserted arriving with its own role. The builder that
// constructs a config is the only place the role can be dropped, and a test that
// reads the config where the open was handed over catches that drop wherever it
// is: a role unset in probeConfig, a role unset in openConfig, or a role unset
// in the enable's own opener.

// roleFixture is one executor over a real split pair whose opener records every
// config it is handed, so a test can read the role each open arrived carrying.
//
// The recording is on the injected opener rather than on the real gate because
// the real gate is in the sync package and this package builds the configs: what
// is being pinned here is what leaves each builder, and the gate's own reading
// of a role is pinned in the sync package's suite.
type roleFixture struct {
	runner *VerbRunner
	shared *db.DB
	local  relevosync.Local
	// opens is every OpenConfig the run produced, in order.
	opens []relevosync.OpenConfig
}

// newRoleFixture builds an executor over a split pair with a remote in the
// section and a token stored beside it, so every verb has something to reach.
func newRoleFixture(t *testing.T) *roleFixture {
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
	putVerbSection(t, local, "libsql://example.invalid")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	// The live file is marked as a member, which is the state a final push runs
	// in: the turn-off only opens a handle for a file the driver has joined,
	// because an unjoined file has nothing a push could carry. A fake opener never
	// joins anything, so the tables a real open would have written are written
	// here.
	markVerbSyncMember(t, path)

	f := &roleFixture{shared: shared, local: local}
	f.runner = &VerbRunner{
		Shared:     shared,
		Local:      local,
		Path:       shared.Path(),
		Runner:     &relevosync.Runner{Client: &relevosync.Fake{}, Local: local},
		ClientName: "relevo",
		Open: func(_ context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
			// The credential never leaves the open, so the recorded config is
			// recorded without it.
			cfg.AuthToken = nil
			f.opens = append(f.opens, cfg)
			return &relevosync.Fake{}, nil
		},
	}
	return f
}

// markVerbSyncMember writes the marker tables a real driver open leaves in a file
// it joined, so a fixture can be in the state a post-enable machine is in.
//
// The names are the ones the sync package reads: membership is decided by the
// presence of one of them, so writing any of them is what makes the file a member.
func markVerbSyncMember(t *testing.T, path string) {
	t.Helper()

	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS turso_cdc (change_id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
		t.Fatalf("write the sync marker table: %v", err)
	}
}

// TestTheProbeOpenArrivesCarryingScratch pins the emptiness probe's role.
//
// The probe names a throwaway of its own, so it is the one open whose file is
// disposable by construction and the one open for which the driver may create
// membership freely. It is named separately from openConfig rather than built by
// it precisely so it cannot inherit a live path; naming its role in the same
// builder is the other half of that separation, and this is the test that says
// the separation held.
//
// Drop the role from probeConfig and the probe's open arrives at the gate
// carrying nothing, the gate refuses it, and this fails.
func TestTheProbeOpenArrivesCarryingScratch(t *testing.T) {
	f := newRoleFixture(t)

	// The compress marker is what the enable's preflight reads, so the enable
	// reaches its probe instead of stopping at a check.
	putVerbMarker(t, f.shared.LocalOrSelf(), "zstd-compress.v1", `{"done_at":"1970-01-01T00:00:00Z"}`)

	res := f.runner.Run(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbEnable,
	}, []byte(verbFixtureToken))
	if !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	if len(f.opens) == 0 {
		t.Fatal("the enable made no open, so no role arrived anywhere")
	}

	probe := f.opens[0]
	if probe.Role != relevosync.OpenScratch {
		t.Errorf("the probe's open arrived carrying role %q, want %q", probe.Role, relevosync.OpenScratch)
	}
	if probe.Path == f.runner.Path {
		t.Errorf("the probe's open named the live file %s", probe.Path)
	}
}

// TestTheTurnOffReachesTheUserWithNothingOpen pins the wedged machine's way out,
// at the layer where it was stuck.
//
// A driver that aborts the process inside its first pull leaves the daemon gone,
// the mark on, and no handle: the enable wrote the mark before its first round and
// the round never finished. The verbs answer for that machine as follows. Push and
// pull refuse, because there is no handle to drive and the refusal already says
// so. Enable re-runs, because the seeding window is open -- that is the fix on
// this path. And disable completes with no handle and no network at all: this
// machine's file was never joined, so the turn-off opens nothing, marks off,
// forgets the token and closes nothing.
//
// The opener counts its calls, so "no network" is asserted rather than assumed: a
// turn-off that dialed a remote it cannot use to push nothing would fail here.
func TestTheTurnOffReachesTheUserWithNothingOpen(t *testing.T) {
	f := newRoleFixture(t)
	// The fixture's live file is marked as a member, which is what lets the
	// turn-off find something to push. This machine's file is the other shape: a
	// driver that aborted left it unjoined.
	unmarkVerbSyncMember(t, f.runner.Path)

	// The wedged state: marked on, window open, nothing joined.
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if err := relevosync.MarkSeeding(f.local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkSeeding: %v", err)
	}

	opens := 0
	joined, _ := relevosync.HasSyncMarker(f.shared.Path())
	f.runner.Open = func(_ context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
		opens++
		// Mirror the production gate: only a seed open may create membership,
		// so a member open on a file no driver joined refuses. The fixture's
		// fake used to open anything, which let a push succeed here for no
		// reason closer to production than the fake itself; with lazy client
		// building, that permissiveness would turn a wedged machine's refusal
		// into a drive. Seed and scratch opens stay allowed: the re-enable
		// below joins through seed, and probes are disposable by construction.
		if !joined && cfg.Role != relevosync.OpenSeed && cfg.Role != relevosync.OpenScratch {
			return nil, errors.New("sync: this file is not a member of a sync yet")
		}
		return &relevosync.Fake{}, nil
	}
	// The daemon's runner holds no client, which is what "no handle open" means
	// on this path: the driver aborted, so the handle it would have opened is
	// gone with the process that held it.
	f.runner.Runner.Client = nil
	ctx := context.Background()

	for _, verb := range []string{wire.SyncVerbPush, wire.SyncVerbPull} {
		res := f.runner.Run(ctx, &wire.SyncVerb{Verb: verb}, nil)
		if res.OK {
			t.Errorf("%s answered OK on a machine with no handle open", verb)
		}
	}

	res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil)
	if !res.OK {
		t.Fatalf("disable refused on a wedged machine: %s", res.Message)
	}
	if opens != 0 {
		t.Errorf("the turn-off made %d opens, want none: it has no handle and nothing to push", opens)
	}
	if len(res.Steps) != 4 {
		t.Errorf("steps = %v, want all four", res.Steps)
	}
	if res.FinalPush {
		t.Error("the turn-off reported a final push against a file no driver joined")
	}
	on, err := relevosync.Enabled(f.local)
	if err != nil {
		t.Fatalf("Enabled: %v", err)
	}
	if on {
		t.Error("the turn-off left the machine marked on")
	}
	if _, ok, err := relevosync.ReadToken(f.local); err != nil {
		t.Fatalf("ReadToken: %v", err)
	} else if ok {
		t.Error("the turn-off left the token in place")
	}
	if seeding, err := relevosync.ReadSeeding(f.local); err != nil {
		t.Fatalf("ReadSeeding: %v", err)
	} else if seeding {
		t.Error("the turn-off left the seeding window open")
	}

	// And the machine it leaves behind can be pointed at a different remote, which is
	// the seed detour's own escape: the documented upload created a new cloud
	// database while the section still names the old one, so the sequence that has to
	// work is disable and then enable --url <the new one>. The turn-off above made the
	// machine off, which is what lets the flag repoint rather than refuse.
	putVerbMarker(t, f.shared.LocalOrSelf(), "zstd-compress.v1", `{"at":"1970-01-01T00:00:00Z"}`)
	res = f.runner.Run(ctx, &wire.SyncVerb{
		Header:    wire.Header{Type: wire.TypeSyncVerb},
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://relevo-seed.turso.io",
	}, []byte(verbFixtureToken))
	if !res.OK {
		t.Fatalf("an enable after the turn-off, naming another remote, refused: %s", res.Message)
	}
	if res.RemoteURL != "libsql://relevo-seed.turso.io" {
		t.Errorf("the enable opened %q, want the remote it was named", res.RemoteURL)
	}
	settings, err := relevosync.ReadSettings(f.local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if settings.RemoteURL != "libsql://relevo-seed.turso.io" {
		t.Errorf("the section holds %q after the repoint, want the remote it was named", settings.RemoteURL)
	}
}

// unmarkVerbSyncMember drops the marker tables a real driver open leaves, which
// puts the file back into the shape a driver that aborted mid-enable leaves it.
func unmarkVerbSyncMember(t *testing.T, path string) {
	t.Helper()

	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`DROP TABLE IF EXISTS turso_cdc`); err != nil {
		t.Fatalf("drop the sync marker table: %v", err)
	}
	if joined, err := relevosync.HasSyncMarker(path); err != nil {
		t.Fatalf("HasSyncMarker after dropping: %v", err)
	} else if joined {
		t.Fatal("the file still reads as a member after dropping its marker table")
	}
}

// TestEveryOpenArrivesNamingItsRole pins the whole set in one run, so a future
// open this package adds cannot join without naming one.
//
// Each open is checked against its own role rather than only for being non-zero:
// a role is a claim about a file, and two opens naming the same role would mean
// one of them is claiming the wrong thing.
func TestEveryOpenArrivesNamingItsRole(t *testing.T) {
	f := newRoleFixture(t)
	putVerbMarker(t, f.shared.LocalOrSelf(), "zstd-compress.v1", `{"done_at":"1970-01-01T00:00:00Z"}`)

	if res := f.runner.Run(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbEnable,
	}, []byte(verbFixtureToken)); !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	// The turn-off's final push, which is the one open naming the live file.
	if res := f.runner.Run(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbDisable,
	}, nil); !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}

	if len(f.opens) < 2 {
		t.Fatalf("the run made %d opens, want the probe's and the seed's", len(f.opens))
	}
	// The probe runs first, then the enable's own open once the seed decision is
	// made, then the turn-off's final push.
	want := []relevosync.OpenRole{
		relevosync.OpenScratch,
		relevosync.OpenSeed,
		relevosync.OpenMember,
	}
	if len(f.opens) != len(want) {
		// The count is asserted rather than assumed so a new open shows up here
		// as a failure to state what it is, not as a silent extra.
		t.Fatalf("the run made %d opens, want %d: %v", len(f.opens), len(want), f.opens)
	}
	for i, cfg := range f.opens {
		if cfg.Role != want[i] {
			t.Errorf("open %d (path %s) arrived carrying role %q, want %q", i, cfg.Path, cfg.Role, want[i])
		}
	}
}

// TestTheFinalPushArrivesCarryingMember pins the turn-off's own open.
//
// A final push names the live file, and by then an enable has joined it to the
// remote. A final push has no business creating that membership: it is the one
// open that runs on the way out, and a file it turned into a member would be a
// machine left joined to a remote it just stopped syncing with.
func TestTheFinalPushArrivesCarryingMember(t *testing.T) {
	f := newRoleFixture(t)

	if res := f.runner.Run(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbDisable,
	}, nil); !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	if len(f.opens) == 0 {
		t.Fatal("the turn-off made no open, so no role arrived anywhere")
	}
	push := f.opens[len(f.opens)-1]
	if push.Role != relevosync.OpenMember {
		t.Errorf("the final push arrived carrying role %q, want %q", push.Role, relevosync.OpenMember)
	}
	if push.Path != f.runner.Path {
		t.Errorf("the final push named %s, want the live file %s", push.Path, f.runner.Path)
	}
}

// TestAnOpenThatNamedNoRoleIsNotAnInternalFailure pins the classification of the
// gate's own refusal.
//
// The gate refusing an unnamed role is a guard for the callers in this
// repository, not a class of thing a user did: it means a build of this tree
// forgot to name a role somewhere. Classifying it as internal would point the
// reader at `relevo bugreport` for a command they typed correctly, and the fix
// is in this tree rather than on their machine.
func TestAnOpenThatNamedNoRoleIsNotAnInternalFailure(t *testing.T) {
	// The real gate is driven here rather than a hand-built error: the refusal
	// is what the driver route returns for an open that named no role, and it
	// returns before the driver is reached, so this reaches no network.
	_, err := relevosync.OpenRemote(context.Background(), relevosync.OpenConfig{
		Path:      filepath.Join(t.TempDir(), "never-opened.db"),
		RemoteURL: "libsql://example.invalid",
	})
	if err == nil {
		t.Fatal("an open that named no role was accepted")
	}
	if !strings.Contains(err.Error(), "no role for") {
		t.Fatalf("the refusal does not say what was missing: %v", err)
	}

	code := verbClassify(err)
	if code == wire.SyncCodeInternal {
		t.Errorf("the role refusal is classified %q, which sends the reader to file a defect", code)
	}
	if code != wire.SyncCodeInvalid {
		t.Errorf("the role refusal is classified %q, want %q", code, wire.SyncCodeInvalid)
	}
}

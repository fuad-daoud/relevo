package relevo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The tests here reproduce the data loss with a fixture rather than against a
// live machine: a database holding this machine's history, an enable that asks
// an empty remote whether it holds anything, and the hash of the live file
// before and after.
//
// The mechanism was that the emptiness probe opened the live file and pulled
// into it. Pointed at a database with history and a remote with nothing, that
// pull applied the remote's emptiness over the local file: 209MB became a 32KB
// skeleton, and the seed copy faithfully copied the skeleton. So the assertions
// here are about bytes on disk, not about a return value.

// historyRows is how many binding rows the fixture writes. Enough that a file
// holding them is unmistakably larger than a skeleton, so a wipe is visible as a
// size change and not only as a hash change.
const historyRows = 40

// probeOwner is the owner every fixture row carries, so the row count is a
// single owner's list rather than a whole-table scan.
const probeOwner = "probe-owner"

// probeFixture is one executor over a split pair whose shared file holds real
// history, plus the opens it made so a test can read what each one named.
type probeFixture struct {
	runner   *VerbRunner
	shared   *db.DB
	local    relevosync.Local
	livePath string
	// opens is every OpenConfig the run produced, in order.
	opens []relevosync.OpenConfig
	// client is the fake the probe's open returns.
	client *probeClient
	// opened is the path the most recent open named, which is the file a pull
	// rewrites.
	opened string
	// remote reports what the fake remote holds, which decides both whether the
	// pull reports it applied something and what the pull writes.
	remote probeRemote
	// t is the test the fixture belongs to, so the fake can fail the test rather
	// than swallow the error a destructive pull would report.
	t *testing.T
}

// probeRemote is what the fake remote holds. The driver applies the remote's
// state over the file it was handed, so an empty remote is what destroys a
// local database -- which is the whole mechanism this fixture reproduces.
type probeRemote struct{ empty bool }

// probeClient is the fake the fixture's opens hand back. Its Pull applies the
// remote's state over the file the open named, which is the driver's behaviour
// and the reason naming the wrong file in a probe was destructive.
type probeClient struct {
	fixture *probeFixture
	// pushed records that this client's change set reached the remote, which is
	// what stops a later pull from finding the remote empty.
	pushed bool
}

var _ relevosync.SyncClient = (*probeClient)(nil)

// Push carries the change set to the remote. The remote is no longer empty once
// it has one, which is the whole reason the enable's push-then-pull order is
// safe and a bare pull against an empty remote is not.
func (c *probeClient) Push(context.Context) error {
	c.pushed = true
	c.fixture.remote.empty = false
	return nil
}

// Pull rewrites the file this client's open named, bringing it to the remote's
// state. Against a remote holding nothing that is the wipe: the file keeps its
// header and loses every row, which is the 209MB-to-32KB outcome at fixture
// scale.
func (c *probeClient) Pull(context.Context) (bool, error) {
	remote := c.fixture.remote
	applied := !remote.empty
	if remote.empty {
		if err := c.wipe(c.fixture.opened); err != nil {
			return false, err
		}
	}
	return applied, nil
}

// wipe applies an empty remote's state to path.
func (c *probeClient) wipe(path string) error {
	if err := os.Truncate(path, 0); err != nil {
		return err
	}
	empty, err := db.OpenRaw(path)
	if err != nil {
		return err
	}
	defer func() { _ = empty.Close() }()
	_, err = empty.Exec(`CREATE TABLE turso_cdc (change_id INTEGER PRIMARY KEY AUTOINCREMENT)`)
	return err
}

func (c *probeClient) Stats(context.Context) (relevosync.Stats, error) {
	return relevosync.Stats{}, nil
}

func (c *probeClient) Checkpoint(context.Context) error { return nil }

// newProbeFixture builds an executor over a shared file holding history rows, a
// configured machine, and a fake remote whose pull brings nothing back -- which
// is the state that made the real pull wipe the file.
func newProbeFixture(t *testing.T) *probeFixture {
	t.Helper()

	dir := probeOwnedDir(t)
	path := filepath.Join(dir, "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	// A machine configured for sync, with the compress mark the preflight reads,
	// so the enable reaches its seed decision rather than stopping at a check.
	putVerbSection(t, local, "libsql://relevo-test-org.turso.io")
	if err := relevosync.SetToken(local, []byte(verbFixtureToken), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	writeProbeHistory(t, shared)
	// The rows are stamped with an origin so the enable's preflight reaches its
	// seed decision. That check is the origin gate's, and a fixture whose rows
	// have no origin would be refused before the probe ever ran -- which would
	// make every test here pass without exercising anything.
	if _, _, err := db.BackfillOriginOnce(shared, "probe-installation", time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
	putVerbMarker(t, local, "zstd-compress.v1", `{"at":"2026-10-05T09:00:00Z"}`)
	// The upload shape the preflight asserts: a drained log and the page size the
	// upload path needs. A seed copy brings both about, so one is written and
	// removed rather than leaving a second seed on disk.
	drain := filepath.Join(dir, "drain.db")
	if err := shared.SeedCopy(drain); err != nil {
		t.Fatalf("SeedCopy to drain the log: %v", err)
	}
	if err := os.Remove(drain); err != nil {
		t.Fatalf("remove the drain copy: %v", err)
	}
	if pre := db.EnablePreflight(shared); !pre.OK() {
		t.Fatalf("the fixture did not reach the asserted shape: %v", pre.Err())
	}

	f := &probeFixture{
		t: t, shared: shared, local: local, livePath: shared.Path(),
		// The remote holds data: the probe's pull applies what came back, which
		// is what makes the seed matrix meet history on both sides and refuse
		// with the upload instruction. That is the arm this fixture is for, and
		// it is the arm the upload refusal belongs to.
		remote: probeRemote{empty: false},
	}
	f.client = &probeClient{fixture: f}
	f.runner = &VerbRunner{
		Shared: shared,
		Local:  local,
		Path:   shared.Path(),
		Runner: &relevosync.Runner{Client: f.client, Local: local},
		// The fake stands in for the driver. What it does with the path it is
		// handed is the driver's business and is not what these tests pin; what
		// they pin is which path each open named, and the bytes of the live file.
		Open: func(_ context.Context, cfg relevosync.OpenConfig) (relevosync.SyncClient, error) {
			// The credential never travels out of the open, so the recorded config
			// is recorded without it.
			cfg.AuthToken = nil
			f.opens = append(f.opens, cfg)
			f.opened = cfg.Path
			return f.client, nil
		},
		ClientName: "relevo",
	}
	return f
}

// probeOwnedDir is a directory the test owns and removes itself, rather than
// t.TempDir. The turso driver writes scratch of its own beside whatever file it
// opens, from a thread this side does not wait for, and t.TempDir reports a
// failure for a file landing in the middle of its own removal -- which would
// fail a test over an artefact it did not create.
func probeOwnedDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "relevo-probe-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return dir
}

// writeProbeHistory puts historyRows rows into the shared file's binding_record
// table. They are the rows a pull's rebase would roll back, so their absence
// after a probe is the data loss itself.
//
// Each row gets its own name because a record is keyed on owner and name: one
// shared name would make every write an update of the first, and the fixture
// would hold a single row whatever it claimed to write.
func writeProbeHistory(t *testing.T, d *db.DB) {
	t.Helper()

	for i := range historyRows {
		rec := db.Record{
			ID:        fmt.Sprintf("probe-%03d", i),
			Owner:     probeOwner,
			Name:      fmt.Sprintf("probe-%03d", i),
			State:     "done",
			Round:     1,
			CWD:       "/probe",
			JSON:      `{"probe":true}`,
			CreatedAt: time.Unix(0, 0).UTC(),
			UpdatedAt: time.Unix(0, 0).UTC(),
		}
		if _, err := d.RecordPut(rec); err != nil {
			t.Fatalf("RecordPut %s: %v", rec.Name, err)
		}
	}
}

// probeRunEnable drives the enable verb with the token, exactly as the owner
// does, and answers the result.
func probeRunEnable(f *probeFixture) *wire.SyncResult {
	return f.runner.Run(context.Background(), &wire.SyncVerb{
		Header: wire.Header{Type: wire.TypeSyncVerb},
		Verb:   wire.SyncVerbEnable,
	}, []byte(verbFixtureToken))
}

// TestProbeNeverNamesTheLiveFile is the load-bearing assertion of this whole
// round: the emptiness probe opens a file of its own, and the live database's
// bytes are identical before and after.
//
// An empty remote is what makes the old probe destructive -- a pull against it
// applies its emptiness -- so this runs the probe against exactly that and
// compares the live file's hash across it. Restoring the live path to the probe
// is one edit, and it is what makes this test fail.
func TestProbeNeverNamesTheLiveFile(t *testing.T) {
	f := newProbeFixture(t)
	before, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file before: %v", err)
	}
	beforeInfo, err := os.Stat(f.livePath)
	if err != nil {
		t.Fatalf("stat the live file before: %v", err)
	}

	// A fake whose pull reports it applied nothing: the remote holds no data.
	res := probeRunEnable(f)

	after, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file after: %v", err)
	}
	afterInfo, err := os.Stat(f.livePath)
	if err != nil {
		t.Fatalf("stat the live file after: %v", err)
	}

	if before != after {
		t.Errorf("the live file changed across the probe: %d bytes before, %d after, hash %s -> %s",
			beforeInfo.Size(), afterInfo.Size(), before[:16], after[:16])
	}
	if beforeInfo.Size() != afterInfo.Size() {
		t.Errorf("the live file was resized across the probe: %d -> %d", beforeInfo.Size(), afterInfo.Size())
	}

	// Every row is still readable, which is the loss stated in the terms the
	// user would notice it in.
	if got := probeCountRows(t, f.shared); got != historyRows {
		t.Errorf("the shared file holds %d rows after the probe, want %d", got, historyRows)
	}

	// The enable reached its seed decision and refused with the upload
	// instruction, rather than being answered by a probe that destroyed anything.
	if res.OK {
		t.Fatalf("the enable reported success against an empty remote holding no data: %+v", res)
	}
	if res.Code != wire.SyncCodeSeedUploadRequired {
		t.Errorf("code = %q, want %q: %s", res.Code, wire.SyncCodeSeedUploadRequired, res.Message)
	}
	// The command is pinned rather than merely checked for a word: the shipped
	// turso CLI takes the file as a positional and prompts for the target, so a
	// message naming a flag form the CLI does not have names a command the user
	// cannot run. The seed path is named too, so the message is copy-pasteable.
	seed := f.runner.seedPath()
	if !strings.Contains(res.Message, "with `turso db import "+seed+"`") {
		t.Errorf("the upload refusal does not name the working command and the seed path.\n got: %s", res.Message)
	}
	if strings.Contains(res.Message, "--from-file") {
		t.Errorf("the upload refusal names a flag form the CLI does not have: %s", res.Message)
	}
}

// TestProbeAgainstAnEmptyRemoteLeavesTheLiveFileIntact is the reproduction of
// the data loss itself, and the assertion the plan's fixture is written for: a
// history-holding database, an empty remote, and the live file's bytes
// identical across the probe.
//
// The empty remote is what made the real pull destructive -- a pull applies the
// remote's state, and applying "nothing" to a file holding history is the wipe.
// So the fake remote here reports empty and its pull rewrites whatever file the
// open named. Restore the live path to the probe and the live file is truncated
// in this test, which is the failure it exists to catch.
func TestProbeAgainstAnEmptyRemoteLeavesTheLiveFileIntact(t *testing.T) {
	f := newProbeFixture(t)
	f.remote = probeRemote{empty: true}

	before, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file before: %v", err)
	}
	beforeRows := probeCountRows(t, f.shared)
	if beforeRows != historyRows {
		t.Fatalf("the fixture holds %d rows before the probe, want %d", beforeRows, historyRows)
	}

	res := probeRunEnable(f)

	after, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file after: %v", err)
	}
	if before != after {
		t.Errorf("an empty remote's pull rewrote the live file: hash %s -> %s", before[:16], after[:16])
	}
	if got := probeCountRows(t, f.shared); got != historyRows {
		t.Errorf("the live file holds %d rows after an empty remote's pull, want %d", got, historyRows)
	}

	// An empty remote is a real answer, not a refusal: this is the empty-cloud
	// arm, so the enable proceeds rather than stopping.
	if !res.OK {
		t.Errorf("an empty remote refused the enable: %s", res.Message)
	}
	if res.SeedCase != string(relevosync.SeedEmptyCloud) {
		t.Errorf("seed case = %q, want %q", res.SeedCase, relevosync.SeedEmptyCloud)
	}
}

// TestProbeOpensNoLivePath walks every open an enable made and asserts none named
// the live file. The hash test above pins the bytes; this pins the cause, so a
// fix that happened to preserve the bytes by another route still has to have
// stopped naming the live path.
func TestProbeOpensNoLivePath(t *testing.T) {
	f := newProbeFixture(t)
	if res := probeRunEnable(f); res.OK {
		t.Fatalf("the enable reported success: %+v", res)
	}

	if len(f.opens) == 0 {
		t.Fatal("the enable opened nothing, so nothing was checked")
	}
	live, err := filepath.EvalSymlinks(f.livePath)
	if err != nil {
		live = f.livePath
	}
	for i, cfg := range f.opens {
		if cfg.Path == f.livePath {
			t.Errorf("open %d named the live file %s", i, cfg.Path)
		}
		if resolved, rerr := filepath.EvalSymlinks(cfg.Path); rerr == nil && resolved == live {
			t.Errorf("open %d named the live file by another path: %s", i, cfg.Path)
		}
	}
}

// TestProbeLeavesNoThrowawayBehind pins that the probe's own file is cleaned up.
// A throwaway that outlived its question would accumulate beside the live file,
// and one left in place is one a later run could mistake for a database.
func TestProbeLeavesNoThrowawayBehind(t *testing.T) {
	f := newProbeFixture(t)
	if res := probeRunEnable(f); res.OK {
		t.Fatalf("the enable reported success: %+v", res)
	}

	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(f.livePath), ".relevo-probe-*"))
	if err != nil {
		t.Fatalf("glob the probe scratch: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("the probe left %v behind beside %s", leftovers, f.livePath)
	}
}

// TestProbeRefusesARemoteItCannotReach pins that a probe which cannot open the
// remote says so rather than answering "the remote is empty". An enable that
// read a failed probe as an empty remote would take the empty-cloud arm and push
// over a remote it never asked.
func TestProbeRefusesARemoteItCannotReach(t *testing.T) {
	f := newProbeFixture(t)
	wantErr := errors.New("no such remote")
	f.runner.Open = func(context.Context, relevosync.OpenConfig) (relevosync.SyncClient, error) {
		return nil, wantErr
	}
	before, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file: %v", err)
	}

	res := probeRunEnable(f)
	if res.OK {
		t.Fatalf("the enable reported success with an unopenable remote: %+v", res)
	}
	if !strings.Contains(res.Message, wantErr.Error()) {
		t.Errorf("the refusal does not carry the open's own failure: %s", res.Message)
	}
	// It must not have been answered as the empty-cloud arm, which is what
	// pushing over a remote the probe never reached would look like.
	if strings.Contains(res.Message, "turso db import") {
		t.Errorf("an unopenable remote was answered as an empty one: %s", res.Message)
	}
	after, herr := probeHash(f.livePath)
	if herr != nil {
		t.Fatalf("hash the live file after: %v", herr)
	}
	if before != after {
		t.Error("a refused probe still changed the live file")
	}
}

// TestSeedCopyHoldsHistoryAndNoSecret pins what the seed copy is for, on the
// same fixture the probe test uses: every shared row is in it, and nothing the
// machine-local file holds is.
//
// This is the half of the loss that was never the bug: the copy faithfully
// copied whatever the live file held, and when the probe had already reduced
// that to a skeleton the copy carried the skeleton. The copy is worth keeping
// exactly because it holds what the live file held.
func TestSeedCopyHoldsHistoryAndNoSecret(t *testing.T) {
	f := newProbeFixture(t)
	seed := filepath.Join(probeOwnedDir(t), "seed.db")
	if err := f.shared.SeedCopy(seed); err != nil {
		t.Fatalf("SeedCopy: %v", err)
	}

	seedDB, err := db.OpenReadOnly(seed)
	if err != nil {
		t.Fatalf("open the seed copy: %v", err)
	}
	defer func() { _ = seedDB.Close() }()

	if got := probeCountRows(t, seedDB); got != historyRows {
		t.Errorf("the seed copy holds %d rows, want every one of the %d", got, historyRows)
	}

	// The machine-local file holds the token this enable was handed. It must not
	// be in the copy: the copy is what leaves this machine.
	names, err := seedDB.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames on the seed copy: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("the seed copy holds secret rows %v, want none", names)
	}

	// The copy is refused at a path that already exists, so a second seed over
	// the first is not a silent overwrite.
	if err := f.shared.SeedCopy(seed); err == nil {
		t.Error("a second SeedCopy over the first succeeded, want a refusal")
	}
}

// TestLiveOpensRefuseABareFile is the destructive-wiring test for the opens that
// name the live file after an enable: each must refuse a file the driver has
// never joined rather than making it a member.
//
// The guard lives in the one place that reaches the driver, so this drives that
// guard rather than the injected opener: an injected fake stands in for the
// driver entirely, so it cannot show what the real open does to a file it was
// handed. What is asserted here is that the guard refuses, and that it refuses
// without having touched the file -- the ordering is the whole safety, because a
// refusal made after the driver opened the path would come after the rewrite.
func TestLiveOpensRefuseABareFile(t *testing.T) {
	f := newProbeFixture(t)
	before, err := probeHash(f.livePath)
	if err != nil {
		t.Fatalf("hash the live file: %v", err)
	}

	// A bare file: real history, and none of the driver's marker tables. This is
	// exactly what the live file is before any enable joins it, so it is what
	// every post-enable open would name on a machine that never enabled.
	err = relevosync.RequireSyncMember(f.livePath)
	if !errors.Is(err, relevosync.ErrNotSynced) {
		t.Fatalf("the live file of an unjoined machine passes the membership guard: %v", err)
	}

	// The guard is what the audited opens run, so it is driven here rather than
	// only called: an open naming this file must be refused, and the refusal has
	// to arrive before anything opens it.
	_, oerr := relevosync.OpenRemote(context.Background(), relevosync.OpenConfig{
		Role:       relevosync.OpenMember,
		Path:       f.livePath,
		RemoteURL:  "libsql://relevo-test-org.turso.io",
		ClientName: "relevo",
	})
	if !errors.Is(oerr, relevosync.ErrNotSynced) {
		t.Errorf("a member open over a bare file = %v, want ErrNotSynced", oerr)
	}

	after, herr := probeHash(f.livePath)
	if herr != nil {
		t.Fatalf("hash the live file after the guard: %v", herr)
	}
	if before != after {
		t.Error("the membership guard changed the file it was asked about")
	}
	if got := probeCountRows(t, f.shared); got != historyRows {
		t.Errorf("the guard left %d rows, want %d", got, historyRows)
	}
}

// TestAnOpenWithNoRoleRefuses pins that an open which did not say whether it may
// create sync membership is refused rather than assumed safe. The zero value has
// to be the refusal, because a caller that forgot the question is exactly the
// caller that must not reach the driver with a live path.
func TestAnOpenWithNoRoleRefuses(t *testing.T) {
	path := filepath.Join(probeOwnedDir(t), "bare.db")
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })

	ok, err := relevosync.HasSyncMarker(path)
	if err != nil {
		t.Fatalf("HasSyncMarker on a bare file: %v", err)
	}
	if ok {
		t.Fatal("a file no driver opened reports a sync marker")
	}
	if err := relevosync.RequireSyncMember(path); !errors.Is(err, relevosync.ErrNotSynced) {
		t.Errorf("RequireSyncMember on a bare file = %v, want ErrNotSynced", err)
	}
	if err := relevosync.RequireSyncMember(""); !errors.Is(err, relevosync.ErrNotSynced) {
		t.Errorf("RequireSyncMember on an empty path = %v, want ErrNotSynced", err)
	}
}

// TestLivePathIsNamedOnlyByTheAllowlistedOpens pins where the live path may
// appear, by reading this file's own source rather than trusting a review to
// have looked.
//
// It is a grep rather than a behavioural assertion because the hazard is
// textual: a probe that named the live path broke this machine, and the fix is
// that no probe code path mentions it at all. The allow-list is the three
// production opens that are entitled to it, and a fourth appearance is the
// defect coming back under a new name.
func TestLivePathIsNamedOnlyByTheAllowlistedOpens(t *testing.T) {
	// Each entry is an open entitled to name the live file, with the role it
	// must be asking for. A probe is not among them: it names a throwaway.
	allow := map[string]bool{
		"opener":   true, // the enable's own open: the seed
		"seedPath": true, // where the seed copy lands, beside the live file
	}
	// cloudEmpty names v.Path only to place the throwaway beside it, and
	// probeConfig is the open that names the scratch file instead. Both are
	// allowed to mention the live path as a location, never as an open.
	allow["cloudEmpty"] = true
	allow["probeConfig"] = true
	// joined reads the live path's own marker tables to decide whether the
	// turn-off has anything to push. It opens nothing, so it is allowed the path.
	allow["joined"] = true
	// memberOpener reads the same marker tables to refuse an unjoined file
	// before any dial. It names v.Path only for that read; the open itself
	// goes through openRemote, which is covered by the opener allow-listing
	// in the role tests rather than here.
	allow["memberOpener"] = true
	// openConfig builds the member open the turn-off's final push drives.
	// It names the live path as the open itself, which is its whole purpose;
	// the role tests pin that it asks as a member and never for anything else.
	allow["openConfig"] = true
	// backfill walks the live file's own tables over the dedicated capture
	// connection, which is the one connection whose writes reach the change set.
	// It names v.Path as that walk's target, which is its whole purpose, and it
	// opens nothing: the sync package owns the connection.
	allow["backfill"] = true

	body := probeSource(t, "syncverb.go")
	var offenders []string
	for _, name := range probeFuncs(body) {
		if !strings.Contains(body, name) {
			continue
		}
		start := strings.Index(body, "func (v *VerbRunner) "+name+"(")
		if start < 0 {
			continue
		}
		end := probeFuncEnd(body, start)
		if !strings.Contains(body[start:end], "v.Path") {
			continue
		}
		if !allow[name] {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) != 0 {
		t.Errorf("these functions name the live path but are not allowlisted: %v", offenders)
	}

	// The probe must be built from the scratch path, never from the live one.
	probeStart := strings.Index(body, "func (v *VerbRunner) probeConfig(")
	if probeStart < 0 {
		t.Fatal("probeConfig is gone, so the probe has no separate open builder")
	}
	probeBody := body[probeStart:probeFuncEnd(body, probeStart)]
	if strings.Contains(probeBody, "Path: v.Path") || strings.Contains(probeBody, "Path:             v.Path") {
		t.Error("probeConfig names the live path as an open")
	}
	if !strings.Contains(probeBody, "Path:      scratch") {
		t.Errorf("probeConfig does not name the throwaway:\n%s", probeBody)
	}
}

// probeSource reads a file in this package, so the grep above reads the same
// bytes the compiler does.
func probeSource(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// probeFuncs is every method name on VerbRunner in this file's source.
func probeFuncs(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "func (v *VerbRunner) ") {
			continue
		}
		rest := strings.TrimPrefix(line, "func (v *VerbRunner) ")
		if i := strings.Index(rest, "("); i > 0 {
			out = append(out, rest[:i])
		}
	}
	return out
}

// probeFuncEnd is where the function starting at start ends: the next top-level
// declaration, or the end of the file.
func probeFuncEnd(body string, start int) int {
	rest := body[start:]
	if i := strings.Index(rest, "\nfunc "); i >= 0 {
		return start + i
	}
	return len(body)
}

// probeHash is the live file's content hash, which is what the data-loss
// assertions compare. The whole file is read rather than its metadata: the loss
// was a content change, and a size check alone would miss a same-size rewrite.
func probeHash(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(sum)*2)
	for _, b := range sum {
		out = append(out, hex[b>>4], hex[b&0x0f])
	}
	return string(out), nil
}

// probeCountRows is how many live binding rows the handle holds. It is the
// count a pull's rebase would roll back, so a loss shows here first.
//
// RecordCounts rather than a scoped list: the fixture's rows carry an origin
// stamped by the backfill, and a list is scoped to one handle's origin, which
// would report zero for a file whose rows are all present.
func probeCountRows(t *testing.T, d *db.DB) int {
	t.Helper()

	live, _, err := d.RecordCounts()
	if err != nil {
		t.Fatalf("RecordCounts: %v", err)
	}
	return live
}

// TestProbePullIsBounded pins that the emptiness probe carries a pull bound:
// it asks whether the remote holds anything, so one chunk answers it. An
// unbounded probe downloads the entire remote state to answer, which outlasts
// the verb timeout against any real-sized database.
func TestProbePullIsBounded(t *testing.T) {
	v := &VerbRunner{ClientName: "relevo"}
	cfg := v.probeConfig(relevosync.Settings{RemoteURL: "libsql://example.invalid"}, []byte("token"), t.TempDir())
	if cfg.PullBytesThreshold != relevosync.ProbePullBytes {
		t.Errorf("probe pull threshold = %d, want ProbePullBytes %d", cfg.PullBytesThreshold, relevosync.ProbePullBytes)
	}
	if cfg.PullBytesThreshold <= 0 {
		t.Error("probe pull threshold is not positive: the probe would pull unbounded")
	}
}

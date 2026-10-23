package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

const (
	handoverOrigin = "https://github.com/acme/webshop.git"
)

// handoverRuntime is a runtime whose one binding "webshop" has a branch and a
// recorded worktree, with sync on and a second installation, zen, that has
// written at the current schema.
func handoverRuntime(t *testing.T) (Runtime, *fakeGit) {
	t.Helper()
	fg := &fakeGit{repoFactsOrigin: handoverOrigin, branchExists: true}
	rt := newTestRuntime(t, fg)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	oldTree := t.TempDir()
	b.Branch, b.Worktree, b.CWD = "relevo/webshop", oldTree, oldTree
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.PutSettings(local, relevosync.Settings{Enabled: true, RemoteURL: "libsql://x"}, time.Now()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := d.Tx(func(tx *db.Tx) error { return tx.InstallationTouch("zen-id", "zen", time.Now()) }); err != nil {
		t.Fatalf("InstallationTouch: %v", err)
	}
	if err := d.NoteOriginSchema("zen-id", MinHandoverSchema); err != nil {
		t.Fatalf("NoteOriginSchema: %v", err)
	}
	return rt, fg
}

func TestHandoverPushesFlipsTheWriterAndRefusesLocalWork(t *testing.T) {
	rt, fg := handoverRuntime(t)
	ctx := context.Background()

	res, err := Handover(ctx, rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "origin"})
	if err != nil {
		t.Fatalf("Handover: %v", err)
	}
	if res.Target != "zen" || res.Already || len(fg.pushCalls) != 1 || fg.pushCalls[0].Branch != "relevo/webshop" || fg.pushCalls[0].Remote != "origin" {
		t.Fatalf("result %+v, pushes %+v, want one push of relevo/webshop to origin", res, fg.pushCalls)
	}

	_, err = rt.Store.Load("webshop")
	var gone *store.HandedOverError
	if !errors.Is(err, store.ErrHandedOver) || !errors.As(err, &gone) || gone.To != "zen-id" || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after the hand-over = %v, want the handed-over error naming zen-id and not not-found", err)
	}
	if _, err := Bind(ctx, rt, BindOptions{Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo"}); !errors.Is(err, store.ErrHandedOver) {
		t.Errorf("bind --resume on the old machine = %v, want the handed-over refusal", err)
	}
	if _, err := Bind(ctx, rt, BindOptions{Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo2"}); !errors.Is(err, store.ErrHandedOver) {
		t.Errorf("a fresh bind of the handed-over name = %v, want the handed-over refusal", err)
	}

	rep, err := Status(ctx, rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 0 || len(rep.HandedOver) != 1 || rep.HandedOver[0].Name != "webshop" || rep.HandedOver[0].To != "zen" || !rep.HandedOver[0].NotExported {
		t.Fatalf("status = %+v, want webshop listed as handed over to zen and not yet exported", rep)
	}

	again, err := Handover(ctx, rt, HandoverOptions{Name: "webshop", To: "zen-id", Remote: "origin"})
	if err != nil || !again.Already || len(fg.pushCalls) != 1 {
		t.Fatalf("rerun = (%+v, %v) with %d pushes, want Already and no second push", again, err, len(fg.pushCalls))
	}
	if _, err := Handover(ctx, rt, HandoverOptions{Name: "webshop", To: "someone-else", Remote: "origin"}); err == nil {
		t.Error("a hand-over to a second machine succeeded")
	}
}

func TestHandoverFailedPushChangesNothing(t *testing.T) {
	rt, fg := handoverRuntime(t)
	fg.pushErr = errors.New("remote rejected")
	if _, err := Handover(context.Background(), rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "origin"}); err == nil {
		t.Fatal("Handover succeeded with a rejected push")
	}
	if _, err := rt.Store.Load("webshop"); err != nil {
		t.Fatalf("a failed push left the binding unloadable: %v", err)
	}
	if left, _ := rt.Store.Left(); len(left) != 0 {
		t.Errorf("a failed push recorded %+v as left", left)
	}
}

func TestHandoverRefusesWithoutRunningAnyGit(t *testing.T) {
	rt, fg := handoverRuntime(t)
	fg.dirtyResult = true
	if _, err := Handover(context.Background(), rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "origin"}); err == nil {
		t.Fatal("Handover succeeded with a dirty tree")
	}
	if len(fg.pushCalls) != 0 {
		t.Errorf("a refused hand-over pushed: %+v", fg.pushCalls)
	}
}

// What a hand-over leaves on the target: the record is written by this machine
// and created by another. The first resume cuts this machine's own worktree,
// repoints the history mirror, and leaves the record under the resuming
// MasterMind: binding_record.owner is the serve tenant (empty on a local
// machine), so it never moved off the scope the target reads.
func TestResumeHandedOverCutsLocalWorktreeAndStaysVisibleToTheResumingMasterMind(t *testing.T) {
	rt, fg := handoverRuntime(t)
	d, _ := rt.Store.DB()
	// The history mirror of the binding, as the sync left it.
	if err := d.Tx(func(tx *db.Tx) error {
		_, err := tx.UpsertBinding(db.Binding{Name: "webshop", CWD: "/old", BuilderMode: "headless", CreatedAt: time.Now(), IngestSource: db.IngestLive})
		return err
	}); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	// The record was created by another installation and is written by this one.
	for _, table := range []string{"binding_record", "binding"} {
		if _, err := rawExec(d, `UPDATE `+table+` SET origin = 'creator-x'`); err != nil {
			t.Fatalf("stage %s: %v", table, err)
		}
	}
	got, err := Bind(context.Background(), rt, BindOptions{Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	wantTree := rt.Store.WorktreePath("webshop")
	if got.Worktree != wantTree || got.CWD != wantTree {
		t.Errorf("worktree/cwd = %q/%q, want this machine's %q", got.Worktree, got.CWD, wantTree)
	}
	if len(fg.fetchCalls) != 1 || fg.fetchCalls[0].Ref != "relevo/webshop" || fg.fetchCalls[0].Remote != "origin" {
		t.Errorf("fetches = %+v, want one of relevo/webshop from origin", fg.fetchCalls)
	}
	if len(fg.checkoutWorktreeCalls) != 1 || fg.checkoutWorktreeCalls[0].Path != wantTree {
		t.Errorf("checkouts = %+v, want the branch checked out at %s", fg.checkoutWorktreeCalls, wantTree)
	}

	raw, err := db.OpenRaw(d.Path())
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var mirrorURL string
	if err := raw.QueryRow(`SELECT r.origin_url FROM binding b JOIN repo r ON r.id = b.repo_id WHERE b.name = 'webshop'`).Scan(&mirrorURL); err != nil || mirrorURL != "https://github.com/acme/webshop" {
		t.Errorf("mirror repo = (%q, %v), want this machine's row for the normalised %s", mirrorURL, err, handoverOrigin)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 || rep.Bindings[0].Name != "webshop" || rep.Bindings[0].MasterMindName != testMasterMindName {
		t.Fatalf("status rows = %+v, want webshop under the resuming MasterMind %s", rep.Bindings, testMasterMindName)
	}
	recs, err := d.RecordList(rt.Store.Owner())
	if err != nil || len(recs) != 1 || recs[0].Owner != rt.Store.Owner() {
		t.Fatalf("records = (%+v, %v), want the one record still under owner %q", recs, err, rt.Store.Owner())
	}
}

func TestResumeHandedOverRefusesAnotherRepositoryAndAMissingBranch(t *testing.T) {
	rt, fg := handoverRuntime(t)
	d, _ := rt.Store.DB()
	for _, table := range []string{"binding_record", "binding"} {
		if _, err := rawExec(d, `UPDATE `+table+` SET origin = 'creator-x'`); err != nil {
			t.Fatalf("stage %s: %v", table, err)
		}
	}
	opts := BindOptions{Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo"}
	before, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	fg.repoFactsOrigin = "https://github.com/acme/other.git"
	_, err = Bind(context.Background(), rt, opts)
	if err == nil || !strings.Contains(err.Error(), "https://github.com/acme/other") || len(fg.fetchCalls) != 0 {
		t.Fatalf("resume from another repository = %v with %d fetches, want a refusal naming the origin and no fetch", err, len(fg.fetchCalls))
	}

	fg.repoFactsOrigin = handoverOrigin
	fg.fetchErr = errors.New("couldn't find remote ref")
	_, err = Bind(context.Background(), rt, opts)
	if err == nil || !strings.Contains(err.Error(), "ask the old machine to run relevo handover") {
		t.Fatalf("resume with no branch on origin = %v, want the ask-the-old-machine refusal", err)
	}
	if b, lerr := rt.Store.Load("webshop"); lerr != nil || b.Worktree != before.Worktree {
		t.Errorf("a refused resume rewrote the record: %+v, %v", b, lerr)
	}
}

// A record this machine holds but another writes is not resumable yet, and the
// refusal says to sync rather than to bind a new one.
func TestResumeNamesAHandOverThatHasNotArrivedYet(t *testing.T) {
	rt, _ := handoverRuntime(t)
	d, _ := rt.Store.DB()
	if _, err := rawExec(d, `UPDATE binding_record SET writer = 'old-id'`); err != nil {
		t.Fatalf("stage: %v", err)
	}
	_, err := Bind(context.Background(), rt, BindOptions{Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo"})
	if err == nil || !strings.Contains(err.Error(), "not handed to this machine yet") {
		t.Fatalf("resume = %v, want the run-relevo-db-sync refusal", err)
	}
}

// rawExec runs one statement on the file, the way another machine's import
// would have left it.
func rawExec(d *db.DB, stmt string) (int64, error) {
	raw, err := db.OpenRaw(d.Path())
	if err != nil {
		return 0, err
	}
	defer func() { _ = raw.Close() }()
	res, err := raw.Exec(stmt)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func TestHandoverRefusesAForeignRemoteBeforePushing(t *testing.T) {
	rt, fg := handoverRuntime(t)
	fg.remotes = []string{"origin", "mirror"}
	fg.remoteURLs = map[string]string{"origin": handoverOrigin, "mirror": "https://github.com/other/webshop.git"}
	_, err := Handover(context.Background(), rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "mirror"})
	if err == nil || !strings.Contains(err.Error(), "next:") {
		t.Fatalf("Handover = %v, want the foreign-remote refusal", err)
	}
	if len(fg.pushCalls) != 0 {
		t.Errorf("a refused hand-over pushed: %+v", fg.pushCalls)
	}
	if _, err := rt.Store.Load("webshop"); err != nil {
		t.Errorf("binding no longer loads: %v", err)
	}
}

func TestHandoverAcceptsAnotherNameForOriginsURL(t *testing.T) {
	rt, fg := handoverRuntime(t)
	fg.remotes = []string{"origin", "mirror"}
	fg.remoteURLs = map[string]string{"origin": handoverOrigin, "mirror": "git@github.com:acme/webshop.git"}
	if _, err := Handover(context.Background(), rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "mirror"}); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	if len(fg.pushCalls) != 1 || fg.pushCalls[0].Remote != "mirror" {
		t.Errorf("pushes = %+v, want one to mirror", fg.pushCalls)
	}
}

type urlCountingGit struct {
	*fakeGit
	urlReads int
}

func (g *urlCountingGit) RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	g.urlReads++
	return g.fakeGit.RemoteURL(ctx, dir, remote)
}

func TestHandoverToOriginReadsNoRemoteURL(t *testing.T) {
	rt, fg := handoverRuntime(t)
	cg := &urlCountingGit{fakeGit: fg}
	rt.Git = cg
	if _, err := Handover(context.Background(), rt, HandoverOptions{Name: "webshop", To: "zen", Remote: "origin"}); err != nil {
		t.Fatalf("Handover: %v", err)
	}
	if cg.urlReads != 0 {
		t.Errorf("RemoteURL called %d times for --remote origin, want 0", cg.urlReads)
	}
}

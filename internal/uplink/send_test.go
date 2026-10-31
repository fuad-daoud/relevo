package uplink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeGit is the git seam a send needs: branch resolution, the out-ref move
// and the tag list. It embeds the exported interface and overrides only what
// the ship reads.
type fakeGit struct {
	relevo.Git
	refSHA map[string]string
	tags   map[string]string
}

func (f *fakeGit) RefSHA(ctx context.Context, dir, ref string) (string, bool, error) {
	if f.refSHA != nil {
		sha, ok := f.refSHA[ref]
		return sha, ok, nil
	}
	return "fakerefsha", true, nil
}

func (f *fakeGit) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	if f.refSHA != nil {
		f.refSHA[ref] = newSHA
	}
	return nil
}

func (f *fakeGit) ListTags(ctx context.Context, dir string) (map[string]string, error) {
	return f.tags, nil
}

// fakeRemote records what a send asks the server and answers from canned
// views and errors, so a test names only the answers its case varies.
type fakeRemote struct {
	relevo.RemoteClient
	calls []string

	whoAmIResp          remote.WhoAmI
	whoAmIErr           error
	candidatesResp      remote.CandidatesResponse
	candidatesErr       error
	unavailableErr      error
	unavailableErrFor   func(server, name, token string) error
	availableResp       remote.AvailableResponse
	availableErr        error
	availableErrFor     func(server, subject string) error
	startRoundResp      remote.BindingView
	startRoundErr       error
	startRoundPlan      []byte
	startRoundTier      string
	startRoundCandidate string
	startRoundForce     bool
	startRoundTags      []remote.TagRef
	startRoundRetry     bool
	startRoundVerify    *bool
	getBindingResp      remote.BindingView
	roundFileFromResp   io.ReadCloser

	beforeCall func(call string)
}

func (f *fakeRemote) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	f.calls = append(f.calls, "WhoAmI:"+server)
	return f.whoAmIResp, f.whoAmIErr
}

// Candidates answers the server's candidate list from the canned response.
func (f *fakeRemote) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	f.calls = append(f.calls, "Candidates:"+server)
	return f.candidatesResp, f.candidatesErr
}

// Unavailable records one gated binding, answering per-binding when the test
// fails one binding and succeeds the rest.
func (f *fakeRemote) Unavailable(ctx context.Context, server, name, token, reason string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unavailable:%s:%s:%s", server, name, token))
	if f.unavailableErrFor != nil {
		return f.unavailableErrFor(server, name, token)
	}
	return f.unavailableErr
}

// Available answers the clear from the canned response.
func (f *fakeRemote) Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error) {
	f.calls = append(f.calls, fmt.Sprintf("Available:%s:%s", server, subject))
	if f.availableErrFor != nil {
		return remote.AvailableResponse{}, f.availableErrFor(server, subject)
	}
	return f.availableResp, f.availableErr
}

func (f *fakeRemote) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, force bool, tags []remote.TagRef, retryOnUnreachable bool, verify *bool) (remote.BindingView, error) {
	call := fmt.Sprintf("StartRound:%s:%s:%d", server, name, round)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	f.startRoundPlan = plan
	f.startRoundTier = tier
	f.startRoundCandidate = candidate
	f.startRoundForce = force
	f.startRoundTags = tags
	f.startRoundRetry = retryOnUnreachable
	f.startRoundVerify = verify
	return f.startRoundResp, f.startRoundErr
}

// fakeTransport answers the bundle snapshot from a canned snapshot, counting
// every call with its args so a test can pin since handling and retries.
type fakeTransport struct {
	remote.TreeTransport
	snapshotCalls    []snapshotCall
	snapshotResp     remote.Snapshot
	snapshotErr      error
	snapshotErrFirst error
}

type snapshotCall struct {
	Repo  string
	Refs  []string
	Since string
}

func (f *fakeTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (remote.Snapshot, error) {
	f.snapshotCalls = append(f.snapshotCalls, snapshotCall{Repo: repo, Refs: refs, Since: since})
	if f.snapshotErr != nil {
		return remote.Snapshot{}, f.snapshotErr
	}
	if f.snapshotErrFirst != nil && len(f.snapshotCalls) == 1 {
		return remote.Snapshot{}, f.snapshotErrFirst
	}
	return f.snapshotResp, nil
}

// sendDeps wires the hook under test to the real uplink functions, the same
// shape production wires in cmd/relevo and internal/serve.
func sendDeps() relevo.RemoteSendDeps {
	return relevo.RemoteSendDeps{Send: SendRemote, Ship: Ship, Record: Record}
}

// remoteBinding is a minimal active remote binding, round 1.
func remoteBinding(server string) store.Binding {
	return store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: server,
		},
	}
}

// remoteBuilderRT is the client runtime for the send --candidate tests: an
// active remote binding on zen with a current candidate, a fake git whose
// branch resolves, and a fake transport.
func remoteBuilderRT(t *testing.T, fr *fakeRemote) (relevo.Runtime, *store.Store, *fakeTransport) {
	t.Helper()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:             "api",
		CWD:              "/fake/repo",
		Repo:             "/fake/repo",
		Branch:           "relevo/api",
		Round:            1,
		State:            store.StateActive,
		BuilderCandidate: "agy/test/m",
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
			Kind:   "agy",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	return relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}, st, ft
}

// writePlan writes body to a temp file and returns its path.
func writePlan(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// baseTime is the fixed clock the resend test reads halt stamps against.
var baseTime = time.Unix(1757000000, 0).UTC()

// haltedRemoteBinding is a remote binding whose round halted: the server's
// answer is NEEDS YOU, and the client has recorded that as its own state.
func haltedRemoteBinding(server string) store.Binding {
	b := remoteBinding(server)
	b.State = store.StateNeedsYou
	b.Halt = "api: zen: builder exited without a report"
	b.HaltAt = baseTime.Add(-time.Minute)
	b.HaltNotifiedRound = b.Round
	b.RoundStartedAt = baseTime.Add(-time.Hour)
	b.StalledSince = baseTime.Add(-30 * time.Minute)
	return b
}

// reopenRemoteRuntime is the runtime a remote binding's send needs: the store,
// a fake git that resolves the branch, a fake transport whose snapshot is
// empty, and a fake server whose answers a test sets.
func reopenRemoteRuntime(t *testing.T, b store.Binding) (relevo.Runtime, *fakeRemote) {
	t.Helper()
	st := store.New(t.TempDir())
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/" + b.Branch: "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{}
	ft := &fakeTransport{snapshotResp: remote.Snapshot{Empty: true}}
	return relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: func() time.Time { return baseTime }, RemoteSend: sendDeps()}, fr
}

func TestSendRemoteRecordsOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable),
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if err == nil || !strings.Contains(err.Error(), "zen unreachable") {
		t.Fatalf("Send got %v, want zen unreachable", err)
	}

	// No plan file written
	if _, err := os.Stat(st.PromptPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("PromptPath exists after failure: %v", err)
	}

	// No log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}

	// Round unchanged
	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Round != 1 {
		t.Fatalf("reloaded.Round = %d, want 1", reloaded.Round)
	}
	if !reloaded.RoundStartedAt.IsZero() {
		t.Fatalf("reloaded.RoundStartedAt is not zero: %v", reloaded.RoundStartedAt)
	}
}

func TestSendRemoteRoundStartedIsSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    remote.CodeRoundStarted,
				Message: "already started",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	res, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("res.Round = %d, want 1", res.Round)
	}

	// Plan file written
	if _, err := os.Stat(st.PromptPath("api", 1)); err != nil {
		t.Fatalf("PromptPath missing: %v", err)
	}

	// Log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

// TestSendRemoteHaltedIsAnError pins the client-side refusal: a StartRound
// response that says the round could not start -- whether an old server's
// 201 with a needs_you view, or a new server's 409 round_halted -- is
// reported as an error, and nothing is written locally: a resend must not
// look like it succeeded.
func TestSendRemoteHaltedIsAnError(t *testing.T) {
	t.Parallel()

	t.Run("201 needs_you view (old server)", func(t *testing.T) {
		t.Parallel()
		haltedOldServerView(t)
	})
	t.Run("409 round_halted (new server)", func(t *testing.T) {
		t.Parallel()
		haltedRoundHalted(t)
	})
	t.Run("200 broken view (identical re-send)", func(t *testing.T) {
		t.Parallel()
		haltedBrokenView(t)
	})
	t.Run("409 round_open on a broken binding", func(t *testing.T) {
		t.Parallel()
		haltedRoundOpenOnBroken(t)
	})
}

// haltedSendRuntime is the runtime for the halted-send cases: an active
// remote binding with a shipped head the refused send must leave alone.
func haltedSendRuntime(t *testing.T, fr *fakeRemote) (relevo.Runtime, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "0000000000000000000000000000000000000000",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	return relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}, st
}

// assertNothingWritten pins the refusal's silence: the error names the
// refusal, and no plan file, log entry or binding change is left behind.
func assertNothingWritten(t *testing.T, st *store.Store, err error, wantSubstrs ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("Send succeeded, want an error")
	}
	for _, want := range wantSubstrs {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Send error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if _, statErr := os.Stat(st.PromptPath("api", 1)); !os.IsNotExist(statErr) {
		t.Errorf("PromptPath exists after failure: stat err = %v", statErr)
	}
	entries, _ := st.ReadLog("api")
	if len(entries) != 0 {
		t.Errorf("entries = %d, want 0", len(entries))
	}
	reloaded, loadErr := st.Load("api")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if reloaded.State != store.StateActive {
		t.Errorf("State = %s, want unchanged active", reloaded.State)
	}
	if reloaded.Builder.LastShipped != "0000000000000000000000000000000000000000" {
		t.Errorf("LastShipped = %q, want unchanged", reloaded.Builder.LastShipped)
	}
}

func haltedOldServerView(t *testing.T) {
	t.Helper()
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundNeedsYou,
			Halt:       "builder spawn failed: boom",
		},
	}
	rt, st := haltedSendRuntime(t, fr)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{})
	assertNothingWritten(t, st, err, "could not start", "boom")
}

func haltedRoundHalted(t *testing.T) {
	t.Helper()
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    remote.CodeRoundHalted,
				Message: "already switched 2 time(s) this round (max_switches 2)",
			},
		},
	}
	rt, st := haltedSendRuntime(t, fr)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{})
	assertNothingWritten(t, st, err, "could not start", "already switched 2 time(s)")
}

// haltedBrokenView pins that a 200 carrying a broken view reads as the
// refusal it is: a server holds a broken round as an open one, so an
// identical re-send is the same no-op it is for a running round. The round
// did not start, so recording a prompt for it and resetting the round would
// file the human's next halt a second time with the same text.
func haltedBrokenView(t *testing.T) {
	t.Helper()
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundBroken,
			Halt:       "round is broken; rebind before sending",
		},
	}
	rt, st := haltedSendRuntime(t, fr)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{})
	assertNothingWritten(t, st, err, "could not start", "is broken; rebind before sending")
}

// haltedRoundOpenOnBroken pins that a 409 round_open on a broken binding
// reports the server's reason, not a claim that the round is running.
func haltedRoundOpenOnBroken(t *testing.T) {
	t.Helper()
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    remote.CodeRoundOpen,
				Message: `binding "api" is broken; rebind before sending`,
			},
		},
	}
	rt, st := haltedSendRuntime(t, fr)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{})
	assertNothingWritten(t, st, err, "could not start", "is broken; rebind before sending")
	if strings.Contains(err.Error(), "is running") {
		t.Errorf("Send error = %q, want no claim that the round is running", err.Error())
	}
}

// TestSendRemoteTierPassedToStartRound pins that a Send with a Tier option
// reaches StartRound over the wire, after the pre-tier probe succeeds.
func TestSendRemoteTierPassedToStartRound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Tier: "edit"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if fr.startRoundTier != "edit" {
		t.Fatalf("startRoundTier = %q, want edit", fr.startRoundTier)
	}
}

// TestSendRemoteRefusesASymlinkedPlanFile pins that a remote send does not
// follow a symlink planted at the round's plan path: the server has already
// accepted the round, but the write is refused before the plan is logged and
// the binding saved, so the local log stays empty and the stored binding is
// unchanged.
func TestSendRemoteRefusesASymlinkedPlanFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt, st, before, sentinel := symlinkedPlanFixture(t)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Tier: "edit"})
	if err == nil || !strings.Contains(err.Error(), "write plan") {
		t.Fatalf("Send = %v, want an error containing write plan", err)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("ReadFile(sentinel): %v", err)
	}
	if string(got) != "sentinel" {
		t.Errorf("sentinel = %q, want it byte-identical", got)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("log = %+v, want it empty", entries)
	}
	after, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("binding = %+v, want it unchanged (%+v)", after, before)
	}
}

// symlinkedPlanFixture stages the trap: the round's plan path is a symlink to
// a sentinel file, so the send's stage must refuse before logging or saving.
// It returns the runtime, the store, the binding as saved, and the sentinel.
func symlinkedPlanFixture(t *testing.T) (relevo.Runtime, *store.Store, store.Binding, string) {
	t.Helper()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	before, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, st.PromptPath("api", 1)); err != nil {
		t.Fatal(err)
	}
	return rt, st, before, sentinel
}

// TestSendRemoteForcePassedToStartRound pins that a Send with Force reaches
// StartRound over the wire, after the force feature probe succeeds.
func TestSendRemoteForcePassedToStartRound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureForce}},
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Force: true}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if !fr.startRoundForce {
		t.Fatalf("startRoundForce = %v, want true", fr.startRoundForce)
	}
}

// TestSendRemoteForceRefusedWithoutTheFeature pins the force gate: a forced
// send to a server without the force feature is refused before the bundle is
// shipped, naming the missing feature.
func TestSendRemoteForceRefusedWithoutTheFeature(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Force: true})
	if err == nil {
		t.Fatal("Send --force against a pre-force server must be refused")
	}
	if !strings.Contains(err.Error(), "pre-force server") {
		t.Errorf("err = %q, want the pre-force refusal", err.Error())
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "StartRound:") {
			t.Errorf("StartRound was called: %v", fr.calls)
		}
	}
	if len(ft.snapshotCalls) != 0 {
		t.Errorf("a snapshot was taken before the refusal: %+v", ft.snapshotCalls)
	}
	if _, statErr := os.Stat(st.PromptPath("api", 1)); !os.IsNotExist(statErr) {
		t.Errorf("a plan was staged: %v", statErr)
	}
}

// TestSendRemoteStartRoundRetryFollowsTheFeature pins the retry flag: the
// StartRound retry is asked for exactly when WhoAmI advertises
// idempotent_send, and never otherwise.
func TestSendRemoteStartRoundRetryFollowsTheFeature(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		features []string
		want     bool
	}{
		{"idempotent_send advertised", []string{remote.FeatureIdempotentSend}, true},
		{"feature absent", []string{remote.FeatureQueue}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(t.TempDir())
			b := store.Binding{
				Name:   "api",
				CWD:    "/fake/repo",
				Repo:   "/fake/repo",
				Branch: "relevo/api",
				Round:  1,
				State:  store.StateActive,
				Builder: store.Endpoint{
					Mode:        store.ModeRemote,
					Server:      "zen",
					LastShipped: "0000000000000000000000000000000000000000",
				},
			}
			if err := st.Save(b); err != nil {
				t.Fatal(err)
			}

			fg := &fakeGit{
				refSHA: map[string]string{
					"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
				},
			}
			fr := &fakeRemote{
				whoAmIResp:     remote.WhoAmI{Features: tc.features},
				startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
			}
			ft := &fakeTransport{
				snapshotResp: remote.Snapshot{
					Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
				},
			}
			rt := relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}

			planFile := writePlan(t, "# Plan")
			if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if fr.startRoundRetry != tc.want {
				t.Fatalf("StartRound retry = %v, want %v (features %v)", fr.startRoundRetry, tc.want, tc.features)
			}
		})
	}
}

// TestSendRemoteTierAboveMaxWraps pins that a 422 tier_above_max from
// StartRound maps back to resolve.ErrTierAboveMax client-side, matching the local
// refusal's wording.
func TestSendRemoteTierAboveMaxWraps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 422,
			Body: remote.ErrorBody{
				Code:    remote.CodeTierAboveMax,
				Message: "tier yolo exceeds this server's max_tier edit",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if !errors.Is(err, resolve.ErrTierAboveMax) {
		t.Fatalf("Send err = %v, want resolve.ErrTierAboveMax", err)
	}
}

// TestSendRemoteSnapshotDeadlineRetriesOnce pins the snapshot retry: a `git
// bundle create` that outruns the per-command budget keeps
// context.DeadlineExceeded all the way out of Snapshot, and the ship answers
// that transient miss with exactly one retry -- succeeding when the second
// attempt fits, and still failing the same way (never looping) when it does
// not.
func TestSendRemoteSnapshotDeadlineRetriesOnce(t *testing.T) {
	t.Parallel()

	sendFixture := func(t *testing.T, ft *fakeTransport) (relevo.Runtime, string) {
		t.Helper()
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		fg := &fakeGit{
			refSHA: map[string]string{
				"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
			},
		}
		fr := &fakeRemote{
			startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
		}
		rt := relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}

		planFile := writePlan(t, "# Plan")
		return rt, planFile
	}

	deadline := fmt.Errorf("git bundle create /tmp/x.bundle: %w", context.DeadlineExceeded)

	t.Run("second attempt succeeds", func(t *testing.T) {
		t.Parallel()
		ft := &fakeTransport{
			snapshotErrFirst: deadline,
			snapshotResp: remote.Snapshot{
				Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
			},
		}
		rt, planFile := sendFixture(t, ft)

		if _, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{}); err != nil {
			t.Fatalf("Send: %v, want the deadline retried", err)
		}
		if len(ft.snapshotCalls) != 2 {
			t.Fatalf("snapshotCalls = %d, want 2 (one failure, one retry)", len(ft.snapshotCalls))
		}
	})

	t.Run("second attempt fails too", func(t *testing.T) {
		t.Parallel()
		ft := &fakeTransport{snapshotErr: deadline}
		rt, planFile := sendFixture(t, ft)

		_, err := relevo.Send(context.Background(), rt, "api", planFile, relevo.SendOptions{})
		if err == nil || !strings.Contains(err.Error(), "snapshot refs/relevo/api/out:") {
			t.Fatalf("Send err = %v, want the snapshot <ref>: context deadline exceeded failure", err)
		}
		if len(ft.snapshotCalls) != 2 {
			t.Fatalf("snapshotCalls = %d, want 2: one retry, never a loop", len(ft.snapshotCalls))
		}
	})
}

func TestSendRemoteFirstSendFullBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "", // first send
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(ft.snapshotCalls) != 1 {
		t.Fatalf("snapshotCalls = %d, want 1", len(ft.snapshotCalls))
	}
	if ft.snapshotCalls[0].Since != "" {
		t.Fatalf("snapshot Since = %q, want empty string for first send", ft.snapshotCalls[0].Since)
	}
}

func TestSendRemoteShipsTags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
		tags: map[string]string{
			"v1": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"v0": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}

	planFile := writePlan(t, "# Plan")

	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	want := []remote.TagRef{
		{Name: "v0", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "v1", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if !slices.Equal(fr.startRoundTags, want) {
		t.Fatalf("StartRound tags = %+v, want %+v (sorted by name)", fr.startRoundTags, want)
	}
}

func TestSendRemoteSetsLastShipped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	outSHA := "3333333333333333333333333333333333333333"
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": outSHA},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Builder.LastShipped != outSHA {
		t.Fatalf("reloaded LastShipped = %q, want %q", reloaded.Builder.LastShipped, outSHA)
	}
}

// TestSendRemoteBuilderChangesCandidate pins the builder change on send: the
// token reaches StartRound, the client records the candidate the server
// canonicalised, and a pick entry lands under the sent round before its plan
// entry. The observation half stays in the engine, beside observeRemote.
func TestSendRemoteBuilderChangesCandidate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	view := remote.BindingView{RoundState: remote.RoundRunning, Candidate: "opencode/test/m"}
	fr := &fakeRemote{
		whoAmIResp:        remote.WhoAmI{Features: []string{remote.FeatureTier, remote.FeatureBuilder}},
		startRoundResp:    view,
		getBindingResp:    view,
		roundFileFromResp: io.NopCloser(strings.NewReader("")),
	}
	rt, st, _ := remoteBuilderRT(t, fr)

	planFile := writePlan(t, "# Plan")

	res, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Builder: "opencode/test/m"})
	if err != nil {
		t.Fatalf("Send --builder: %v", err)
	}
	if fr.startRoundCandidate != "opencode/test/m" {
		t.Errorf("fake saw candidate %q, want opencode/test/m", fr.startRoundCandidate)
	}
	if !strings.Contains(res.Pick, "picked opencode/test/m on zen: explicit") {
		t.Errorf("res.Pick = %q, want the remote pick note", res.Pick)
	}

	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.BuilderCandidate != "opencode/test/m" {
		t.Errorf("BuilderCandidate = %q, want opencode/test/m", stored.BuilderCandidate)
	}
	if stored.Builder.Kind != "opencode" {
		t.Errorf("Builder.Kind = %q, want opencode", stored.Builder.Kind)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	planIdx := -1
	for i, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPrompt {
			planIdx = i
			break
		}
	}
	if planIdx < 1 {
		t.Fatalf("no plan entry after a pick entry: %+v", entries)
	}
	if e := entries[planIdx-1]; e.Kind != store.KindPick || e.Round != 1 || !strings.Contains(e.Note, "opencode/test/m") {
		t.Errorf("entry before the plan = %+v, want a round 1 pick naming opencode/test/m", e)
	}
}

// TestSendRemoteBuilderPreBuilderServerRefused pins the builder gate: a server
// without the builder feature is refused before anything is shipped.
func TestSendRemoteBuilderPreBuilderServerRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	rt, st, ft := remoteBuilderRT(t, fr)

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Builder: "opencode/test/m"})
	if err == nil {
		t.Fatal("Send --builder against a pre-builder server must be refused")
	}
	if !strings.Contains(err.Error(), "cannot change a binding's candidate") {
		t.Errorf("err = %q, want the pre-builder refusal", err.Error())
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "StartRound:") {
			t.Errorf("StartRound was called: %v", fr.calls)
		}
	}
	if len(ft.snapshotCalls) != 0 {
		t.Errorf("a snapshot was taken before the refusal: %+v", ft.snapshotCalls)
	}
	if _, statErr := os.Stat(st.PromptPath("api", 1)); !os.IsNotExist(statErr) {
		t.Errorf("a plan was staged: %v", statErr)
	}
}

// TestSendRemoteBuilderRefusedWhileRoundOpen pins the open-round guard: the
// client's own open round refuses before any server contact.
func TestSendRemoteBuilderRefusedWhileRoundOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier, remote.FeatureBuilder}},
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	rt, st, _ := remoteBuilderRT(t, fr)
	if err := st.AppendLog("api", store.LogEntry{
		TS: time.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Builder: "opencode/test/m"})
	if err == nil {
		t.Fatal("Send --builder on an open round must be refused")
	}
	if !strings.Contains(err.Error(), "has round 1 open") || !strings.Contains(err.Error(), "relevo stop api ends it") {
		t.Errorf("err = %q, want the round-open refusal", err.Error())
	}
	var open *relevo.CandidateRoundOpenError
	if !errors.As(err, &open) || open.Name != "api" {
		t.Errorf("err = %#v, want a *relevo.CandidateRoundOpenError for api", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("the refusal contacted the server: %v", fr.calls)
	}
}

// TestSendRemoteBuilderRepointsAHaltedRound pins the client half of the
// re-point: a served binding halted on its open round may be re-pointed by
// --candidate. The token reaches StartRound, the server's canonical candidate
// is recorded, and the local copy keeps round 1, clears its halt and goes
// active. Nothing is staged before the server answers.
func TestSendRemoteBuilderRepointsAHaltedRound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	view := remote.BindingView{RoundState: remote.RoundRunning, Candidate: "opencode/test/m"}
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier, remote.FeatureBuilder}},
		startRoundResp: view,
	}
	rt, st, _ := remoteBuilderRT(t, fr)

	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	stored.State = store.StateNeedsYou
	stored.Halt = "builder agy/test/m (exited (code 1) without a report); already switched"
	stored.HaltAt = time.Now().UTC()
	if err := st.Save(stored); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{
		TS: time.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	// The plan may not be on disk while the server is being asked: the client
	// records the server's answer first.
	stagedAtStart := false
	fr.beforeCall = func(call string) {
		if !strings.HasPrefix(call, "StartRound:") {
			return
		}
		if _, statErr := os.Stat(st.PromptPath("api", 1)); statErr == nil {
			stagedAtStart = true
		}
	}

	planFile := writePlan(t, "# Plan 2")

	res, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Builder: "opencode/test/m"})
	if err != nil {
		t.Fatalf("Send --candidate on a halted served round: %v", err)
	}
	if fr.startRoundCandidate != "opencode/test/m" {
		t.Errorf("fake saw candidate %q, want opencode/test/m", fr.startRoundCandidate)
	}
	if stagedAtStart {
		t.Error("a plan was staged before the server answered")
	}
	if res.Round != 1 {
		t.Errorf("res.Round = %d, want 1", res.Round)
	}

	after, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if after.Round != 1 {
		t.Errorf("Round = %d, want 1 (the round is re-pointed, not advanced)", after.Round)
	}
	if after.BuilderCandidate != "opencode/test/m" {
		t.Errorf("BuilderCandidate = %q, want the served candidate opencode/test/m", after.BuilderCandidate)
	}
	if after.State != store.StateActive {
		t.Errorf("State = %q, want active", after.State)
	}
	if after.Halt != "" {
		t.Errorf("Halt = %q, want cleared", after.Halt)
	}
}

func TestRemoteResendOverNeedsYouClearsTheHalt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := haltedRemoteBinding("zen")
	rt, fr := reopenRemoteRuntime(t, b)
	fr.startRoundResp = remote.BindingView{RoundState: remote.RoundRunning}

	planFile := writePlan(t, "# Plan")

	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("Send over NEEDS YOU: %v", err)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want %q: a resend that started a round left the binding %q", got.State, store.StateActive, got.State)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty", got.Halt)
	}
	if !got.HaltAt.IsZero() {
		t.Errorf("HaltAt = %v, want zero", got.HaltAt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
	}
	if !got.StalledSince.IsZero() {
		t.Errorf("StalledSince = %v, want zero", got.StalledSince)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want nil", got.RoundExcluded)
	}
	if got.RoundOOMKills != 0 {
		t.Errorf("RoundOOMKills = %d, want 0", got.RoundOOMKills)
	}
	if !got.StopRequestedAt.IsZero() {
		t.Errorf("StopRequestedAt = %v, want zero", got.StopRequestedAt)
	}
	if got.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
	}
	if !got.FinishPending {
		t.Error("FinishPending = false, want true after a resend opens the round")
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !relevo.HasPromptEntry(entries, b.Round) {
		t.Errorf("no prompt entry for round %d: the resend left no open round: %+v", b.Round, entries)
	}
}

// TestShipRefusesAPreTierServer pins the tier gate: a tiered send to a server
// that does not advertise the tier feature is refused before anything ships.
func TestShipRefusesAPreTierServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now, RemoteSend: sendDeps()}

	_, _, _, err := Ship(ctx, rt, b, []byte("# Plan"), "edit", "", false, nil)
	if !errors.Is(err, relevo.ErrServerPreTier) {
		t.Fatalf("Ship err = %v, want ErrServerPreTier", err)
	}
	if len(ft.snapshotCalls) != 0 {
		t.Errorf("a snapshot was taken before the refusal: %+v", ft.snapshotCalls)
	}
	if len(fr.calls) != 1 {
		t.Errorf("calls = %v, want only the WhoAmI probe", fr.calls)
	}
}

// TestRecordRefusesARoundThatAdvanced pins the under-lock re-check: a binding
// whose round moved between the ship and the record writes nothing and
// reports the advance.
func TestRecordRefusesARoundThatAdvanced(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	b.Round = 2
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	rt := relevo.Runtime{Store: st, Now: time.Now, RemoteSend: sendDeps()}
	view := remote.BindingView{RoundState: remote.RoundRunning}

	var sendRound int
	err := st.WithLock(func(tx *store.Tx) error {
		r, _, rerr := Record(rt, tx, remoteBinding("zen"), view, "refs/relevo/api/out", nil, []byte("# Plan"), "", false)
		sendRound = r
		return rerr
	})
	if err == nil || !strings.Contains(err.Error(), "round advanced during send") {
		t.Fatalf("Record err = %v, want the round-advanced refusal", err)
	}
	if sendRound != 0 {
		t.Errorf("sendRound = %d, want 0 on refusal", sendRound)
	}
	if _, statErr := os.Stat(st.PromptPath("api", 1)); !os.IsNotExist(statErr) {
		t.Errorf("a plan was staged for a round that moved on: %v", statErr)
	}
}

// TestRecordResetsTheRoundForAChainMember pins the chain record half: the
// shipped facts land, the prompt entry carries the chain step note, and the
// halt the round carried goes with the attempt.
func TestRecordResetsTheRoundForAChainMember(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := haltedRemoteBinding("zen")
	b.OwedHalt = &store.OwedHalt{Round: 1, Text: "round 1 needs a hand"}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(db.ChainRow{
			ID: "ch_test", Name: "shop", Status: string(chain.StatusRunning),
			Phase: "build", Step: "build", Plan: 1, Plans: 1, Builder: "api",
		})
	}); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}
	rt := relevo.Runtime{Store: st, Now: func() time.Time { return baseTime }, RemoteSend: sendDeps()}
	view := remote.BindingView{RoundState: remote.RoundRunning}
	heads := map[string]string{"refs/relevo/api/out": "3333333333333333333333333333333333333333"}

	var sendRound int
	err := st.WithLock(func(tx *store.Tx) error {
		r, _, rerr := Record(rt, tx, b, view, "refs/relevo/api/out", heads, []byte("# Plan"), "", true)
		sendRound = r
		return rerr
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if sendRound != 1 {
		t.Fatalf("sendRound = %d, want 1", sendRound)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !promptNoteFor(entries, 1, "chain builder") {
		t.Errorf("log = %+v, want the prompt entry with the chain step note", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.LastShipped != "3333333333333333333333333333333333333333" {
		t.Errorf("LastShipped = %q, want the shipped head", got.Builder.LastShipped)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Errorf("RemoteStatus = %q, want running", got.Builder.RemoteStatus)
	}
	if got.State != store.StateActive || got.Halt != "" {
		t.Errorf("State/Halt = %q/%q, want the halt gone with the attempt", got.State, got.Halt)
	}
	if got.OwedHalt != nil {
		t.Errorf("OwedHalt = %+v, want nil: the re-send answers the halt the marker is about", got.OwedHalt)
	}
}

// promptNoteFor reports whether entries hold a to-builder prompt entry for
// round carrying note.
func promptNoteFor(entries []store.LogEntry, round int, note string) bool {
	for _, e := range entries {
		if e.Direction == store.DirToBuilder && e.Round == round && e.Note == note {
			return true
		}
	}
	return false
}

// tierUnsupportedWire is the 422 invalid answer a server's launch render
// produces for a harness-tier refusal.
func tierUnsupportedWire() *client.HTTPError {
	return &client.HTTPError{
		Status: 422,
		Body: remote.ErrorBody{
			Code:    remote.CodeInvalid,
			Message: "tier not supported by harness: claude cannot run a builder at tier edit: headless acceptEdits denies go, make and git commit, so the builder could not run the gate or commit; use --tier yolo (--dangerously-skip-permissions) or --tier harness (claude's settings.json decides)",
		},
	}
}

// TestSendRemoteTierUnsupportedWraps pins that a 422 invalid carrying the
// harness-tier refusal from StartRound maps back to ErrTierUnsupported,
// modelled on TestSendRemoteTierAboveMaxWraps.
func TestSendRemoteTierUnsupportedWraps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{startRoundErr: tierUnsupportedWire()}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("Send err = %v, want ErrTierUnsupported", err)
	}
}

// TestSendRemoteInvalidWithoutTierStaysGeneric pins the mapping's guard: a
// 422 invalid that carries no harness-tier refusal is not typed as one.
func TestSendRemoteInvalidWithoutTierStaysGeneric(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 422,
			Body: remote.ErrorBody{
				Code:    remote.CodeInvalid,
				Message: "send --candidate bogus: unknown candidate",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := relevo.Runtime{
		Store:      st,
		Git:        fg,
		Remote:     fr,
		Transport:  ft,
		Now:        time.Now,
		RemoteSend: sendDeps(),
	}

	planFile := writePlan(t, "# Plan")

	_, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{})
	if err == nil {
		t.Fatal("Send = nil, want the generic 422 refusal")
	}
	if errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("Send err = %v, want no ErrTierUnsupported for a non-tier refusal", err)
	}
	if !strings.Contains(err.Error(), "unknown candidate") {
		t.Fatalf("Send err = %v, want the server message carried through", err)
	}
}

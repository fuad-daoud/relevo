package relevo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// baseTime is the instant newRuntime's fixed clock reports.
var baseTime = time.Unix(1757000000, 0).UTC()

// The mastermind record newRuntime's registry holds. The id and the name are
// both tested, so both are named constants; --mastermind accepts either (§4.3),
// and the id shape is the one mastermind.ValidID accepts.
const (
	testMasterMindID   = "pl_aaaaaaaaaaaa"
	testMasterMindName = "architect-1"
)

// newRuntime builds a Runtime for tests: a temp store, the test candidate
// set, a fake runner and a fixed clock. No harness is faked: a local builder
// is a process relevo runs, and the tests drive it through fakeRunner.
//
// MasterMinds is a registry on a t.TempDir() holding exactly one record
// (testMasterMindID/testMasterMindName), so every verb that resolves a mastermind
// finds it by --mastermind name. ProcStart fails, so mastermind.Resolve's host step
// can never match and resolution stays on the flag step, which is the one
// these tests drive.
func newRuntime(t *testing.T) Runtime {
	t.Helper()
	reg, _ := testMasterMindRegistry(t, mastermind.Record{
		ID:          testMasterMindID,
		Name:        testMasterMindName,
		HarnessKind: "claude",
		SessionID:   "sess-architect",
		CWD:         "/repo",
	})
	gates := testGates(t)
	return Runtime{
		Store:      store.New(t.TempDir()),
		Candidates: candidateSet(t, testCandidatesJSON),
		Gates:      gates,
		Latency:    gates,
		Now:        func() time.Time { return baseTime },
		// Every local builder is headless since #303, so every Send needs a
		// Runner. A test that wants "no runner" sets rt.Runner = nil.
		Runner: newFakeRunner(),
		// The one seeded mastermind, and a ProcStart that always fails so the
		// host step of Resolve is inert.
		MasterMinds: reg,
		ProcStart:   func(int) (int64, error) { return 0, errors.New("no proc start in tests") },
	}
}

// newTestRuntime is newRuntime with a test's Git: a local builder is headless
// (#303), so there is no pane dependency left to thread through, and fg may be
// nil for the tests that prove --cwd needs no git.
func newTestRuntime(t *testing.T, fg *fakeGit) Runtime {
	t.Helper()
	rt := newRuntime(t)
	if fg != nil {
		rt.Git = fg
	}
	rt.Hooks = nil
	return rt
}

// runtimeWithMasterMind is newRuntime with its one registry record replaced, for
// the tests that need a mastermind whose session id, kind or transcript locator
// a case names. An empty locator leaves MasterMind.TranscriptLocator for
// mastermindLocator (rt.Sessions) to fill, exactly as a real record without one
// would.
func runtimeWithMasterMind(t *testing.T, sessionID, locator string) Runtime {
	t.Helper()
	rt := newRuntime(t)
	reg, _ := testMasterMindRegistry(t, mastermind.Record{
		ID:                testMasterMindID,
		Name:              testMasterMindName,
		HarnessKind:       "claude",
		SessionID:         sessionID,
		CWD:               "/repo",
		TranscriptLocator: locator,
	})
	rt.MasterMinds = reg
	return rt
}

// testMasterMindRegistry seeds a registry holding rec and returns it with the
// record as the registry stamped it (created_at and seen_at filled in).
func testMasterMindRegistry(t *testing.T, rec mastermind.Record) (*mastermind.DBRegistry, mastermind.Record) {
	t.Helper()
	reg := testMasterMinds(t)
	reg.Now = func() time.Time { return baseTime }
	created, err := reg.Create(rec)
	if err != nil {
		t.Fatalf("create mastermind record: %v", err)
	}
	return reg, created
}

// TestBindRecordsRepoFeatureAndLocator pins #172: a fresh bind captures the
// git repo identity (normalised), the human-given --feature label, and the
// mastermind's own transcript file path (via rt.Sessions), and stamps CreatedAt.
// TestBindRepoFactsFailureIsNil pins that a git failure never fails a bind:
// captureRepo swallows it and RepoRef stays nil.
func TestBindRecordsRepoFeatureAndLocator(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{
		repoFactsOrigin:    "git@github.com:o/r.git",
		repoFactsCommonDir: "/repo/.git",
	}
	rt.Sessions = func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "sess-architect" {
			return "/home/x/.claude/projects/slug/S.jsonl", true
		}
		return "", false
	}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		CWD: "/repo", Feature: "auth",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(b.RepoRef, want) {
		t.Errorf("RepoRef = %+v, want %+v", b.RepoRef, want)
	}
	if b.Feature != "auth" {
		t.Errorf("Feature = %q, want auth", b.Feature)
	}
	if b.MasterMind.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("MasterMind.TranscriptLocator = %q, want the resolved session path", b.MasterMind.TranscriptLocator)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt must be stamped at bind")
	}
}

func TestBindRepoFactsFailureIsNil(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{repoFactsErr: errors.New("not a git repository")}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind must tolerate a git failure, got %v", err)
	}
	if b.RepoRef != nil {
		t.Errorf("RepoRef = %+v, want nil", b.RepoRef)
	}
}

// TestBindRecordsMasterMindFromRegistry is the plan's required case (§3.2,
// §5.3): with a registry configured, Binding.MasterMindID, MasterMind.Kind and
// MasterMind.SessionID come from the record. #303 deleted the pane the caller
// used to pass, so MasterMind.PaneID is no longer written by anything (see the
// field's own comment); the assertion on it is gone with the pane.
func TestBindRecordsMasterMindFromRegistry(t *testing.T) {
	t.Parallel()

	rt := runtimeWithMasterMind(t, "sess-from-record", "/home/x/.claude/projects/slug/S.jsonl")

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:         "webshop",
		Candidate:    testOpencodeRef,
		MasterMindID: testMasterMindID,
		CWD:          "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if b.MasterMindID != testMasterMindID {
		t.Errorf("MasterMindID = %q, want the record's %q", b.MasterMindID, testMasterMindID)
	}
	if b.MasterMind.Kind != "claude" {
		t.Errorf("MasterMind.Kind = %q, want the record's claude", b.MasterMind.Kind)
	}
	if b.MasterMind.SessionID != "sess-from-record" {
		t.Errorf("MasterMind.SessionID = %q, want the record's sess-from-record", b.MasterMind.SessionID)
	}
	if b.MasterMind.PaneID != "" {
		t.Errorf("MasterMind.PaneID = %q, want empty: nothing writes a pane id since #303", b.MasterMind.PaneID)
	}
	if b.MasterMind.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("MasterMind.TranscriptLocator = %q, want the record's", b.MasterMind.TranscriptLocator)
	}
}

// TestBindNoMasterMindIsHardError is the plan's required case for §4.3: with a
// registry configured and nothing resolving -- no --mastermind, no
// $RELEVO_MASTERMIND, no host and no detectable session -- a verb fails with
// exactly the CLI's no-mastermind line. The old "no mastermind pane" error is gone.
func TestBindNoMasterMindIsHardError(t *testing.T) {
	t.Setenv("RELEVO_MASTERMIND", "")
	t.Setenv("CLAUDECODE", "")

	rt := newRuntime(t)
	rt.MasterMinds = testMasterMinds(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, CWD: "/repo",
	})
	if err == nil {
		t.Fatal("Bind with no resolvable mastermind must fail")
	}
	if err.Error() != ErrNoMasterMindSession.Error() {
		t.Errorf("err = %q, want exactly %q", err.Error(), ErrNoMasterMindSession.Error())
	}
}

// TestBindRejectsBadFeature pins that a bad --feature is refused before
// anything is spawned, with the same error store.ValidFeature reports.
func TestBindRejectsBadFeature(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo", Feature: "a/b",
	})
	if err == nil || !strings.Contains(err.Error(), "feature:") {
		t.Fatalf("Bind err = %v, want one containing %q", err, "feature:")
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a rejected feature must spawn no process, got %d", got)
	}
}

// TestBindRefusesAnOverlongBuilderName pins #64: a 25-character binding
// name passes the store's own limit but builds a 33-character agent name, and
// Bind must refuse it before anything is started or any binding saved. #303
// deleted the pane client's ErrInvalidAgentName with it, so the wrapped
// error is store.ValidName's own text.
func TestBindRefusesAnOverlongBuilderName(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused name must start no process, got %d", got)
	}
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Bind err = %v, want the agent-name length refusal", err)
	}
	if _, loadErr := rt.Store.Load(name); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a refused name saves no binding", loadErr)
	}
}

// slicesContains reports whether want is one of args.
func slicesContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// launchArgs renders the headless launch for b's builder candidate and tier,
// the way Send would, so a test can assert the argv a bind resolved to.
func launchArgs(t *testing.T, rt Runtime, b store.Binding, tier harness.Tier) []string {
	t.Helper()
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		t.Fatalf("builder candidate %q: %v", b.BuilderCandidate, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		t.Fatalf("lookup %q: %v", ref, err)
	}
	role, _ := harness.RoleByName("builder")
	argv, err := spawn.HeadlessLaunch(c, role, tier, 0, "", b.CWD, rt.Store.Dir(b.Name))
	if err != nil {
		t.Fatalf("headlessLaunch: %v", err)
	}
	return argv
}

func TestBindRefusesACandidateThatDoesNotServeBuilder(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[{"harness":"claude","provider":"test","model":"m","roles":["reviewer"]}]`)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testClaudeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})

	if !errors.Is(err, ErrRoleNotServed) {
		t.Fatalf("want ErrRoleNotServed, got %v", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused bind must spawn nothing, got %d processes", got)
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("a refused bind must save no binding, Load = %v", loadErr)
	}
}

func TestBindSpawnUnknownAliasFails(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "claude/test/nope", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Fatalf("got %v, want ErrUnknownCandidate", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("an unknown alias must not start a process, got %d", got)
	}
}

func TestBindResolvesTheOnlyBuilderCandidate(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--x"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if !b.Builder.Headless() || b.Builder.Kind != "agy" {
		t.Errorf("Builder = %+v, want a headless agy endpoint", b.Builder)
	}
	argv := launchArgs(t, rt, b, harness.TierHarness)
	for _, want := range []string{"--model", "m", "--agent", "plan-executor", "--x"} {
		if !containsArg(argv, want, "") && !slicesContains(argv, want) {
			t.Errorf("launch args = %v, want them to carry %q", argv, want)
		}
	}
	if b.BuilderCandidate != "agy/test/m" {
		t.Errorf("BuilderCandidate = %q, want agy/test/m", b.BuilderCandidate)
	}
}

func TestBindRefusesAnAmbiguousCandidate(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, ErrAmbiguousCandidate) {
		t.Fatalf("want ErrAmbiguousCandidate, got %v", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused bind must spawn nothing, got %d processes", got)
	}
}

func TestBindWithNoCandidatesSaysSo(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, "[]")

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("want ErrNoCandidates, got %v", err)
	}
}

func TestResumeWithoutABuilderDoesNotSpawn(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "agy/test/m", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("seed Bind: %v", err)
	}
	fr := runnerOf(t, rt)
	fr.specs = nil

	_, err = Bind(context.Background(), rt, BindOptions{
		Name: b.Name, Resume: true, Candidate: "", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("resume without candidate must not spawn, got specs = %+v", fr.specs)
	}
}

func TestBindRefusesSecondBindingOnSameTree(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	opts := BindOptions{Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo"}
	if _, err := Bind(context.Background(), rt, opts); err != nil {
		t.Fatalf("first Bind: %v", err)
	}

	opts.Name = "webshop2"
	_, err := Bind(context.Background(), rt, opts)
	if !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestBindResumeRepointsMasterMindAndKeepsRound(t *testing.T) {
	t.Parallel()

	rt := runtimeWithMasterMind(t, "mastermind-sess-2", "")
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	b.Round = 5
	b.State = store.StateBroken
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.Round != 5 {
		t.Errorf("resume must keep the round, got %d", got.Round)
	}
	if got.MasterMind.SessionID != "mastermind-sess-2" {
		t.Errorf("resume must repoint the mastermind, got %+v", got.MasterMind)
	}
	if got.MasterMindID != testMasterMindID {
		t.Errorf("MasterMindID = %q, want the record's %q", got.MasterMindID, testMasterMindID)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

// TestResumeKeepsFieldsAndRefreshesLocator pins the resume rule for #172's
// new fields: a mastermind-only resume leaves RepoRef and Feature exactly as
// the binding already had them, and refreshes MasterMind.TranscriptLocator
// (which endpointOf wipes along with the rest of the old MasterMind endpoint)
// since it was previously empty.
func TestResumeKeepsFieldsAndRefreshesLocator(t *testing.T) {
	t.Parallel()

	rt := runtimeWithMasterMind(t, "mastermind-sess", "")

	existing := store.Binding{
		Name:       "webshop",
		CWD:        "/repo",
		Round:      3,
		State:      store.StateBroken,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old-sess"},
		Builder:    store.Endpoint{AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless},
		RepoRef:    &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"},
		Feature:    "auth",
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	rt.Sessions = func(kind, sessionID string) (string, bool) {
		if kind == "claude" && sessionID == "mastermind-sess" {
			return "/home/x/.claude/projects/slug/S.jsonl", true
		}
		return "", false
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}

	wantRepoRef := &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	if !reflect.DeepEqual(got.RepoRef, wantRepoRef) {
		t.Errorf("RepoRef = %+v, want kept as %+v", got.RepoRef, wantRepoRef)
	}
	if got.Feature != "auth" {
		t.Errorf("Feature = %q, want kept as auth", got.Feature)
	}
	if got.MasterMind.TranscriptLocator != "/home/x/.claude/projects/slug/S.jsonl" {
		t.Errorf("MasterMind.TranscriptLocator = %q, want refreshed to the resolved session path", got.MasterMind.TranscriptLocator)
	}
}

// TestResumeKeepsExistingLocatorWhenAlreadySet is the other half of the
// refresh rule: when the binding already has a TranscriptLocator, resume
// must not overwrite it with whatever rt.Sessions resolves for the new
// mastermind pane's session.
func TestResumeKeepsExistingLocatorWhenAlreadySet(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	existing := store.Binding{
		Name:       "webshop",
		CWD:        "/repo",
		Round:      3,
		State:      store.StateBroken,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect", TranscriptLocator: "/already/set.jsonl"},
		Builder:    store.Endpoint{AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	rt.Sessions = func(kind, sessionID string) (string, bool) {
		return "/would/overwrite.jsonl", true
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume Bind: %v", err)
	}
	if got.MasterMind.TranscriptLocator != "/already/set.jsonl" {
		t.Errorf("MasterMind.TranscriptLocator = %q, want the existing value kept, not re-resolved", got.MasterMind.TranscriptLocator)
	}
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"webshop":    "webshop",
		"money/ai":   "money-ai",
		"My.Repo":    "my-repo",
		"2024-thing": "b2024-thing",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBindRefusesExistingName is the regression test for a silently broken
// second session: Save only rewrites bind.json, so the previous session's
// log.jsonl and NNN-*.md files survive and a fresh round 1 collides with the
// old round 1. Reconcile then reads the old report entry as "already handled"
// and the binding stalls with no error and no notification.
// TestBindResumeStillAdoptsAnExistingName guards the exit the refusal offers.
// TestResumePausedRestoresAndRebinds pins #137: resuming a PAUSED binding
// restores the released worktree and rebinds a fresh builder even though the
// caller passed neither --rebind nor --candidate, because a paused binding has
// no builder identity left to keep.
func TestBindRefusesExistingName(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless, AgentName: "webshop-builder"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err == nil {
		t.Fatal("binding an existing name must be refused")
	}

	// The refusal has to happen before anything is started, or it strands a
	// live builder process with nothing pointing at it.
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("no process may be started, got %d", got)
	}

	if !strings.Contains(err.Error(), "relevo unbind webshop") {
		t.Errorf("error must name the unbind exit, got %q", err)
	}
	if !strings.Contains(err.Error(), "--resume") {
		t.Errorf("error must name the resume exit, got %q", err)
	}

	// The existing binding must be untouched by the refusal.
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Round != 4 || got.Builder.AgentName != "webshop-builder" {
		t.Errorf("refused bind must not rewrite the binding, got %+v", got)
	}
}

func TestBindResumeStillAdoptsAnExistingName(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	existing := store.Binding{
		Name: "webshop", CWD: "/repo", Round: 4, State: store.StateBroken,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{AgentName: "webshop-builder", Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("resume must still adopt an existing binding: %v", err)
	}
	if got.Round != 4 || got.State != store.StateActive {
		t.Errorf("resume = %+v", got)
	}
	if got.MasterMind.SessionID != "sess-architect" {
		t.Errorf("resume must repoint the mastermind, got %+v", got.MasterMind)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("resume must not start a process, got %d", got)
	}
}

func TestBindRebindWithGoneBuilder(t *testing.T) {
	t.Parallel()

	existing := store.Binding{
		Name:              "webshop",
		CWD:               "/repo",
		Round:             5,
		RoundBaselineTree: "tree-abc",
		State:             store.StateBroken,
		HaltNotifiedRound: 5,
		Halt:              "round 5 has run past 24h0m0s",
		HaltAt:            baseTime,
		BuilderScreen:     "some terminal output",
		BuilderScreenAt:   baseTime,
		MasterMind:        store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:           store.Endpoint{Mode: store.ModeHeadless, Kind: "opencode", AgentName: "webshop-builder"},
		BuilderCandidate:  testOpencodeRef,
	}

	t.Run("spawn replacement builder", func(t *testing.T) {
		rt := newRuntime(t)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("rebind: %v", err)
		}

		if !got.Builder.Headless() || got.Builder.Kind != "opencode" {
			t.Errorf("Builder = %+v, want a headless opencode endpoint", got.Builder)
		}
		if got.BuilderCandidate != testOpencodeRef {
			t.Errorf("BuilderCandidate = %q, want %s", got.BuilderCandidate, testOpencodeRef)
		}
		if got.BuilderScreen != "" {
			t.Errorf("BuilderScreen = %q, want empty", got.BuilderScreen)
		}
		if !got.BuilderScreenAt.IsZero() {
			t.Errorf("BuilderScreenAt = %v, want zero", got.BuilderScreenAt)
		}
		if got.HaltNotifiedRound != 0 {
			t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
		}
		if got.Halt != "" {
			t.Errorf("Halt = %q, want empty", got.Halt)
		}
		if !got.HaltAt.IsZero() {
			t.Errorf("HaltAt = %v, want zero", got.HaltAt)
		}
		if got.State != store.StateActive {
			t.Errorf("State = %s, want active", got.State)
		}
		if got.Round != 5 {
			t.Errorf("Round = %d, want 5 (untouched)", got.Round)
		}
		if got.CWD != "/repo" {
			t.Errorf("CWD = %q, want /repo (untouched)", got.CWD)
		}
		if got.RoundBaselineTree != "tree-abc" {
			t.Errorf("RoundBaselineTree = %q, want tree-abc (untouched)", got.RoundBaselineTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !saved.Builder.Headless() ||
			saved.BuilderScreen != "" || !saved.BuilderScreenAt.IsZero() ||
			saved.HaltNotifiedRound != 0 || saved.Halt != "" || !saved.HaltAt.IsZero() ||
			saved.Round != 5 || saved.RoundBaselineTree != "tree-abc" {
			t.Errorf("saved binding does not reflect rebind updates: %+v", saved)
		}
	})

}

func TestBindResumeDoneBindingScope(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	existing := store.Binding{
		Name:       "webshop",
		CWD:        "/repo",
		Round:      4,
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless, AgentName: "webshop-builder", Kind: "opencode"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	t.Run("mastermind-only resume of DONE binding succeeds", func(t *testing.T) {
		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("mastermind-only resume on done binding must succeed: %v", err)
		}
		if got.State != store.StateActive {
			t.Errorf("state = %s, want active", got.State)
		}
		if got.MasterMind.SessionID != "sess-architect" {
			t.Errorf("MasterMind.SessionID = %q, want the record's", got.MasterMind.SessionID)
		}
		if !reflect.DeepEqual(got.Builder, existing.Builder) {
			t.Errorf("Builder = %+v, want %+v (untouched)", got.Builder, existing.Builder)
		}
		if got := len(runnerOf(t, rt).specs); got != 0 {
			t.Errorf("no process may be started, got %d", got)
		}
	})

	t.Run("rebind of DONE binding is refused", func(t *testing.T) {
		// Reset state to Done for this subtest
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("reset existing binding: %v", err)
		}
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
		})
		if err == nil {
			t.Fatal("rebind on done binding must be refused")
		}
		wantMsg := `binding "webshop" is done: ` + "`relevo bind` to start fresh"
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want containing %q", err.Error(), wantMsg)
		}
		if got := len(runnerOf(t, rt).specs); got != 0 {
			t.Errorf("no process may be started, got %d", got)
		}
	})
}

func TestResumeRestoresMissingWorktree(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	wantCall := checkoutWorktreeCall{Dir: "/repo", Path: wt, Branch: "relevo/webshop"}
	if fg.checkoutWorktreeCalls[0] != wantCall {
		t.Errorf("checkoutWorktreeCall = %+v, want %+v", fg.checkoutWorktreeCalls[0], wantCall)
	}
	if res.RestoredWorktree != wt {
		t.Errorf("RestoredWorktree = %q, want %q", res.RestoredWorktree, wt)
	}
	if res.RestoredBranch != "relevo/webshop" {
		t.Errorf("RestoredBranch = %q, want relevo/webshop", res.RestoredBranch)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateActive {
		t.Errorf("loaded.State = %s, want active", loaded.State)
	}
}

// TestResumePausedRestoresAndRebinds pins #137: resuming a PAUSED binding
// restores the released worktree and rebinds a fresh builder even though the
// caller passed neither --rebind nor --candidate, because a paused binding has
// no builder identity left to keep.
func TestResumePausedRestoresAndRebinds(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo
	rt.Policy = orderOf("builder", testAgyRef)

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		Round:      4,
		State:      store.StatePaused,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Kind: "agy", Mode: store.ModeHeadless}, // pause cleared the identity
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if !res.WasPaused {
		t.Error("WasPaused = false, want true")
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	if !got.Builder.Headless() {
		t.Fatalf("Builder = %+v, want a headless rebuild on resume of a PAUSED binding", got.Builder)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
	if got.Round != 4 {
		t.Errorf("Round = %d, want 4 (unchanged)", got.Round)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != store.KindResume {
		t.Errorf("last log kind = %s, want resume", last.Kind)
	}
	if last.Note != "resumed" {
		t.Errorf("last log note = %q, want resumed", last.Note)
	}
}

func TestResumeRestoreHeadlessHasNoOrphan(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo
	rt.Runner = newFakeRunner()

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Errorf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
}

func TestResumeRefusesRestoreWithoutBranch(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "",
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "no branch is recorded") {
		t.Fatalf("err = %v, want containing 'no branch is recorded'", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("loaded.State = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeRefusesRestoreFromWrongRepo(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{} // branchExists stays false: the caller's cwd has no relevo/webshop
	rt.Git = fg

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt,
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/elsewhere",
	})
	if err == nil || !strings.Contains(err.Error(), "run resume from the repository") {
		t.Fatalf("err = %v, want 'run resume from the repository'", err)
	}
	if fg.lastBranchDir != "/elsewhere" || fg.lastBranchName != "relevo/webshop" {
		t.Errorf("BranchExists asked (%q, %q), want (/elsewhere, relevo/webshop)", fg.lastBranchDir, fg.lastBranchName)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != store.StateDone {
		t.Errorf("state = %s, want done (unchanged)", loaded.State)
	}
}

func TestResumeSurfacesBranchCheckedOut(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{checkoutWorktreeErr: git.ErrBranchCheckedOut}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "git worktree list") {
		t.Fatalf("err = %v, want containing 'git worktree list'", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused resume must start no process, got %d", got)
	}
}

func TestResumePresentWorktreeIsNotRestored(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo

	wt := t.TempDir()
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		State:      store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatal(err)
	}

	_, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 0 {
		t.Errorf("checkoutWorktreeCalls = %d, want 0", len(fg.checkoutWorktreeCalls))
	}
	if res.RestoredWorktree != "" {
		t.Errorf("RestoredWorktree = %q, want empty", res.RestoredWorktree)
	}
}

func TestRebindOnDoneWithRestoredWorktree(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fg := &fakeGit{}
	rt.Git = fg
	fg.branchExists = true // relevo/webshop lives in the caller's repo

	wt := filepath.Join(t.TempDir(), "gone")
	existing := store.Binding{
		Name:       "webshop",
		CWD:        wt, // an add binding's CWD is its worktree
		Worktree:   wt,
		Branch:     "relevo/webshop",
		Round:      4,
		State:      store.StateDone,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:    store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, _, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if len(fg.checkoutWorktreeCalls) != 1 {
		t.Fatalf("checkoutWorktreeCalls = %d, want 1", len(fg.checkoutWorktreeCalls))
	}
	if got.State != store.StateActive {
		t.Errorf("State = %s, want active", got.State)
	}
	if !got.Builder.Headless() {
		t.Errorf("Builder = %+v, want a headless endpoint", got.Builder)
	}
}

func TestBindRebindNotFound(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got err = %v, want store.ErrNotFound", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("no process may be started, got %d", got)
	}
}

func TestBindTimeoutOverrideAndDefault(t *testing.T) {
	t.Parallel()

	t.Run("override is stored", func(t *testing.T) {
		rt := newRuntime(t)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
			RoundTimeout: 90 * time.Minute,
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		if got, want := b.RoundTimeoutMS, int((90 * time.Minute).Milliseconds()); got != want {
			t.Errorf("RoundTimeoutMS = %d, want %d", got, want)
		}
	})

	t.Run("default is a day, not half an hour", func(t *testing.T) {
		rt := newRuntime(t)

		b, err := Bind(context.Background(), rt, BindOptions{
			Name: "kobe", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo2",
		})
		if err != nil {
			t.Fatalf("Bind: %v", err)
		}
		// A builder working a real stage runs for hours; 30m flagged healthy
		// work as needing a human on the first live run.
		if got, want := b.RoundTimeoutMS, int((24 * time.Hour).Milliseconds()); got != want {
			t.Errorf("default RoundTimeoutMS = %d, want %d (24h)", got, want)
		}
	})
}

func TestUnbindTeardown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("ordinary binding unbinds with all-zero result", func(t *testing.T) {
		rt := newRuntime(t)
		b := store.Binding{
			Name: "webshop", CWD: "/repo", State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "webshop", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.Archived || res.WorktreeRemoved != "" || res.WorktreeKept != "" || res.KeptReason != "" {
			t.Errorf("expected all-zero result for ordinary binding unbind, got %+v", res)
		}
		if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding state still exists after Unbind: %v", err)
		}
	})

	t.Run("clean worktree is removed", func(t *testing.T) {
		fg := &fakeGit{}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-clean", CWD: wt,
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-clean", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if res.WorktreeKept != "" {
			t.Errorf("WorktreeKept = %q, want empty", res.WorktreeKept)
		}
		if len(fg.removeWorktreeCalls) != 1 {
			t.Fatalf("RemoveWorktree calls = %d, want 1", len(fg.removeWorktreeCalls))
		}
		if fg.removeWorktreeCalls[0].Force {
			t.Error("teardown must pass force: false")
		}
		if _, err := rt.Store.Load("fork-clean"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should be removed")
		}
	})

	t.Run("dirty worktree is kept and says why", func(t *testing.T) {
		fg := &fakeGit{dirtyResult: true}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty", CWD: wt,
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != "" {
			t.Errorf("WorktreeRemoved = %q, want empty", res.WorktreeRemoved)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "uncommitted changes" {
			t.Errorf("KeptReason = %q, want 'uncommitted changes'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Error("RemoveWorktree must NOT be called for dirty tree")
		}
		if _, err := rt.Store.Load("fork-dirty"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("dirty check error keeps worktree with honest reason", func(t *testing.T) {
		fg := &fakeGit{dirtyErr: errors.New("git lock busy\ndetails")}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-dirty-err", CWD: wt,
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-dirty-err", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "dirty check failed: git lock busy" {
			t.Errorf("KeptReason = %q, want 'dirty check failed: git lock busy'", res.KeptReason)
		}
		if len(fg.removeWorktreeCalls) != 0 {
			t.Errorf("RemoveWorktree should not be called on dirty check error, got %d calls", len(fg.removeWorktreeCalls))
		}
	})

	t.Run("git unavailable keeps worktree", func(t *testing.T) {
		rt := newRuntime(t)
		rt.Git = nil

		b := store.Binding{
			Name: "fork-nogit", CWD: "/state/.worktrees/fork-nogit",
			Worktree: "/state/.worktrees/fork-nogit", State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-nogit", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != "/state/.worktrees/fork-nogit" || res.KeptReason != "git unavailable" {
			t.Errorf("kept mismatch: %+v", res)
		}
		if _, err := rt.Store.Load("fork-nogit"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})

	t.Run("git remove failure keeps worktree and completes unbind", func(t *testing.T) {
		fg := &fakeGit{removeWorktreeErr: errors.New("git lock locked\ndetails")}
		rt := newRuntime(t)
		rt.Git = fg

		wt := t.TempDir()
		b := store.Binding{
			Name: "fork-fail", CWD: wt,
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-fail", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeKept != wt {
			t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
		}
		if res.KeptReason != "git lock locked" {
			t.Errorf("KeptReason = %q, want 'git lock locked' (brief)", res.KeptReason)
		}
		if _, err := rt.Store.Load("fork-fail"); !errors.Is(err, store.ErrNotFound) {
			t.Error("binding state should still be deleted")
		}
	})
}

func TestUnbindReportsAnAlreadyGoneWorktree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fg := &fakeGit{}
	rt := newRuntime(t)
	rt.Git = fg

	missingWT := filepath.Join(t.TempDir(), "already-gone-worktree")
	b := store.Binding{
		Name: "fork-gone", CWD: "/repo",
		Worktree: missingWT, State: store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Unbind(ctx, rt, "fork-gone", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if res.WorktreeGone != missingWT {
		t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, missingWT)
	}
	if res.WorktreeKept != "" || res.WorktreeRemoved != "" {
		t.Errorf("kept=%q removed=%q, want both empty", res.WorktreeKept, res.WorktreeRemoved)
	}
	if fg.dirtyCalls != 0 {
		t.Errorf("dirtyCalls = %d, want 0", fg.dirtyCalls)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("removeWorktreeCalls = %d, want 0", len(fg.removeWorktreeCalls))
	}
	if _, err := rt.Store.Load("fork-gone"); !errors.Is(err, store.ErrNotFound) {
		t.Error("binding state should still be deleted")
	}
}

// worktreeDeletingGit is fakeGit whose RemoveWorktree also deletes the tree,
// the way real git does, so a test can watch the parent .worktrees/ go once
// its last tree is gone.
type worktreeDeletingGit struct {
	*fakeGit
}

func (f *worktreeDeletingGit) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	if err := f.fakeGit.RemoveWorktree(ctx, dir, path, force); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// TestUnbindTeardownPrunesWorktreeDirs: once the last worktree under
// .worktrees/ is gone the parents go too, so a finished binding leaves no
// empty .worktrees/ behind. The fake deletes the tree the way git does.
// Mutation: drop the prune call and .worktrees survives.
func TestUnbindTeardownPrunesWorktreeDirs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("last worktree gone removes .worktrees", func(t *testing.T) {
		fg := &worktreeDeletingGit{fakeGit: &fakeGit{}}
		rt := newRuntime(t)
		rt.Git = fg

		wt := rt.Store.WorktreePath("fork-only")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		b := store.Binding{
			Name: "fork-only", CWD: t.TempDir(),
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-only", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if _, err := os.Stat(rt.Store.WorktreeDir()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("WorktreeDir %s still exists after the last teardown: %v", rt.Store.WorktreeDir(), err)
		}
	})

	t.Run("a sibling worktree keeps .worktrees", func(t *testing.T) {
		fg := &worktreeDeletingGit{fakeGit: &fakeGit{}}
		rt := newRuntime(t)
		rt.Git = fg

		sibling := rt.Store.WorktreePath("sibling")
		if err := os.MkdirAll(sibling, 0o755); err != nil {
			t.Fatal(err)
		}
		wt := rt.Store.WorktreePath("fork-among")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		b := store.Binding{
			Name: "fork-among", CWD: t.TempDir(),
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-among", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeRemoved != wt {
			t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
		}
		if _, err := os.Stat(rt.Store.WorktreeDir()); err != nil {
			t.Errorf("WorktreeDir %s was removed though a sibling worktree remains: %v", rt.Store.WorktreeDir(), err)
		}
		if _, err := os.Stat(sibling); err != nil {
			t.Errorf("sibling worktree %s was touched: %v", sibling, err)
		}
	})

	t.Run("an already-gone worktree prunes too", func(t *testing.T) {
		fg := &worktreeDeletingGit{fakeGit: &fakeGit{}}
		rt := newRuntime(t)
		rt.Git = fg

		if err := os.MkdirAll(rt.Store.WorktreeDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		wt := rt.Store.WorktreePath("fork-gone")
		b := store.Binding{
			Name: "fork-gone", CWD: t.TempDir(),
			Worktree: wt, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "s"}, Builder: store.Endpoint{Mode: store.ModeHeadless},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}

		res, err := Unbind(ctx, rt, "fork-gone", false)
		if err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		if res.WorktreeGone != wt {
			t.Errorf("WorktreeGone = %q, want %q", res.WorktreeGone, wt)
		}
		if _, err := os.Stat(rt.Store.WorktreeDir()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("WorktreeDir %s still exists after the Gone teardown: %v", rt.Store.WorktreeDir(), err)
		}
	})
}

// TestResumeRebindClearsRoundClosedTree: a rebind replaces the builder, so a
// tree that changed hands says nothing about the new one and RoundClosedTree
// is cleared.
func TestResumeRebindClearsRoundClosedTree(t *testing.T) {
	t.Parallel()

	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		RoundClosedTree:  "tree-closed-123",
		State:            store.StateActive,
		MasterMind:       store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless, Kind: "opencode"},
		BuilderCandidate: testOpencodeRef,
	}

	t.Run("with alias", func(t *testing.T) {
		rt := newRuntime(t)
		if err := rt.Store.Save(existing); err != nil {
			t.Fatalf("seed existing binding: %v", err)
		}

		got, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo",
		})
		if err != nil {
			t.Fatalf("Bind resume with alias: %v", err)
		}
		if got.RoundClosedTree != "" {
			t.Errorf("returned RoundClosedTree = %q, want empty", got.RoundClosedTree)
		}

		saved, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if saved.RoundClosedTree != "" {
			t.Errorf("loaded RoundClosedTree = %q, want empty", saved.RoundClosedTree)
		}
	})

}

func TestResumeMasterMindOnlyPreservesRoundClosedTree(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	const closedTree = "tree-closed-123"
	existing := store.Binding{
		Name:            "webshop",
		CWD:             "/repo",
		Round:           3,
		RoundClosedTree: closedTree,
		State:           store.StateBroken,
		MasterMind:      store.Endpoint{Kind: "claude", SessionID: "old"},
		Builder:         store.Endpoint{Mode: store.ModeHeadless, AgentName: "webshop-builder"},
	}
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed existing binding: %v", err)
	}

	got, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind resume mastermind only: %v", err)
	}
	if got.RoundClosedTree != closedTree {
		t.Errorf("returned RoundClosedTree = %q, want %q", got.RoundClosedTree, closedTree)
	}

	saved, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.RoundClosedTree != closedTree {
		t.Errorf("loaded RoundClosedTree = %q, want %q", saved.RoundClosedTree, closedTree)
	}
}

func TestResumeRebindResolvesThroughTheOrder(t *testing.T) {
	t.Parallel()

	// #92: a builder that halted between rounds is gone, no round is open,
	// and the mastermind wants a replacement without naming a token. Rebind
	// must walk policy.json order and the ledger exactly as create does,
	// and record the pick.
	existing := store.Binding{
		Name:             "webshop",
		CWD:              "/repo",
		Round:            3,
		State:            store.StateBroken,
		MasterMind:       store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless, Kind: "opencode", AgentName: "webshop-builder"},
		BuilderCandidate: testOpencodeRef,
	}
	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}

	if res.How != HowOrder || res.Position != 1 {
		t.Errorf("resolution = %+v, want order #1", res)
	}
	if got.BuilderCandidate != testAgyRef {
		t.Errorf("BuilderCandidate = %q, want the order's first, %s", got.BuilderCandidate, testAgyRef)
	}
	if !got.Builder.Headless() || got.Builder.Kind != "agy" || got.State != store.StateActive || got.Round != 3 {
		t.Errorf("binding = %+v, want a headless agy builder, active, still round 3", got)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var picks int
	for _, e := range entries {
		if e.Kind == store.KindPick && e.Round == 3 && e.Note == ExplainResolution("builder", res) {
			picks++
		}
	}
	if picks != 1 {
		t.Errorf("want exactly one pick entry for round 3 reading %q, got entries %+v", ExplainResolution("builder", res), entries)
	}
}

func TestBindHeadlessRecordsAnEndpointAndSpawnsNothing(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Fatalf("headless bind must start no process, got %d", got)
	}
	ep := b.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.AgentName != "webshop-builder" || ep.Kind != "opencode" {
		t.Errorf("AgentName/Kind = %q/%q, want webshop-builder/opencode", ep.AgentName, ep.Kind)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("spec §3.1 invariants broken: %+v", ep)
	}
	if b.BuilderCandidate != testOpencodeRef || res.Token() != testOpencodeRef {
		t.Errorf("candidate = %q / %q, want %q", b.BuilderCandidate, res.Token(), testOpencodeRef)
	}
	if b.Round != 1 || b.State != store.StateActive {
		t.Errorf("round/state = %d/%s, want 1/active", b.Round, b.State)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil || len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("a fresh binding logs its pick and nothing else: %+v (%v)", entries, err)
	}
	// The stored binding reads back headless too.
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// A binding's mode is fixed at creation. Rebinding a headless binding whose
// process is gone must produce another headless endpoint, not a pane (#119).
func TestBindResumeRebindKeepsAHeadlessBindingHeadless(t *testing.T) {
	t.Parallel()

	existing := store.Binding{
		Name:       "webshop",
		CWD:        "/repo",
		Round:      4,
		State:      store.StateBroken,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, false) // the old process is gone
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, res, err := BindResolved(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if len(fr.specs) != 0 {
		t.Fatalf("a headless rebind must start no process: specs=%+v", fr.specs)
	}
	ep := got.Builder
	if !ep.Headless() || ep.Mode != store.ModeHeadless {
		t.Errorf("Mode = %q, want headless", ep.Mode)
	}
	if ep.PaneID != "" || ep.SessionID != "" || ep.PID != 0 || ep.LogPath != "" || ep.StartedAt != 0 {
		t.Errorf("rebound endpoint must be a fresh headless endpoint with nothing running: %+v", ep)
	}
	if got.BuilderCandidate != testAgyRef || res.How != HowOrder {
		t.Errorf("candidate = %q (%+v), want the order's first, %s", got.BuilderCandidate, res, testAgyRef)
	}
	if got.State != store.StateActive || got.Round != 4 {
		t.Errorf("state/round = %s/%d, want active/4", got.State, got.Round)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || !stored.Builder.Headless() {
		t.Errorf("stored builder: %+v (%v)", stored.Builder, err)
	}
}

// The Runner is the only thing that can see a process, so a headless
// binding's liveness is its answer. A live process refuses the rebind (§4.3).
func TestBindResumeRebindRefusesALiveHeadlessProcess(t *testing.T) {
	t.Parallel()

	existing := store.Binding{
		Name:       "webshop",
		CWD:        "/repo",
		Round:      4,
		State:      store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-architect"},
		Builder: store.Endpoint{
			AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless,
			PID: 4321, StartedAt: 1_700_000_000, LogPath: "/state/webshop/004-builder.log",
		},
		BuilderCandidate: testOpencodeRef,
	}
	rt := newRuntime(t)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)
	fr := newFakeRunner()
	fr.script(4321, true) // still running
	rt.Runner = fr
	if err := rt.Store.Save(existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Resume: true, Rebind: true, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if !errors.Is(err, ErrBuilderAlive) {
		t.Fatalf("err = %v, want ErrBuilderAlive", err)
	}
	if len(fr.specs) != 0 {
		t.Errorf("a refused rebind must start no process: specs=%+v", fr.specs)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil || stored.Builder.PID != 4321 || !stored.Builder.Headless() {
		t.Errorf("a refused rebind must leave the binding untouched: %+v (%v)", stored.Builder, err)
	}
}

func TestBindHeadlessStillRefusesAnOverlongName(t *testing.T) {
	t.Parallel()

	// The agent name is validated even though no agent is started:
	// the name is what status, log and a later pane-mode rebind identify
	// the builder by, and the limit must not depend on the mode.
	rt := newRuntime(t)
	name := "abcdefghij1234567890abcde" // 25 chars; + "-builder" = 33
	_, err := Bind(context.Background(), rt, BindOptions{
		Name: name, Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	})
	if err == nil || !strings.Contains(err.Error(), "builder agent name") {
		t.Fatalf("err = %v, want the agent-name refusal", err)
	}
}

func TestBindWithTierEditOnClaude(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:         "webshop",
		Candidate:    testClaudeRef,
		MasterMindID: testMasterMindName,
		CWD:          "/repo",
		Tier:         "edit",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "edit" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "edit")
	}
	gotArgs := launchArgs(t, rt, b, harness.TierEdit)
	for _, want := range []string{"--model", "m", "--agent", "plan-executor", "--permission-mode", "acceptEdits"} {
		if !slicesContains(gotArgs, want) {
			t.Errorf("expected %q in the launch args, got %v", want, gotArgs)
		}
	}
}

func TestBindWithTierYoloWithoutAllowYoloRefused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:         "webshop",
		Candidate:    testClaudeRef,
		MasterMindID: testMasterMindName,
		CWD:          "/repo",
		Tier:         "yolo",
	})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("err = %v, want ErrTierAboveMax", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused tier must start no process, got %d", got)
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindOpencodeCandidateTierReadRefused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name:         "webshop",
		Candidate:    testOpencodeRef,
		MasterMindID: testMasterMindName,
		CWD:          "/repo",
		Tier:         "read",
	})
	if !errors.Is(err, harness.ErrTierUnsupported) {
		t.Fatalf("err = %v, want harness.ErrTierUnsupported", err)
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a refused tier must start no process, got %d", got)
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound", loadErr)
	}
}

func TestBindPolicyTierBuilderRead(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Tier = map[string]string{"builder": "read"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:         "webshop",
		Candidate:    testClaudeRef,
		MasterMindID: testMasterMindName,
		CWD:          "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Tier != "read" {
		t.Errorf("b.Tier = %q, want %q", b.Tier, "read")
	}
	args := launchArgs(t, rt, b, harness.TierRead)
	hasFlag := false
	for i, arg := range args {
		if arg == "--permission-mode" && i+1 < len(args) && args[i+1] == "plan" {
			hasFlag = true
			break
		}
	}
	if !hasFlag {
		t.Errorf("expected --permission-mode plan in the launch args, got %v", args)
	}
}

// TestBindGateFlagStored pins #132: an explicit --gate is stored on the
// binding as given.
func TestBindGateFlagStored(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
		Gate: "make check",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want %q", b.Gate, "make check")
	}
}

// TestBindGatePolicyDefaultApplied pins #132: with no --gate, policy.json's
// gate.default is used.
func TestBindGatePolicyDefaultApplied(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("b.Gate = %q, want the policy default %q", b.Gate, "make check")
	}
}

// TestBindNoGateOverridesPolicyDefault pins #132: --no-gate opts a binding
// out of policy.json's gate.default.
func TestBindNoGateOverridesPolicyDefault(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
		NoGate: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "" {
		t.Errorf("b.Gate = %q, want empty despite the policy default", b.Gate)
	}
}

// TestBindRegateFlagStored pins #132 part 2: an explicit --regate is stored on
// the binding as given.
func TestBindRegateFlagStored(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
		Regate: ptr(3),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 3 {
		t.Errorf("b.Regate = %d, want 3", b.Regate)
	}
	stored, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Regate != 3 {
		t.Errorf("stored Regate = %d, want 3", stored.Regate)
	}
}

// TestBindRegatePolicyDefaultApplied pins #132 part 2: with no --regate,
// policy.json's gate.regate becomes the binding's budget.
func TestBindRegatePolicyDefaultApplied(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 2 {
		t.Errorf("b.Regate = %d, want the policy default 2", b.Regate)
	}
}

// TestBindRegateFlagOverridesPolicy pins #132 part 2: an explicit --regate 0
// turns the policy default off for this binding.
func TestBindRegateFlagOverridesPolicy(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Regate: ptr(2)}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
		Regate: ptr(0),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Regate != 0 {
		t.Errorf("b.Regate = %d, want 0 despite the policy default", b.Regate)
	}
}

func TestResolveVerbMasterMindRegistersOpencodeSession(t *testing.T) {
	t.Setenv("RELEVO_MASTERMIND", "")
	t.Setenv("CLAUDECODE", "")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
	t.Setenv("RELEVO_HARNESS", "opencode")
	rt := newRuntime(t)
	rt.OpencodeSession = func(cwd string, now time.Time) (string, error) {
		return "ses_abc", nil
	}

	rec, ok, err := resolveVerbMasterMind(rt, "")
	if err != nil {
		t.Fatalf("resolveVerbMasterMind: %v", err)
	}
	if !ok {
		t.Fatal("resolveVerbMasterMind returned ok=false")
	}
	if rec.HarnessKind != "opencode" || rec.SessionID != "ses_abc" {
		t.Errorf("got rec = %+v, want opencode/ses_abc", rec)
	}

	// a second call returns the same record id
	second, ok, err := resolveVerbMasterMind(rt, "")
	if err != nil {
		t.Fatalf("second resolveVerbMasterMind: %v", err)
	}
	if !ok {
		t.Fatal("second resolveVerbMasterMind returned ok=false")
	}
	if second.ID != rec.ID {
		t.Errorf("second resolveVerbMasterMind ID = %s, want %s", second.ID, rec.ID)
	}
}

// TestRequireFeatureChoice pins #637's pure CLI rule: a fresh bind must name
// exactly one of --feature/--no-feature; a resume may name none, but not both.
func TestRequireFeatureChoice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		feature   string
		noFeature bool
		resume    bool
		wantErr   bool
	}{
		{"fresh with feature", "auth", false, false, false},
		{"fresh with no-feature", "", true, false, false},
		{"fresh with neither", "", false, false, true},
		{"fresh with both", "auth", true, false, true},
		{"resume with feature", "auth", false, true, false},
		{"resume with no-feature", "", true, true, false},
		{"resume with neither", "", false, true, false},
		{"resume with both", "auth", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RequireFeatureChoice(c.feature, c.noFeature, c.resume)
			if !c.wantErr {
				if err != nil {
					t.Fatalf("RequireFeatureChoice(%q, %v, %v) = %v, want nil", c.feature, c.noFeature, c.resume, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("RequireFeatureChoice(%q, %v, %v) = nil, want an error", c.feature, c.noFeature, c.resume)
			}
			for _, flag := range []string{"--feature", "--no-feature"} {
				if !strings.Contains(err.Error(), flag) {
					t.Errorf("err = %q, want it to name %s", err.Error(), flag)
				}
			}
		})
	}
}

// TestBindStoresTicketWithRepoHint pins #637: a fresh bind parses --ticket
// against the binding's own captured origin, so a bare number becomes
// owner/repo#N.
func TestBindStoresTicketWithRepoHint(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{
		repoFactsOrigin:    "git@github.com:o/r.git",
		repoFactsCommonDir: "/repo/.git",
	}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		CWD: "/repo", NoFeature: true, Ticket: "607",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Ticket != "o/r#607" {
		t.Errorf("Ticket = %q, want o/r#607", b.Ticket)
	}
	if b.Feature != "" {
		t.Errorf("Feature = %q, want empty: --no-feature stores no label", b.Feature)
	}
}

// TestBindStoresBareTicketWithoutRepo pins the other half: with no captured
// origin, a bare number stores as #N.
func TestBindStoresBareTicketWithoutRepo(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t) // no Git: captureRepo is nil

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		CWD: "/repo", NoFeature: true, Ticket: "#42",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Ticket != "#42" {
		t.Errorf("Ticket = %q, want #42", b.Ticket)
	}
}

// TestBindRejectsBadTicketBeforeSpawn pins #637: a malformed --ticket is
// refused before anything is spawned and no binding is saved.
func TestBindRejectsBadTicketBeforeSpawn(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName,
		CWD: "/repo", NoFeature: true, Ticket: "not a ticket",
	})
	if err == nil || !strings.Contains(err.Error(), "ticket:") {
		t.Fatalf("Bind err = %v, want one containing %q", err, "ticket:")
	}
	if got := len(runnerOf(t, rt).specs); got != 0 {
		t.Errorf("a rejected ticket must spawn no process, got %d", got)
	}
	if _, loadErr := rt.Store.Load("webshop"); !errors.Is(loadErr, store.ErrNotFound) {
		t.Errorf("Load err = %v, want store.ErrNotFound: a rejected ticket saves no binding", loadErr)
	}
}

// TestResumeAppliesLabelFlags pins #637's resume rule: --feature sets,
// --no-feature clears, neither keeps; --ticket sets (parsed against the
// binding's own origin) and absent keeps. A malformed --ticket is refused even
// though the resume changes nothing else.
func TestResumeAppliesLabelFlags(t *testing.T) {
	t.Parallel()

	seed := func(t *testing.T) Runtime {
		t.Helper()
		rt := runtimeWithMasterMind(t, "mastermind-sess", "")
		b := store.Binding{
			Name:       "webshop",
			CWD:        "/repo",
			Round:      3,
			State:      store.StateBroken,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "old-sess"},
			Builder:    store.Endpoint{AgentName: "webshop-builder", Kind: "opencode", Mode: store.ModeHeadless},
			RepoRef:    &store.RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"},
			Feature:    "auth",
			Ticket:     "o/r#607",
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
		return rt
	}
	resume := func(t *testing.T, rt Runtime, opts BindOptions) store.Binding {
		t.Helper()
		opts.Name = "webshop"
		opts.Resume = true
		opts.MasterMindID = testMasterMindName
		opts.CWD = "/repo"
		got, err := Bind(context.Background(), rt, opts)
		if err != nil {
			t.Fatalf("resume Bind: %v", err)
		}
		return got
	}

	t.Run("neither keeps feature and ticket", func(t *testing.T) {
		got := resume(t, seed(t), BindOptions{})
		if got.Feature != "auth" || got.Ticket != "o/r#607" {
			t.Errorf("feature/ticket = %q/%q, want auth and o/r#607 kept", got.Feature, got.Ticket)
		}
	})

	t.Run("no-feature clears, ticket keeps", func(t *testing.T) {
		got := resume(t, seed(t), BindOptions{NoFeature: true})
		if got.Feature != "" {
			t.Errorf("Feature = %q, want cleared by --no-feature", got.Feature)
		}
		if got.Ticket != "o/r#607" {
			t.Errorf("Ticket = %q, want kept", got.Ticket)
		}
	})

	t.Run("feature sets", func(t *testing.T) {
		got := resume(t, seed(t), BindOptions{Feature: "checkout"})
		if got.Feature != "checkout" {
			t.Errorf("Feature = %q, want checkout", got.Feature)
		}
	})

	t.Run("ticket sets with the binding's own repo hint", func(t *testing.T) {
		got := resume(t, seed(t), BindOptions{Ticket: "42"})
		if got.Ticket != "o/r#42" {
			t.Errorf("Ticket = %q, want o/r#42", got.Ticket)
		}
	})

	t.Run("a malformed ticket is refused", func(t *testing.T) {
		rt := seed(t)
		_, err := Bind(context.Background(), rt, BindOptions{
			Name: "webshop", Resume: true, MasterMindID: testMasterMindName, CWD: "/repo",
			Ticket: "not a ticket",
		})
		if err == nil || !strings.Contains(err.Error(), "ticket:") {
			t.Fatalf("resume Bind err = %v, want one containing %q", err, "ticket:")
		}
	})
}

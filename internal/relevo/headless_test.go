package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The headless fixtures (#303 step 3): a local builder is a process relevo runs
// per round, so every test here drives it through fakeRunner, and no pane
// agent or pane appears anywhere.

// sentHeadless is seedHeadless plus one Send: round 1 open, one process
// started on fr.
func sentHeadless(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return rt, b
}

func TestHandleOfConvertsUnixSeconds(t *testing.T) {
	t.Parallel()

	h := handleOf(store.Endpoint{PID: 42, StartedAt: 1_789_000_000})
	if h.PID != 42 || !h.StartedAt.Equal(time.Unix(1_789_000_000, 0)) {
		t.Errorf("handleOf = %+v", h)
	}
	if z := handleOf(store.Endpoint{}); z.PID != 0 || !z.StartedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("zero endpoint: %+v", z)
	}
}

func TestRoundBudgetIsTheBindingsRoundTimeout(t *testing.T) {
	t.Parallel()

	if got := roundBudget(store.Binding{RoundTimeoutMS: 90 * 60 * 1000}); got != 90*time.Minute {
		t.Errorf("roundBudget = %s, want 1h30m", got)
	}
	// Store.Save fills RoundTimeoutMS, so zero is only ever a binding that was
	// never saved; the default matches the store's 24h.
	if got := roundBudget(store.Binding{}); got != 24*time.Hour {
		t.Errorf("roundBudget(zero) = %s, want 24h", got)
	}
}

// TestBuilderEnv pins builderEnv's contract (#335): no identity on the
// binding is nil (a local binding, or one made before the identity existed),
// a half identity is nil too, and a full one is exactly the four GIT_*
// values a commit reads, in order.
func TestBuilderEnv(t *testing.T) {
	t.Parallel()

	if got := builderEnv(store.Binding{}); got != nil {
		t.Errorf("builderEnv(no Serve) = %v, want nil", got)
	}
	if got := builderEnv(store.Binding{Serve: &store.ServeFacts{AuthorName: "Ada Lovelace"}}); got != nil {
		t.Errorf("builderEnv(no email) = %v, want nil", got)
	}

	got := builderEnv(store.Binding{Serve: &store.ServeFacts{
		AuthorName:  "Ada Lovelace",
		AuthorEmail: "ada@example.com",
	}})
	want := []string{
		"GIT_AUTHOR_NAME=Ada Lovelace",
		"GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Lovelace",
		"GIT_COMMITTER_EMAIL=ada@example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("builderEnv = %v, want %v", got, want)
	}
}

func TestHeadlessLaunchPerKind(t *testing.T) {
	t.Parallel()

	role, _ := harness.RoleByName("builder")
	set := candidateSet(t, testCandidatesJSON)
	lookup := func(token string) candidate.Candidate {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			t.Fatal(err)
		}
		c, err := set.Lookup(ref)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		token string
		want  []string
	}{
		{testAgyRef, []string{"agy", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor",
			"--output-format", "stream-json", "--print-timeout", "2h0m0s", "--add-dir", "/repo", "--dangerously-skip-permissions"}},
		{testClaudeRef, []string{"claude", "-p", "PROMPT", "--model", "m", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}},
		{testOpencodeRef, []string{"opencode", "run", "PROMPT", "-m", "test/m", "--agent", "plan-executor", "--format", "json", "--thinking", "--standalone"}},
	}
	for _, c := range cases {
		got, err := spawn.HeadlessLaunch(lookup(c.token), role, harness.TierHarness, 2*time.Hour, "PROMPT", "/repo", "/state/dir")
		if err != nil {
			t.Fatalf("%s: %v", c.token, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.token, got, c.want)
		}
	}
	if _, err := spawn.HeadlessLaunch(candidate.Candidate{Harness: "nope"}, role, harness.TierHarness, time.Hour, "x", "/repo", "/state/dir"); err == nil {
		t.Error("unknown harness kind must be an error, not a panic or an empty argv")
	}
}

func TestStartRoundPassesStateDir(t *testing.T) {
	t.Parallel()

	const codexCandidatesJSON = `[
	  {"harness":"codex","provider":"openai","model":"gpt-5.6-terra","roles":["builder"]}
	]`
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, codexCandidatesJSON)
	rt.Runner = fr

	b, err := Bind(context.Background(), rt, BindOptions{
		Name:      "codex-binding",
		Candidate: "codex/openai/gpt-5.6-terra",
		PlannerID: testPlannerName,
		CWD:       "/repo",
		Headless:  true,
		Tier:      "edit",
	})
	if err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}

	_, err = startRound(context.Background(), rt, nil, b, "the prompt")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	wantRoot := fmt.Sprintf(`sandbox_workspace_write.writable_roots=[%q]`, rt.Store.Dir(b.Name))
	found := false
	for _, arg := range spec.Argv {
		if arg == wantRoot {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in spec.Argv, got %v", wantRoot, spec.Argv)
	}
}

func TestStartRoundRecordsTheHandleAndTheLogPath(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	// A new process announces its own session on the stream (#147): the
	// previous round's id must not survive the start.
	b.Builder.StreamSessionID = "sess-from-the-previous-process"

	got, err := startRound(context.Background(), rt, nil, b, "the prompt")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	// A new round has no builder.log: stderr joins the stream, so the spec's
	// LogPath is the stream path itself (builder-log spec §4.4).
	wantStream := rt.Store.BuilderStreamPath("webshop", 1)
	if spec.Dir != "/repo" || spec.LogPath != wantStream || spec.StreamPath != wantStream {
		t.Errorf("spec Dir/LogPath/StreamPath = %q/%q/%q, want /repo/%q/%q", spec.Dir, spec.LogPath, spec.StreamPath, wantStream, wantStream)
	}
	if got.Builder.StreamRound != 1 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor after a fresh start = round %d offset %d; want 1, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if spec.Argv[0] != "agy" || spec.Argv[1] != "-p" || spec.Argv[2] != "the prompt" {
		t.Errorf("argv = %v; want the agy print form with the prompt at index 2", spec.Argv)
	}
	if !containsArg(spec.Argv, "--print-timeout", "24h0m0s") {
		t.Errorf("argv %v lacks the default 24h budget", spec.Argv)
	}
	if !containsArg(spec.Argv, "--add-dir", "/repo") {
		t.Errorf("argv %v lacks --add-dir pinned to the binding's CWD (#192)", spec.Argv)
	}
	h := fr.handles[0]
	if got.Builder.PID != h.PID || got.Builder.StartedAt != h.StartedAt.Unix() || got.Builder.LogPath != wantStream {
		t.Errorf("endpoint after start = %+v, want pid %d started %d log %s", got.Builder, h.PID, h.StartedAt.Unix(), wantStream)
	}
	if !got.Builder.Headless() || got.Builder.PaneID != "" {
		t.Errorf("mode or pane changed: %+v", got.Builder)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID after startRound = %q; a new process announces its own", got.Builder.StreamSessionID)
	}
}

func TestStartRoundOnTheSameRoundKeepsTheCursor(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 512 // a switch mid-round: the file already has 512 bytes rendered
	got, err := startRound(context.Background(), rt, nil, b, "again")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != b.Round || got.Builder.StreamOffset != 512 {
		t.Errorf("cursor = round %d offset %d; a same-round start must keep it at %d/512", got.Builder.StreamRound, got.Builder.StreamOffset, b.Round)
	}
}

func TestStartRoundOnALaterRoundMovesTheCursor(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Round = 2
	b.Builder.StreamRound, b.Builder.StreamOffset = 1, 512
	got, err := startRound(context.Background(), rt, nil, b, "round two")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if got.Builder.StreamRound != 2 || got.Builder.StreamOffset != 0 {
		t.Errorf("cursor = round %d offset %d; want 2, 0", got.Builder.StreamRound, got.Builder.StreamOffset)
	}
	if fr.specs[0].StreamPath != rt.Store.BuilderStreamPath("webshop", 2) {
		t.Errorf("StreamPath = %q, want round 2's", fr.specs[0].StreamPath)
	}
}

func TestReconcileHeadlessExitReadsTheTrailerFromTheStream(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	if _, err := reconcile(t, at(rt, time.Minute), b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.exitPaths) == 0 || fr.exitPaths[0] != rt.Store.BuilderStreamPath("webshop", 1) {
		t.Errorf("ExitCode was asked about %v; want the round-1 stream %s", fr.exitPaths, rt.Store.BuilderStreamPath("webshop", 1))
	}
}

// containsArg reports whether argv has flag immediately followed by value.

func containsArg(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func TestStartRoundFailureRecordsSpawnFailedAndLeavesPIDZero(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found on PATH")
	rt, b := seedHeadless(t, fr)

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = startRound(context.Background(), rt, tx, b, "p")
		return err
	})
	if err == nil || !errors.Is(err, fr.startErr) {
		t.Fatalf("err = %v, want the Start error wrapped", err)
	}
	if got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("a failed start must leave the endpoint idle: %+v", got.Builder)
	}
	var gated bool
	for _, g := range availability.Gates(AvailabilityDeps(rt)) {
		if g.Token == testAgyRef && g.Kind == "spawn_failed" {
			gated = true
		}
	}
	if !gated {
		t.Errorf("spawn_failed must be in the ledger for %s: %+v", testAgyRef, availability.Gates(AvailabilityDeps(rt)))
	}
}

func TestStartRoundWithoutARunnerIsErrRunnerUnavailable(t *testing.T) {
	t.Parallel()

	rt, b := seedHeadless(t, newFakeRunner())
	rt.Runner = nil
	if _, err := startRound(context.Background(), rt, nil, b, "p"); !errors.Is(err, spawn.ErrRunnerUnavailable) {
		t.Errorf("err = %v, want ErrRunnerUnavailable", err)
	}
}

// TestStartRoundSetsScope: rt.Scope set fills ProcSpec.Scope with the
// per-round unit name and the template's slice/weight/limits (#244, #216).

func TestStartRoundSetsScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 150, CPUQuota: "150%", MemoryMax: "2G", TasksMax: 64}

	if _, err := startRound(context.Background(), rt, nil, b, "the prompt"); err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	if spec.Scope == nil {
		t.Fatal("spec.Scope = nil, want a scope filled from rt.Scope")
	}
	wantUnit := fmt.Sprintf("relevo-round-local-webshop-%d", b.Round)
	if spec.Scope.Unit != wantUnit {
		t.Errorf("Scope.Unit = %q, want %q", spec.Scope.Unit, wantUnit)
	}
	if spec.Scope.Slice != rt.Scope.Slice || spec.Scope.CPUWeight != rt.Scope.CPUWeight ||
		spec.Scope.MemoryMax != rt.Scope.MemoryMax || spec.Scope.CPUQuota != rt.Scope.CPUQuota ||
		spec.Scope.TasksMax != rt.Scope.TasksMax {
		t.Errorf("Scope = %+v, want the rt.Scope template's fields carried through", spec.Scope)
	}
}

func TestScopeUnitNameSafe(t *testing.T) {
	t.Parallel()

	if got, want := scopeUnitName(store.Binding{Name: "webshop", Round: 3}), "relevo-round-local-webshop-3"; got != want {
		t.Errorf("scopeUnitName(no owner) = %q, want %q", got, want)
	}
	if got, want := scopeUnitName(store.Binding{Name: "web/shop no", Round: 1}), "relevo-round-local-web-shop-no-1"; got != want {
		t.Errorf("scopeUnitName(unsafe name) = %q, want %q", got, want)
	}
}

// TestScopeUnitNameFor is #313's unit-name table: each kind, id present and
// absent, an owned binding whose owner8 comes from a real ClientID, and a name
// that needs sanitising.
func TestScopeUnitNameFor(t *testing.T) {
	t.Parallel()

	ownerID, ok := remote.IDFromDir("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if !ok {
		t.Fatal("seed client id did not parse")
	}
	cases := []struct {
		name  string
		kind  scopeKind
		owner string
		bname string
		round int
		id    string
		want  string
	}{
		{"round, no id", scopeRound, "", "webshop", 3, "", "relevo-round-local-webshop-3"},
		{"gate, no id", scopeGate, "", "webshop", 3, "", "relevo-gate-local-webshop-3"},
		{"consult, with id", scopeConsult, "", "webshop", 2, "7f2a3c1d", "relevo-consult-local-webshop-2-7f2a3c1d"},
		{"verify, with id", scopeVerify, "", "webshop", 2, "7f2a3c1d", "relevo-verify-local-webshop-2-7f2a3c1d"},
		{"owned binding", scopeRound, string(ownerID), "webshop", 1, "", "relevo-round-00010203-webshop-1"},
		{"sanitised name and id", scopeConsult, "", "web/shop no", 1, "a b", "relevo-consult-local-web-shop-no-1-a-b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scopeUnitNameFor(c.kind, c.owner, c.bname, c.round, c.id); got != c.want {
				t.Errorf("scopeUnitNameFor = %q, want %q", got, c.want)
			}
		})
	}
}

// TestScopeFor is #313's scopeFor table: nil template gives nil, the spec
// carries the template's other fields, a gate uses GateCPUQuota when set and
// falls back to CPUQuota when not, the returned spec always zeroes
// GateCPUQuota, and the template is never mutated.
func TestScopeFor(t *testing.T) {
	t.Parallel()

	base := spawn.ScopeSpec{Slice: "relevo.slice", CPUWeight: 150, MemoryMax: "2G", CPUQuota: "150%", TasksMax: 64}

	t.Run("nil template gives nil", func(t *testing.T) {
		if got := scopeFor(Runtime{}, scopeGate, "relevo-gate-local-webshop-1", ""); got != nil {
			t.Fatalf("scopeFor(nil template) = %+v, want nil", got)
		}
	})

	t.Run("round and consult keep CPUQuota", func(t *testing.T) {
		for _, kind := range []scopeKind{scopeRound, scopeConsult} {
			tmpl := base
			tmpl.GateCPUQuota = "300%"
			rt := Runtime{Scope: &tmpl}
			got := scopeFor(rt, kind, "unit", "")
			if got == nil {
				t.Fatalf("scopeFor(%s) = nil, want a spec", kind)
			}
			if got.Unit != "unit" {
				t.Errorf("%s Unit = %q, want unit", kind, got.Unit)
			}
			if got.CPUQuota != "150%" {
				t.Errorf("%s CPUQuota = %q, want the template's 150%%", kind, got.CPUQuota)
			}
			if got.GateCPUQuota != "" {
				t.Errorf("%s GateCPUQuota = %q, want it zeroed", kind, got.GateCPUQuota)
			}
			if got.Slice != base.Slice || got.CPUWeight != base.CPUWeight ||
				got.MemoryMax != base.MemoryMax || got.TasksMax != base.TasksMax {
				t.Errorf("%s spec = %+v, want the template's other fields carried through", kind, got)
			}
		}
	})

	t.Run("gate with GateCPUQuota uses it", func(t *testing.T) {
		tmpl := base
		tmpl.GateCPUQuota = "300%"
		got := scopeFor(Runtime{Scope: &tmpl}, scopeGate, "unit", "")
		if got == nil {
			t.Fatal("scopeFor = nil, want a spec")
		}
		if got.CPUQuota != "300%" {
			t.Errorf("gate CPUQuota = %q, want the gate quota 300%%", got.CPUQuota)
		}
		if got.GateCPUQuota != "" {
			t.Errorf("gate GateCPUQuota = %q, want it zeroed", got.GateCPUQuota)
		}
	})

	t.Run("gate without GateCPUQuota falls back to CPUQuota", func(t *testing.T) {
		tmpl := base
		got := scopeFor(Runtime{Scope: &tmpl}, scopeGate, "unit", "")
		if got == nil {
			t.Fatal("scopeFor = nil, want a spec")
		}
		if got.CPUQuota != "150%" {
			t.Errorf("gate CPUQuota = %q, want the template's 150%%", got.CPUQuota)
		}
	})

	t.Run("cpus overrides the pool for every kind", func(t *testing.T) {
		for _, kind := range []scopeKind{scopeRound, scopeGate, scopeConsult, scopeVerify} {
			tmpl := base
			tmpl.AllowedCPUs = "0-2"
			rt := Runtime{Scope: &tmpl}
			if got := scopeFor(rt, kind, "unit", ""); got == nil || got.AllowedCPUs != "0-2" {
				t.Errorf("%s with cpus=\"\" AllowedCPUs = %+v, want the pool 0-2", kind, got)
			}
			if got := scopeFor(rt, kind, "unit", "1"); got == nil || got.AllowedCPUs != "1" {
				t.Errorf("%s with cpus=\"1\" AllowedCPUs = %+v, want 1", kind, got)
			}
		}
	})

	t.Run("template is not mutated", func(t *testing.T) {
		tmpl := base
		tmpl.GateCPUQuota = "300%"
		before := tmpl
		rt := Runtime{Scope: &tmpl}
		_ = scopeFor(rt, scopeGate, "unit", "")
		_ = scopeFor(rt, scopeConsult, "unit", "")
		if tmpl != before {
			t.Errorf("rt.Scope mutated: %+v, want %+v", tmpl, before)
		}
	})
}

// TestSendHeadlessWithoutRunnerStagesNothing pins #149's behaviour fix: the
// missing runner is a send precondition, checked before anything is staged.
// Before the fix Send wrote the plan first and only startRound discovered the
// runner was gone, leaving a staged plan and a NEEDS YOU binding behind a
// process that could never start.

func TestSendHeadlessWithoutRunnerStagesNothing(t *testing.T) {
	t.Parallel()

	rt, _ := seedHeadless(t, newFakeRunner())
	rt.Runner = nil

	before, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nBefore, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	_, err = Send(context.Background(), rt, "webshop", writePlan(t, "# x"), SendOptions{})
	if !errors.Is(err, spawn.ErrRunnerUnavailable) {
		t.Fatalf("Send err = %v, want ErrRunnerUnavailable", err)
	}

	if _, statErr := os.Stat(rt.Store.PlanPath("webshop", 1)); statErr == nil {
		t.Error("no plan may be staged when there is no runner")
	}
	nAfter, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(nBefore) != len(nAfter) {
		t.Errorf("log length changed: %d -> %d", len(nBefore), len(nAfter))
	}
	after, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.State != before.State || after.State == store.StateNeedsYou {
		t.Errorf("state = %s, want unchanged %s (not needs_you)", after.State, before.State)
	}
}

func TestSendHeadlessRefusesWhileThePreviousProcessIsAlive(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one"), SendOptions{}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Unscripted, the fake reports the process alive forever.
	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "round one again"), SendOptions{})
	if !errors.Is(err, ErrBuilderBusy) {
		t.Fatalf("second Send: err = %v, want ErrBuilderBusy", err)
	}
	if len(fr.specs) != 1 {
		t.Errorf("a refused send must start nothing: specs = %d", len(fr.specs))
	}
	plan, _ := os.ReadFile(rt.Store.PlanPath("webshop", 1))
	if string(plan) != "round one" {
		t.Errorf("a refused send must not restage the plan: %q", plan)
	}
}

func TestSendHeadlessStartsAgainOnceThePreviousProcessExited(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one"), SendOptions{}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	fr.script(fr.handles[0].PID, false) // exited between the two sends
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "one, corrected"), SendOptions{}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if len(fr.specs) != 2 || len(fr.handles) != 2 || fr.handles[1].PID == fr.handles[0].PID {
		t.Fatalf("want a second, distinct process: specs=%d handles=%+v", len(fr.specs), fr.handles)
	}
	b, _ := rt.Store.Load("webshop")
	if b.Builder.PID != fr.handles[1].PID {
		t.Errorf("endpoint pid = %d, want the new process %d", b.Builder.PID, fr.handles[1].PID)
	}
}

func TestSendHeadlessStartFailureGoesNeedsYou(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found on PATH")
	rt, _ := seedHeadless(t, fr)

	_, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{})
	if err == nil || !errors.Is(err, fr.startErr) {
		t.Fatalf("err = %v, want the Start error wrapped", err)
	}
	b, _ := rt.Store.Load("webshop")
	if b.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", b.State)
	}
	if b.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0 after a failed start", b.Builder.PID)
	}
	if !strings.HasPrefix(b.Halt, "builder spawn failed: ") {
		t.Errorf("Halt = %q, want prefix %q", b.Halt, "builder spawn failed: ")
	}
	if !strings.Contains(b.Halt, "not found on PATH") {
		t.Errorf("Halt = %q, want it to contain %q", b.Halt, "not found on PATH")
	}
	if b.HaltAt.IsZero() {
		t.Error("HaltAt is zero, want set")
	}
	entries, _ := rt.Store.ReadLog("webshop")
	for _, e := range entries {
		if e.Kind == store.KindPlan {
			t.Errorf("no plan entry may be logged for a round that never started: %+v", e)
		}
	}
	if _, err := os.Stat(rt.Store.PlanPath("webshop", 1)); err != nil {
		t.Errorf("the plan stays staged so the human can retry: %v", err)
	}
}

// TestSendClearsAStaleHalt pins Send's success path: a binding that was
// halted for an earlier round must not carry that halt into a round it just
// successfully handed over.

func TestSendClearsAStaleHalt(t *testing.T) {
	t.Parallel()

	rt, b := seedBound(t)
	b.Halt = "round 1 has run past 1s"
	b.HaltAt = rt.Now()
	b.State = store.StateNeedsYou
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
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
}

func TestLogTailReturnsTheLastLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "003-builder.log")
	if err := os.WriteFile(p, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 2); got != "c\nd" {
		t.Errorf("logTail(2) = %q, want \"c\\nd\"", got)
	}
	if got := logTail(p, 10); got != "a\nb\nc\nd" {
		t.Errorf("logTail(10) = %q, want the whole file without the trailing newline", got)
	}
	if got := logTail(filepath.Join(dir, "absent.log"), 3); got != "" {
		t.Errorf("logTail(absent) = %q, want empty", got)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 3); got != "" {
		t.Errorf("logTail(empty) = %q, want empty", got)
	}
}

func TestClearProcessKeepsIdentity(t *testing.T) {
	t.Parallel()

	e := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 7, StartedAt: 9, LogPath: "/l", StreamRound: 3, StreamOffset: 99}
	got := clearProcess(e)
	want := store.Endpoint{AgentName: "x-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 3, StreamOffset: 99}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clearProcess = %+v, want %+v", got, want)
	}
}

// streamWrite appends raw to webshop's round-1 stream file, creating it.

func streamWrite(t *testing.T, rt Runtime, raw string) {
	t.Helper()
	p := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// readLog is webshop's round-1 builder log, "" when absent.

func readLog(t *testing.T, rt Runtime) string {
	t.Helper()
	data, err := os.ReadFile(rt.Store.BuilderLogPath("webshop", 1))
	if err != nil {
		return ""
	}
	return string(data)
}

// seedLegacyLog creates an empty NNN-builder.log for a round, making it a
// round from before builder-log round 2 (builder-log spec §4.5): the drain
// renders into it and the readers show it, exactly as before.
func seedLegacyLog(t *testing.T, rt Runtime, name string, round int) {
	t.Helper()
	p := rt.Store.BuilderLogPath(name, round)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	agyToolActive = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}` + "\n"
	agyToolDone   = `{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command"}}` + "\n"
	agyResult     = `{"event":"result","result":{"status":"SUCCESS","response":"all done","denied_actions":[]}}` + "\n"
)

func TestDrainStreamRendersNewLinesInOrderAndAdvances(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr) // round 1 open, cursor at 1/0
	seedLegacyLog(t, rt, "webshop", 1)
	streamWrite(t, rt, agyToolActive+agyToolDone)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\n  ⎿ ok\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
	if got.Builder.StreamOffset != int64(len(agyToolActive+agyToolDone)) {
		t.Errorf("offset = %d, want the whole file %d", got.Builder.StreamOffset, len(agyToolActive+agyToolDone))
	}

	// Nothing new: nothing appended.
	again, err := reconcile(t, rt, got)
	if err != nil || readLog(t, rt) != "● run_command go test ./...\n  ⎿ ok\n" || again.Builder.StreamOffset != got.Builder.StreamOffset {
		t.Errorf("a tick with no new stream data must change nothing: log=%q offset=%d err=%v", readLog(t, rt), again.Builder.StreamOffset, err)
	}

	// More arrives: appended after, in order.
	streamWrite(t, rt, agyResult)
	got, err = reconcile(t, rt, again)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\n  ⎿ ok\nall done\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamWaitsForAPartialLine(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	whole := strings.TrimSuffix(agyToolActive, "\n")
	streamWrite(t, rt, whole[:40]) // mid-event, no newline yet

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "" || got.Builder.StreamOffset != 0 {
		t.Errorf("a partial line must not be rendered or consumed: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
	streamWrite(t, rt, whole[40:]+"\n")
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("completed line: log=%q offset=%d", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamCursorSurvivesAReload(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	streamWrite(t, rt, agyToolActive)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.Save(got) }); err != nil {
		t.Fatal(err)
	}
	loaded, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Builder.StreamRound != 1 || loaded.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Fatalf("cursor after reload = %d/%d", loaded.Builder.StreamRound, loaded.Builder.StreamOffset)
	}
	// A daemon restarted from that state renders nothing twice.
	if _, err := reconcile(t, rt, loaded); err != nil {
		t.Fatal(err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" {
		t.Errorf("log after reload tick = %q; the line was rendered twice", readLog(t, rt))
	}
}

func TestDrainStreamCursorPastEndRendersFromTheStart(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	streamWrite(t, rt, agyToolActive)
	b.Builder.StreamOffset = 10_000 // a state file rewritten by hand
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if readLog(t, rt) != "● run_command go test ./...\n" || got.Builder.StreamOffset != int64(len(agyToolActive)) {
		t.Errorf("log=%q offset=%d; want rendered from 0 and the cursor at EOF", readLog(t, rt), got.Builder.StreamOffset)
	}
}

func TestDrainStreamNoiseAdvancesWithoutWriting(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	noise := `{"event":"init","init":{}}` + "\n"
	streamWrite(t, rt, noise)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, statErr := os.Stat(rt.Store.BuilderLogPath("webshop", 1)); statErr == nil {
		t.Error("all-noise input must not create the log")
	}
	if got.Builder.StreamOffset != int64(len(noise)) {
		t.Errorf("offset = %d, want %d", got.Builder.StreamOffset, len(noise))
	}
}

func TestReconcileHeadlessExitEntryCarriesTheRenderedResult(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	// stderr went straight to the log; stdout and the trailer to the stream,
	// all before this tick (the process is gone).
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("jetski: starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	streamWrite(t, rt, agyToolActive+agyToolDone+agyResult+"\nrelevo-exit:0\n")

	if _, err := reconcile(t, at(rt, time.Minute), b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	want := "jetski: starting\n● run_command go test ./...\n  ⎿ ok\nall done"
	if ex[0].Payload != want {
		t.Errorf("payload = %q, want the drained log %q", ex[0].Payload, want)
	}
	if !strings.Contains(ex[0].Note, "(code 0)") {
		t.Errorf("note = %q; the code must still come from the trailer", ex[0].Note)
	}
}

func TestDrainStreamKeepsGoingAfterAMarkerClose(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	streamWrite(t, rt, agyToolActive)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 {
		t.Fatalf("round=%d pid=%d; want the marker to close round 1", got.Round, got.Builder.PID)
	}
	if got.Builder.StreamRound != 1 {
		t.Fatalf("StreamRound = %d after the close; the cursor must stay on round 1's file", got.Builder.StreamRound)
	}
	// The builder flushes its result after relevo saw the marker.
	streamWrite(t, rt, agyResult+"\nrelevo-exit:0\n")
	if _, err := reconcile(t, rt, got); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := "● run_command go test ./...\nall done\n"; readLog(t, rt) != want {
		t.Errorf("round 1 log after the close = %q, want %q", readLog(t, rt), want)
	}
}

func TestDrainStreamIsANoopBeforeAnyRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr) // bound, never sent: StreamRound 0
	got := drainStream(rt, b)
	if !reflect.DeepEqual(got, b) {
		t.Errorf("drainStream changed a binding with no stream: %+v", got.Builder)
	}
}

func TestDrainStreamDoesNotAdvanceOnAWriteFailure(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if err := os.MkdirAll(rt.Store.BuilderLogPath("webshop", 1), 0o755); err != nil {
		t.Fatal(err)
	}
	streamWrite(t, rt, agyToolActive)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.StreamOffset != 0 {
		t.Errorf("StreamOffset = %d on write failure; want 0 (cursor must not advance)", got.Builder.StreamOffset)
	}
}

// sentHeadless is seedHeadless plus one Send: round 1 open, one process
// started on fr.

func TestReconcileHeadlessSkipsQueued(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{Defer: true}); err != nil {
		t.Fatalf("Send(Defer): %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !store.SameBinding(b, got) {
		t.Errorf("queued binding changed: before=%+v after=%+v", b, got)
	}
	if len(fr.specs) != 0 {
		t.Errorf("specs = %d, want 0 (no spawn while queued)", len(fr.specs))
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty", got.Halt)
	}
}

// TestVerifyRoundStartsOnHeadlessClose pins #144's headless close path: the
// same reviewer a pane round's close starts must start when a headless round
// closes on its marker. The builder's own process is the first entry in
// fr.specs; the verify consult is the second, in the throwaway worktree.
//
// Mutation check (run and report): delete the wantVerify block from
// reconcileHeadless's close path and this fails on addDetachedWorktreeCalls.

func TestVerifyRoundStartsOnHeadlessClose(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fg := &fakeGit{headCommitID: "head1"}
	rt.Git = fg
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: the round closed", got.Round)
	}
	if got.RoundVerify {
		t.Errorf("RoundVerify = true after the close, want cleared")
	}

	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)

	// specs[0] is the builder's own process; the verify consult is second.
	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want the builder's plus one verify consult", len(fr.specs))
	}
	if fr.specs[1].Dir != wantWT {
		t.Errorf("verify consult Dir = %q, want the throwaway worktree %q", fr.specs[1].Dir, wantWT)
	}

	if len(fg.addDetachedWorktreeCalls) != 1 {
		t.Fatalf("AddDetachedWorktree calls = %v, want exactly 1", fg.addDetachedWorktreeCalls)
	}
	if call := fg.addDetachedWorktreeCalls[0]; call.Dir != b.CWD || call.Path != wantWT || call.Commit != "head1" {
		t.Errorf("AddDetachedWorktree = %+v, want {%s %s head1}", call, b.CWD, wantWT)
	}

	var vc *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			vc = &got.Consults[i]
		}
	}
	if vc == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}
	if vc.Round != 1 {
		t.Errorf("consult round = %d, want 1", vc.Round)
	}
	if vc.State != store.ConsultRunning {
		t.Errorf("consult state = %q, want running", vc.State)
	}
}

// TestVerifyInlinesItsQuestion (N5): the verify reviewer's question is carried
// in its argv when it fits, recorded at AskPath with no file on disk.
//
// Mutation check (run and report): always os.WriteFile with the Read: prompt
// fails the argv assertion.
func TestVerifyInlinesItsQuestion(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// specs[0] is the builder's own process; the verify consult is second.
	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want the builder's plus one verify consult", len(fr.specs))
	}
	argv := strings.Join(fr.specs[1].Argv, "\x00")
	if !strings.Contains(argv, "Verify round 1 of binding") {
		t.Errorf("verify argv does not carry the question:\n%s", argv)
	}

	var vc *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			vc = &got.Consults[i]
		}
	}
	if vc == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}
	if _, err := os.Stat(vc.AskPath); !os.IsNotExist(err) {
		t.Errorf("ask file exists on disk at %s (err %v), want no file", vc.AskPath, err)
	}
	question, err := rt.Store.ReadFile(vc.AskPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", vc.AskPath, err)
	}
	if !strings.Contains(string(question), "Verify round 1 of binding") {
		t.Errorf("recorded question does not contain the verify prompt:\n%s", question)
	}
}

func TestVerifyRoundHandsTheReviewerAGitDiff(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fg := &fakeGit{
		headCommitID:   "head1",
		snapshotTreeID: "tree-end",
		diffResult:     git.Diff{Stat: git.Stat{FilesChanged: 1, Insertions: 1}, Patch: []byte("PATCH\n")},
	}
	rt.Git = fg
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }

	b.RoundVerify = true
	b.RoundBaselineTree = "tree-base"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.Round != 2 {
		t.Fatalf("round = %d, want 2: the round closed", got.Round)
	}

	var vc *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			vc = &got.Consults[i]
		}
	}
	if vc == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}

	q, err := rt.Store.ReadFile(vc.AskPath)
	if err != nil {
		t.Fatalf("read ask: %v", err)
	}
	if !strings.Contains(string(q), "git diff tree-base tree-end") {
		t.Errorf("question %q does not contain %q", string(q), "git diff tree-base tree-end")
	}
	if strings.Contains(string(q), "diff.patch") {
		t.Errorf("question %q contains diff.patch", string(q))
	}

	if _, err := os.Stat(rt.Store.DiffPath("webshop", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected diff.patch to not exist on disk, got err: %v", err)
	}

	patch, err := rt.Store.ReadFile(rt.Store.DiffPath("webshop", 1))
	if err != nil {
		t.Fatalf("read diff patch: %v", err)
	}
	if string(patch) != "PATCH\n" {
		t.Fatalf("diff patch = %q, want %q", string(patch), "PATCH\n")
	}
}

// TestSendResetsRoundBudget pins #250 item 2 for the headless shape: a
// human's re-send is a fresh attempt, so it clears the round's switch
// bookkeeping along with Halt/HaltAt.

func TestSendResetsRoundBudget(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false) // previous round's process no longer running
	b.RoundSwitches = 1
	b.RoundExcluded = []string{"x/y/z"}
	b.HaltNotifiedRound = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it again"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want nil", got.RoundExcluded)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
	}
}

// switchHeadless runs switchBuilder on b inside the lock, the way Reconcile does.

func switchHeadless(t *testing.T, rt Runtime, b store.Binding, reason string, closeOld bool) (store.Binding, error) {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		out, err = switchBuilder(context.Background(), rt, tx, b, reason, closeOld, true)
		return err
	})
	return out, err
}

// exits returns the exit entries in webshop's log.

func exits(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindExit {
			out = append(out, e)
		}
	}
	return out
}

func TestReconcileHeadlessReportWinsEvenIfTheProcessExitedNonZero(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, rt, b)
	pending, found, perr := rt.Store.PendingForPlanner("webshop")
	if err != nil || got.Round != 2 || len(exits(t, rt)) != 0 || len(fr.specs) != 1 || perr != nil || !found {
		t.Fatalf("round=%d exits=%d specs=%d err=%v found=%v; want the report to finish the round with no exit entry and no switch", got.Round, len(exits(t, rt)), len(fr.specs), err, found)
	}
	if pending.Note != "unmarked" || !strings.Contains(pending.Payload, "exited (code 1)") {
		t.Errorf("note=%q payload=%q, want an unmarked close naming the exit code", pending.Note, pending.Payload)
	}
}

// TestHeadlessMarkerWrittenBetweenChecksClosesMarked pins the check-then-check
// race (#328): the marker is absent when closeOnMarker reads it, then the
// process writes it and exits before the liveness check observes the exit. The
// re-check in the exited branch must close the round through the marker path,
// so the report's note is the marked one (""), not "unmarked".
func TestHeadlessMarkerWrittenBetweenChecksClosesMarked(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The race inside one tick: Alive creates the marker, then reports the
	// process dead.
	fr.onAlive = func() {
		fr.onAlive = nil
		if err := os.WriteFile(rt.Store.DonePath("webshop", 1), nil, 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
	fr.script(b.Builder.PID, false)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 (the marker closed the round)", got.Round)
	}
	pending, found, perr := rt.Store.PendingForPlanner("webshop")
	if perr != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, perr)
	}
	if pending.Note != "" {
		t.Errorf("note = %q, want empty (closed by marker, not unmarked)", pending.Note)
	}
}

// TestHeadlessExitWithReportNoMarkerStillUnmarked pins that the fix leaves the
// old path intact: an exited process with a report and still no marker is
// closed "unmarked", exactly as before.
func TestHeadlessExitWithReportNoMarkerStillUnmarked(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 1)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2", got.Round)
	}
	pending, found, perr := rt.Store.PendingForPlanner("webshop")
	if perr != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, perr)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
}

func TestReconcileHeadlessStallClearsWhenStreamMoves(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)

	now := baseTime.Add(10 * time.Minute)
	rt = at(rt, 10*time.Minute)
	b.RoundStartedAt = now.Add(-30 * time.Minute)
	stream := rt.Store.BuilderStreamPath(b.Name, b.Round)
	if err := os.WriteFile(stream, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	quietAt := now.Add(-20 * time.Minute)
	if err := os.Chtimes(stream, quietAt, quietAt); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.StalledSince.IsZero() {
		t.Fatalf("StalledSince is zero; want the stall stamped before the clear case")
	}

	// The stream moves again: the next tick clears the stamp. The clock must
	// tick past progress_interval_ms first -- #135 samples at most once per
	// interval, so a tick at the same instant records nothing.
	moved := now
	if err := os.Chtimes(stream, moved, moved); err != nil {
		t.Fatalf("chtimes back: %v", err)
	}
	cleared, err := reconcile(t, at(rt, 11*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile (moved): %v", err)
	}
	if !cleared.StalledSince.IsZero() {
		t.Errorf("StalledSince = %s, want zero once the stream moves again", cleared.StalledSince)
	}

	// A process that exits with a report closes the round, and the closed
	// binding carries no stall.
	fr2 := newFakeRunner()
	rt2, b2 := sentHeadless(t, fr2)
	rt2 = at(rt2, 10*time.Minute)
	b2.RoundStartedAt = rt2.Now().Add(-30 * time.Minute)
	b2.StalledSince = rt2.Now().Add(-20 * time.Minute)
	fr2.script(b2.Builder.PID, false)
	if err := os.WriteFile(rt2.Store.ReportPath(b2.Name, b2.Round), []byte("done"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	closed, err := reconcile(t, rt2, b2)
	if err != nil {
		t.Fatalf("Reconcile (exited with report): %v", err)
	}
	if closed.Round != 2 {
		t.Fatalf("round = %d, want 2 after the report closed the round", closed.Round)
	}
	if !closed.StalledSince.IsZero() {
		t.Errorf("StalledSince = %s, want zero on the closed round", closed.StalledSince)
	}
}

// TestStatusHeadlessStalledLabel pins #252's label: a live, stalled headless
// builder reads "stalled <age>", and a live, unstalled one reads "working".

func TestStatusHeadlessStalledLabel(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.StalledSince = baseTime.Add(-20 * time.Minute)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	if got := rep.Bindings[0].BuilderStatus; !strings.HasPrefix(got, "stalled ") {
		t.Fatalf("BuilderStatus = %q, want it to start with %q", got, "stalled ")
	} else if !strings.Contains(got, view.AgeText(baseTime.Sub(b.StalledSince))) {
		t.Errorf("BuilderStatus = %q, want it to contain the age %q", got, view.AgeText(baseTime.Sub(b.StalledSince)))
	}

	b.StalledSince = time.Time{}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status (not stalled): %v", err)
	}
	if got := rep.Bindings[0].BuilderStatus; got != "working" {
		t.Errorf("BuilderStatus = %q, want %q when not stalled", got, "working")
	}
}

func TestReconcileHeadlessExitWithoutReportLogsAndSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 3)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("starting\nboom: out of tokens\nrelevo-exit:3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1", len(ex))
	}
	if ex[0].Note != "builder exited (code 3) without a report" {
		t.Errorf("note = %q", ex[0].Note)
	}
	if !strings.Contains(ex[0].Payload, "boom: out of tokens") || ex[0].Path != logPath {
		t.Errorf("payload/path = %q / %q, want the log tail and the log path", ex[0].Payload, ex[0].Path)
	}
	if !ex[0].Confirmed || ex[0].Direction != store.DirToPlanner || ex[0].Round != 1 {
		t.Errorf("exit entry shape = %+v", ex[0])
	}
	// Then the switch, exactly as "gone" does today.
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (exited (code 3) without a report): picked "+testClaudeRef) {
		t.Errorf("switch entries = %+v", sw)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a second Start on claude: %+v", fr.specs)
	}
	if got.Builder.PID != fr.handles[1].PID || got.Builder.PID == oldPID {
		t.Errorf("pid = %d, want the replacement's %d", got.Builder.PID, fr.handles[1].PID)
	}
	if got.Round != 1 || got.RoundSwitches != 1 || got.State != store.StateActive || got.BuilderCandidate != testClaudeRef {
		t.Errorf("bookkeeping: round=%d switches=%d state=%s cand=%q", got.Round, got.RoundSwitches, got.State, got.BuilderCandidate)
	}
	if len(fr.kills) != 0 {
		t.Errorf("an exited process is not killed: %+v", fr.kills)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince is a pane concept: %s", got.BuilderMissingSince)
	}
}

func TestReconcileHeadlessExitUnknownCode(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false) // exited; no exit() set: killed before the trailer, say

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 || ex[0].Note != "builder exited (code unknown) without a report" {
		t.Errorf("exit entries = %+v", ex)
	}
}

// TestReconcileHeadlessLostToDaemonRestartRelaunches pins #244 half 1: a
// builder whose recorded start predates the daemon's own could not have
// died on its own -- the daemon itself must have taken it down (a systemd
// restart, a kill -9 of the process tree) before the supervisor could write
// the relevo-exit: trailer. relevo relaunches the same candidate on the same
// round instead of switching, and charges nothing.
//
// Mutation check: drop the `Before(rt.StartedAt)` condition in headless.go's
// `lost` computation (making it always false) and this test must fail.

func TestReconcileHeadlessLostToDaemonRestartRelaunches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	rt.Watched = NewWatched()                           // and it has seen nothing yet: this builder is lost
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown
	// Tick ten minutes after the round started, so a relaunch that restarted
	// the round's budget clock (the pre-#370 reset) is visible here.
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})
	keep := b.RoundStartedAt

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the relaunch)", len(fr.specs))
	}
	// The relaunch's argv is the first run's with only the prompt replaced by
	// one carrying the interrupted note (#370, spec §4.3): the two slices have
	// equal length, exactly one element differs, and that element is the old
	// prompt plus "\n\n" and the note.
	first, relaunch := fr.specs[0].Argv, fr.specs[1].Argv
	if len(relaunch) != len(first) {
		t.Fatalf("relaunch Argv has %d elements, want the first run's %d:\n %v\n %v", len(relaunch), len(first), relaunch, first)
	}
	diff := -1
	for i := range first {
		if relaunch[i] == first[i] {
			continue
		}
		if diff != -1 {
			t.Fatalf("relaunch Argv differs at elements %d and %d, want exactly one differing element:\n %v\n %v", diff, i, relaunch, first)
		}
		diff = i
	}
	if diff == -1 {
		t.Fatalf("relaunch Argv = %v, want its prompt element to carry the interrupted note", relaunch)
	}
	wantPrompt := first[diff] + "\n\n" + interruptedNote(rt.StartedAt)
	if relaunch[diff] != wantPrompt {
		t.Errorf("relaunch prompt = %q, want the first prompt plus the note %q", relaunch[diff], wantPrompt)
	}
	if !strings.Contains(relaunch[diff], interruptedNote(rt.StartedAt)) {
		t.Errorf("relaunch prompt = %q, want it to contain the interrupted note %q", relaunch[diff], interruptedNote(rt.StartedAt))
	}
	if !got.RoundStartedAt.Equal(keep) {
		t.Errorf("RoundStartedAt = %s, want the pre-relaunch value %s: the interruption must not restart the round's clock",
			got.RoundStartedAt, keep)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	if len(got.RoundExcluded) != 0 {
		t.Errorf("RoundExcluded = %v, want empty", got.RoundExcluded)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if len(fr.handles) != 2 || got.Builder.PID != fr.handles[1].PID {
		t.Errorf("PID = %d, want the new handle's pid", got.Builder.PID)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var kinds []store.Kind
	var switchNote string
	for _, e := range entries {
		switch e.Kind {
		case store.KindExit, store.KindSwitch:
			kinds = append(kinds, e.Kind)
		}
		if e.Kind == store.KindSwitch {
			switchNote = e.Note
		}
	}
	if len(kinds) != 2 || kinds[0] != store.KindExit || kinds[1] != store.KindSwitch {
		t.Fatalf("exit/switch entries = %+v, want [exit switch]", kinds)
	}
	if !strings.Contains(switchNote, "lost to a daemon restart") || !strings.Contains(switchNote, "picked "+got.BuilderCandidate+" for builder") {
		t.Errorf("switch note = %q, want it to contain %q and %q", switchNote, "lost to a daemon restart", "picked "+got.BuilderCandidate+" for builder")
	}

	// Second tick: the relaunched pid is alive, nothing repeats.
	fr.script(got.Builder.PID, true)
	if _, err := reconcile(t, rt, got); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Errorf("specs after second tick = %d, want still 2", len(fr.specs))
	}
}

// TestReconcileHeadlessSeenBuilderIsNotLost pins the other half of #370's
// rule (spec §4.3): a builder this daemon saw alive that later exits with no
// trailer is not lost to a restart. It takes the normal
// exited-without-report path -- here a counted switch -- and is never
// relaunched on the same round.
//
// Mutation check: drop the Seen clause from lostToRestart and this fails: the
// marked pid is relaunched, so there is no switch and no exit entry.
func TestReconcileHeadlessSeenBuilderIsNotLost(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	rt.Watched = NewWatched()
	rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt) // ... but this daemon saw it alive
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 || ex[0].Note != "builder exited (code unknown) without a report" {
		t.Fatalf("exit entries = %+v, want the normal exited-without-a-report entry", ex)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (exited (code unknown) without a report): picked "+testClaudeRef) {
		t.Fatalf("switch entries = %+v, want the counted switch, not a relaunch", sw)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1: a seen builder's failure is the round's own", got.RoundSwitches)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Errorf("specs = %+v, want the switch to claude, not a relaunch of agy", fr.specs)
	}
	if got.Builder.PID != fr.handles[1].PID {
		t.Errorf("PID = %d, want the switched process's %d", got.Builder.PID, fr.handles[1].PID)
	}
}

// TestReconcileHeadlessCLINeverRelaunchesLostBuilder pins lostToRestart's
// first clause (spec §4.2): rt.StartedAt is zero in a CLI one-shot, so a
// builder that merely predates the command is never judged lost and takes the
// normal counted switch instead of being relaunched (#244 kept).
func TestReconcileHeadlessCLINeverRelaunchesLostBuilder(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	rt.StartedAt = time.Time{} // the CLI: this process did not start a daemon
	rt.Watched = NewWatched()
	fr.script(b.Builder.PID, false) // exited; no exit() set: code unknown

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (exited (code unknown) without a report): picked "+testClaudeRef) {
		t.Fatalf("switch entries = %+v, want the normal counted switch", sw)
	}
	if got.RoundSwitches != 1 {
		t.Errorf("RoundSwitches = %d, want 1: a CLI one-shot charges the switch", got.RoundSwitches)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Errorf("specs = %+v, want a switch to claude, never a relaunch", fr.specs)
	}
}

// TestLostBuilderRequeuesOnServer pins #285: on a server (Owner set), a
// builder lost to a daemon restart is re-queued at the head of the queue
// instead of relaunched -- a box reboot must not relaunch every builder past
// the cap. TestReconcileHeadlessLostToDaemonRestartRelaunches above is the
// mirror for Owner == "" (the local daemon): it must keep relaunching
// exactly as it does today.
//
// Mutation check: drop the `b.Owner != ""` branch in headless.go's lost
// handling and this fails on fr.specs staying at 1.

func TestLostBuilderRequeuesOnServer(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.Owner = "owner1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	wantQueuedAt := b.RoundStartedAt
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1 (no relaunch)", len(fr.specs))
	}
	if !got.QueuedAt.Equal(wantQueuedAt) {
		t.Errorf("QueuedAt = %v, want the old RoundStartedAt %v", got.QueuedAt, wantQueuedAt)
	}
	if !got.RoundStartedAt.IsZero() {
		t.Errorf("RoundStartedAt = %v, want zero", got.RoundStartedAt)
	}
	if got.Builder.PID != 0 {
		t.Errorf("PID = %d, want 0", got.Builder.PID)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var queueEntries []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindQueue {
			queueEntries = append(queueEntries, e)
		}
	}
	if len(queueEntries) != 1 || queueEntries[0].Note != "re-queued (builder lost to a restart)" {
		t.Errorf("queue entries = %+v, want one with note %q", queueEntries, "re-queued (builder lost to a restart)")
	}
}

// TestReconcileHeadlessUnknownExitBeforeDaemonStartStillSwitches pins the
// negative cases of #244: a builder that started after the daemon (so its
// death cannot be blamed on a restart) still switches and counts, exactly
// as before -- and so does one whose daemon start is unknown (zero
// rt.StartedAt), the control case for every other test in this file.

func TestReconcileHeadlessUnknownExitBeforeDaemonStartStillSwitches(t *testing.T) {
	t.Parallel()

	t.Run("builder started after the daemon: a real death", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentHeadless(t, fr)
		rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
		rt.StartedAt = time.Unix(b.Builder.StartedAt-60, 0) // the daemon started before the builder
		fr.script(b.Builder.PID, false)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(fr.specs) != 2 {
			t.Fatalf("specs = %d, want 2 (the switch)", len(fr.specs))
		}
		if got.RoundSwitches != 1 {
			t.Errorf("RoundSwitches = %d, want 1 (counted)", got.RoundSwitches)
		}
		if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testAgyRef {
			t.Errorf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testAgyRef)
		}
	})

	t.Run("daemon start unknown (zero)", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentHeadless(t, fr)
		rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
		// rt.StartedAt left zero: unknown, so the check cannot fire.
		fr.script(b.Builder.PID, false)

		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(fr.specs) != 2 {
			t.Fatalf("specs = %d, want 2 (the switch)", len(fr.specs))
		}
		if got.RoundSwitches != 1 {
			t.Errorf("RoundSwitches = %d, want 1 (counted)", got.RoundSwitches)
		}
		if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testAgyRef {
			t.Errorf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testAgyRef)
		}
	})
}

// TestReconcileHeadlessLostToDaemonRestartRelaunchFails pins the failure
// path: the daemon recognizes the loss but cannot relaunch (e.g. the
// binary vanished); the binding halts naming both facts, and nothing is
// charged.

func TestReconcileHeadlessLostToDaemonRestartRelaunchFails(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	fr.script(b.Builder.PID, false)
	fr.startErr = errors.New("boom: no such binary")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "lost to a daemon restart") || !strings.Contains(got.Halt, "could not be relaunched") {
		t.Errorf("Halt = %q, want it to contain %q and %q", got.Halt, "lost to a daemon restart", "could not be relaunched")
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
}

// TestReconcileHeadlessLostToDaemonRestartResumesSession pins #370's R3 half
// (spec §4.10): a builder lost to a restart whose round announced a session
// is continued in that session -- claude --resume <id> on the round's own
// builder-grade argv -- instead of being started fresh. It is still not
// counted, and the round's budget clock still survives.
//
// Mutation check: drop the `sess != ""` branch and this fails: the second
// Start carries no --resume and the note says "relaunched".
func TestReconcileHeadlessLostToDaemonRestartResumesSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	const sess = "sess-lost-1"
	b.Builder.StreamSessionID = sess
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0) // the daemon started after the builder
	rt.Watched = NewWatched()                           // and it has seen nothing yet: this builder is lost
	fr.script(b.Builder.PID, false)                     // exited; no exit() set: code unknown
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})
	keep := b.RoundStartedAt

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the resume)", len(fr.specs))
	}
	resume := fr.specs[1].Argv
	if resume[0] != "claude" {
		t.Errorf("resume argv[0] = %q, want claude", resume[0])
	}
	if !containsArg(resume, "--resume", sess) {
		t.Errorf("resume argv = %v, want --resume %s", resume, sess)
	}
	// The full builder launch argv: the same model and agent the round ran
	// with, not a bare print form.
	if !containsArg(resume, "--model", "m") || !containsArg(resume, "--agent", "plan-executor") {
		t.Errorf("resume argv = %v, want the round's --model and --agent", resume)
	}
	if !anyArgContains(resume, interruptedNote(rt.StartedAt)) {
		t.Errorf("resume argv = %v, want its prompt to carry the interrupted note %q", resume, interruptedNote(rt.StartedAt))
	}
	if !got.RoundStartedAt.Equal(keep) {
		t.Errorf("RoundStartedAt = %s, want the pre-resume value %s: resuming must not restart the round's clock",
			got.RoundStartedAt, keep)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	if got.Builder.PID != fr.handles[1].PID || got.State != store.StateActive {
		t.Errorf("after resume: pid=%d state=%s, want the new handle %d and active", got.Builder.PID, got.State, fr.handles[1].PID)
	}

	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "resumed session "+sess+" builder (lost to a daemon restart") {
		t.Fatalf("switch entries = %+v, want one starting %q", sw, "resumed session "+sess+" builder")
	}
}

// TestReconcileHeadlessLostToDaemonRestartCodexFallsBackFresh pins the
// unsupported fallback (spec §6): codex has no verified resume form, so a lost
// codex builder is relaunched fresh, with the round 1 note, and never carries
// a resume selector.
//
// Mutation check: drop the ErrResumeUnsupported fallback and this fails: the
// binding halts instead of starting a second process.
func TestReconcileHeadlessLostToDaemonRestartCodexFallsBackFresh(t *testing.T) {
	t.Parallel()

	const codexCandidatesJSON = `[
	  {"harness":"codex","provider":"openai","model":"gpt-5.6-terra","roles":["builder"]}
	]`
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, codexCandidatesJSON)
	rt.Runner = fr
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: "codex/openai/gpt-5.6-terra", PlannerID: testPlannerName,
		CWD: "/repo", Headless: true, Tier: "edit",
	}); err != nil {
		t.Fatalf("Bind --headless: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamSessionID = "thread-lost-1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	rt.Watched = NewWatched()
	fr.script(b.Builder.PID, false) // exited; no exit() set: code unknown
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the fresh relaunch)", len(fr.specs))
	}
	fresh := fr.specs[1].Argv
	for _, arg := range fresh {
		switch arg {
		case "--resume", "--conversation", "--session":
			t.Errorf("fresh argv = %v, want no resume selector: codex cannot resume", fresh)
		}
	}
	if !anyArgContains(fresh, interruptedNote(rt.StartedAt)) {
		t.Errorf("fresh argv = %v, want its prompt to carry the interrupted note", fresh)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0 (not counted)", got.RoundSwitches)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "relaunched builder (lost to a daemon restart") {
		t.Fatalf("switch entries = %+v, want the fresh relaunch note", sw)
	}
}

// TestReconcileHeadlessLostToDaemonRestartWithoutSessionRelaunchesFresh pins
// the other side of the session test (#370, spec §4.3): a lost builder whose
// round never announced a session has nothing to resume, so it takes round
// 1's fresh relaunch and the note names the relaunch.
func TestReconcileHeadlessLostToDaemonRestartWithoutSessionRelaunchesFresh(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if b.Builder.StreamSessionID != "" {
		t.Fatalf("fixture announces session %q, want none: this test pins the no-session path", b.Builder.StreamSessionID)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	rt.Watched = NewWatched()
	fr.script(b.Builder.PID, false)
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (the fresh relaunch)", len(fr.specs))
	}
	if got.BuilderCandidate != testAgyRef || got.RoundSwitches != 0 {
		t.Errorf("candidate=%q switches=%d, want the same candidate and no count", got.BuilderCandidate, got.RoundSwitches)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "relaunched builder (lost to a daemon restart") {
		t.Fatalf("switch entries = %+v, want the fresh relaunch note", sw)
	}
}

// anyArgContains reports whether any element of argv contains substring s.
// A harness whose print form does not put the prompt first (codex's "exec")
// still has exactly one element carrying the handoff text; this finds it
// without pinning the index.
func anyArgContains(argv []string, s string) bool {
	for _, arg := range argv {
		if strings.Contains(arg, s) {
			return true
		}
	}
	return false
}

// TestReconcileHeadlessLostToDaemonRestartResumeSpawnFailsFallsBackFresh pins
// spec §6's spawn failure: the resume is attempted first, its Start fails, and
// one fresh relaunch follows -- not a halt. Two Start calls, and the note
// names the relaunch.
//
// Mutation check: drop the spawnFailure fallback and this fails: the binding
// halts after the first Start and no fresh argv is ever started.
func TestReconcileHeadlessLostToDaemonRestartResumeSpawnFailsFallsBackFresh(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	const sess = "sess-lost-1"
	b.Builder.StreamSessionID = sess
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	rt.Watched = NewWatched()
	fr.script(b.Builder.PID, false)
	// Only the resume's Start fails; fakeRunner runs onStart before it reads
	// startErr, so the second call clears it and the fresh relaunch spawns.
	fr.startErr = errors.New("boom: resume refused")
	starts := 0
	fr.onStart = func() {
		starts++
		if starts == 2 {
			fr.startErr = nil
		}
	}
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if starts != 2 {
		t.Fatalf("Start calls = %d, want 2: the resume attempt, then the fresh relaunch", starts)
	}
	// The first Start is the Send that opened the round; the failed resume
	// records no spec, so the one after it is the fresh relaunch.
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2: the round's own start and the fresh relaunch", len(fr.specs))
	}
	fresh := fr.specs[1].Argv
	for _, arg := range fresh {
		switch arg {
		case "--resume":
			t.Errorf("fresh argv = %v, want no resume selector after a failed resume", fresh)
		}
	}
	if got.Builder.PID != fr.handles[1].PID || got.RoundSwitches != 0 {
		t.Errorf("pid=%d switches=%d, want the fresh handle %d and no count", got.Builder.PID, got.RoundSwitches, fr.handles[1].PID)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "relaunched builder (lost to a daemon restart") {
		t.Fatalf("switch entries = %+v, want the fresh relaunch note", sw)
	}
}

func TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "starting\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\nrelevo-exit:1\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("exit entries = %d, want 1 (the exit is still logged)", len(ex))
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, "switched builder (rate-limited: Individual quota reached") {
		t.Errorf("switch entries = %+v", sw)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 || rl[0].Source != "relevo" || rl[0].Subject != "other" {
		t.Errorf("rate_limited entries = %+v", rl)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none: the process already exited", fr.kills)
	}
}

func TestReconcileHeadlessExitWithReportOnLimitGatesAndClosesUnmarked(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := os.WriteFile(reportPath, []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "starting\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\nrelevo-exit:1\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, at(rt, time.Minute), b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("PendingForPlanner: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "Provider rate-limited: Individual quota reached") {
		t.Errorf("payload = %q, want it to contain the rate-limit sentence", pending.Payload)
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 {
		t.Errorf("rate_limited entries = %+v, want 1", rl)
	}
	if sw := switches(t, rt); len(sw) != 0 {
		t.Errorf("switch entries = %+v, want none", sw)
	}
	if len(fr.specs) != 1 {
		t.Errorf("fr.specs = %+v, want 1: no replacement spawned", fr.specs)
	}
}

func TestReconcileHeadlessStrayProcessIsKilled(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr) // no round open
	b.Builder.PID = 999
	b.Builder.StartedAt = 1_700_000_000
	b.Builder.LogPath = rt.Store.BuilderLogPath("webshop", 1)
	fr.script(999, true)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != 999 {
		t.Errorf("kills = %+v, want the stray 999", fr.kills)
	}
	if got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("process fields must clear: %+v", got.Builder)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
}

func TestReconcileHeadlessAliveErrorIsTreatedAsAlive(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.aliveErr = errors.New("ps: permission denied")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile must not fail the tick on an OS hiccup: %v", err)
	}
	if got.Round != 1 || got.State != store.StateActive || got.Builder.PID != b.Builder.PID {
		t.Errorf("round=%d state=%s pid=%d; want the round left open", got.Round, got.State, got.Builder.PID)
	}
	if len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("no exit, no switch on an OS hiccup: exits=%d specs=%d", len(exits(t, rt)), len(fr.specs))
	}
}

func TestReconcileHeadlessOpenRoundWithNoProcessIsLeftAlone(t *testing.T) {
	t.Parallel()

	// A send whose Start failed: round open, PID 0, NEEDS YOU already set.
	fr := newFakeRunner()
	fr.startErr = errors.New("agy: not found")
	rt, _ := seedHeadless(t, fr)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "x"), SendOptions{}); err == nil {
		t.Fatal("Send should have failed")
	}
	b, _ := rt.Store.Load("webshop")
	// Stage a plan entry by hand so the round reads as open the way a
	// half-started round would; the failed Send logged none.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("webshop", store.LogEntry{TS: rt.Now(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: rt.Store.PlanPath("webshop", 1), Confirmed: true})
	}); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou || got.Builder.PID != 0 || len(fr.kills) != 0 || len(exits(t, rt)) != 0 {
		t.Errorf("state=%s pid=%d kills=%d exits=%d; want NEEDS YOU left as it is", got.State, got.Builder.PID, len(fr.kills), len(exits(t, rt)))
	}
}

func TestDoneHeadlessStopsTheLiveProcess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	h := handleOf(b.Builder)

	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != h {
		t.Errorf("kills = %+v, want the round's process %+v", fr.kills, h)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateDone || got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("after done: state=%s builder=%+v; want done with process fields cleared", got.State, got.Builder)
	}
}

// N6: `relevo done` records the stop in the ledger, in the same shape
// `relevo stop` uses (builder-log spec §4.4). A live process gets one KindStop
// entry with Note "stopped/done"; an idle binding gets none.
func TestDoneRecordsTheStop(t *testing.T) {
	t.Parallel()

	t.Run("live process", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := sentHeadless(t, fr)

		if _, err := Done(context.Background(), rt, "webshop"); err != nil {
			t.Fatalf("Done: %v", err)
		}
		st := stopEntries(t, rt, "webshop")
		if len(st) != 1 {
			t.Fatalf("stop entries = %+v, want exactly one", st)
		}
		if st[0].Note != "stopped/done" || st[0].Kind != store.KindStop || !st[0].Confirmed ||
			st[0].Direction != store.DirToPlanner || st[0].Round != 1 {
			t.Errorf("stop entry = %+v, want a confirmed stopped/done KindStop on round 1", st[0])
		}
	})

	t.Run("idle binding", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := seedHeadless(t, fr)

		if _, err := Done(context.Background(), rt, "webshop"); err != nil {
			t.Fatalf("Done: %v", err)
		}
		if st := stopEntries(t, rt, "webshop"); len(st) != 0 {
			t.Errorf("stop entries = %+v, want none when the binding is idle", st)
		}
	})
}

func TestDoneHeadlessIdleKillsNothing(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("no process, no kill: %+v", fr.kills)
	}
}

func TestDoneHeadlessKillFailureStillMarksDone(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	_, err := Done(context.Background(), rt, "webshop")
	if !errors.Is(err, ErrStopFailed) || !strings.Contains(err.Error(), "marked done") {
		t.Fatalf("err = %v, want ErrStopFailed saying the binding is still marked done", err)
	}
	got, _ := rt.Store.Load("webshop")
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done even when the kill failed", got.State)
	}
	if got.Builder.PID == 0 {
		t.Error("a process relevo could not stop must stay recorded, so the human can find it")
	}
}

func TestDoneHeadlessKillFailureKeepsWorktree(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fg := &fakeGit{}
	rt.Git = fg
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	wt := t.TempDir()
	b.Worktree = wt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, "webshop")
	if !errors.Is(err, ErrStopFailed) {
		t.Fatalf("err = %v, want ErrStopFailed", err)
	}
	if res.WorktreeKept != wt {
		t.Errorf("WorktreeKept = %q, want %q", res.WorktreeKept, wt)
	}
	if res.KeptReason != "builder process still running" {
		t.Errorf("KeptReason = %q, want 'builder process still running'", res.KeptReason)
	}
	if len(fg.removeWorktreeCalls) != 0 {
		t.Errorf("RemoveWorktree calls = %d, want 0", len(fg.removeWorktreeCalls))
	}
}

func TestDoneHeadlessStopReleasesWorktree(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fg := &fakeGit{dirtyResult: false}
	rt.Git = fg

	wt := t.TempDir()
	b.Worktree = wt
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	res, err := Done(context.Background(), rt, "webshop")
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeRemoved != wt {
		t.Errorf("WorktreeRemoved = %q, want %q", res.WorktreeRemoved, wt)
	}
}

func TestUnbindHeadlessStopsTheProcessAndSaysSo(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	pid := b.Builder.PID

	res, err := Unbind(context.Background(), rt, "webshop", false)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0].PID != pid {
		t.Errorf("kills = %+v, want pid %d", fr.kills, pid)
	}
	if res.ProcessStopped != pid || res.ProcessErr != "" {
		t.Errorf("result = %+v, want ProcessStopped=%d", res, pid)
	}
	if _, err := rt.Store.Load("webshop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding still loads: %v", err)
	}
	text := UnbindText("webshop", res)
	if !strings.Contains(text, fmt.Sprintf("stopped builder process %d", pid)) {
		t.Errorf("UnbindText = %q, want the stopped line", text)
	}
}

func TestUnbindHeadlessKillFailureIsReportedNotFatal(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.killErr = errors.New("SIGTERM: operation not permitted")

	res, err := Unbind(context.Background(), rt, "webshop", true)
	if err != nil {
		t.Fatalf("Unbind must still succeed: %v", err)
	}
	if res.ProcessStopped != 0 || !strings.Contains(res.ProcessErr, "operation not permitted") {
		t.Errorf("result = %+v, want ProcessErr set and ProcessStopped 0", res)
	}
	text := UnbindText("webshop", res)
	if !strings.Contains(text, fmt.Sprintf("could not stop builder process (pid %d", b.Builder.PID)) {
		t.Errorf("UnbindText = %q, want the failure line", text)
	}
	if !res.Archived {
		t.Error("the archive still happens")
	}
}

func TestStatusHeadlessWorkingShowsPidAndLogTail(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderKind != "agy" || row.BuilderStatus != "working" {
		t.Errorf("row = kind %q status %q; want agy/working", row.BuilderKind, row.BuilderStatus)
	}
	if row.Headless == nil {
		t.Fatal("Headless info missing")
	}
	if row.Headless.PID != b.Builder.PID || row.Headless.LogPath != b.Builder.LogPath || row.Headless.ExitCode != "" {
		t.Errorf("info = %+v", *row.Headless)
	}
	if !row.Headless.StartedAt.Equal(time.Unix(b.Builder.StartedAt, 0)) {
		t.Errorf("StartedAt = %s, want %s", row.Headless.StartedAt, time.Unix(b.Builder.StartedAt, 0))
	}
	if !reflect.DeepEqual(row.Headless.Tail, []string{"l2", "l3", "l4"}) {
		t.Errorf("Tail = %q, want the last three lines", row.Headless.Tail)
	}

	text := view.RenderStatus(rep)
	for _, want := range []string{"  runner  headless       agy      working", fmt.Sprintf("pid %d since", b.Builder.PID), "`agy-m`", "  log      l2\n  log      l3\n  log      l4\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("view.RenderStatus lacks %q:\n%s", want, text)
		}
	}
}

func TestStatusHeadlessIdle(t *testing.T) {
	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderStatus != "idle" || row.Headless == nil || row.Headless.PID != 0 || len(row.Headless.Tail) != 0 {
		t.Errorf("row = %q %+v; want idle with no pid and no tail", row.BuilderStatus, row.Headless)
	}
	text := view.RenderStatus(rep)
	if strings.Contains(text, "pid ") || strings.Contains(text, "  log ") {
		t.Errorf("idle must show no pid and no log lines:\n%s", text)
	}
	if !strings.Contains(text, "  runner  headless       agy      idle") {
		t.Errorf("view.RenderStatus:\n%s", text)
	}
}

func TestStatusHeadlessExited(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	row := rep.Bindings[0]
	if row.BuilderStatus != "exited 3" || row.Headless.ExitCode != "3" {
		t.Errorf("status = %q info = %+v; want exited 3", row.BuilderStatus, row.Headless)
	}

	// No trailer: exited, code unknown.
	fr2 := newFakeRunner()
	rt2, b2 := sentHeadless(t, fr2)
	fr2.script(b2.Builder.PID, false)
	rep2, _ := Status(context.Background(), rt2)
	if rep2.Bindings[0].BuilderStatus != "exited" || rep2.Bindings[0].Headless.ExitCode != "unknown" {
		t.Errorf("no trailer: status = %q info = %+v", rep2.Bindings[0].BuilderStatus, rep2.Bindings[0].Headless)
	}
}

func TestStatusHeadlessWithoutRunnerIsUnknown(t *testing.T) {
	rt, _ := sentHeadless(t, newFakeRunner())
	rt.Runner = nil
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rep.Bindings[0].BuilderStatus != "unknown" {
		t.Errorf("status = %q, want unknown when no Runner can answer", rep.Bindings[0].BuilderStatus)
	}
}

// TestReconcileHeadlessAliveWithReportButNoMarkerWaits is the headless half
// of the fix: a running process that has written a report is still running.
// Mutation: stat the report instead of the marker -> round 2, PID cleared.

func TestReconcileHeadlessAliveWithReportButNoMarkerWaits(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || got.Builder.PID != b.Builder.PID {
		t.Errorf("round=%d pid=%d, want round 1 and the same pid: the process is still running", got.Round, got.Builder.PID)
	}
	if _, pending, _ := rt.Store.PendingForPlanner("webshop"); pending {
		t.Error("nothing is queued while the process runs without a marker")
	}
	if len(fr.kills) != 0 || len(exits(t, rt)) != 0 {
		t.Errorf("kills=%d exits=%d, want none", len(fr.kills), len(exits(t, rt)))
	}
}

// TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked: exit is a
// hard fact, so the report is trusted with the omission noted (spec §4.4).

func TestReconcileHeadlessExitedWithReportButNoMarkerClosesUnmarked(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "unmarked" {
		t.Errorf("note = %q, want unmarked", pending.Note)
	}
	if !strings.Contains(pending.Payload, "exited (code 0) after writing its report but never confirmed completion (no 001-done)") ||
		!strings.Contains(pending.Payload, "relevo show webshop --round 1 --report") {
		t.Errorf("payload = %q", pending.Payload)
	}
	if len(exits(t, rt)) != 0 || len(fr.specs) != 1 {
		t.Errorf("exits=%d specs=%d, want no exit entry and no switch: the report is the record", len(exits(t, rt)), len(fr.specs))
	}
}

// TestReconcileHeadlessMarkerClosesAndClearsTheHandle: the marker closes the
// round the same way for a process as for a pane, and the handle goes with it.

func TestReconcileHeadlessMarkerClosesAndClearsTheHandle(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 || got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("round=%d builder=%+v, want round 2 with process fields cleared", got.Round, got.Builder)
	}
	if !got.Builder.Headless() || got.Builder.AgentName != "webshop-builder" {
		t.Errorf("identity must survive: %+v", got.Builder)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found || pending.Note != "" {
		t.Errorf("want a normal report queued: found=%v note=%q err=%v", found, pending.Note, err)
	}
	if len(fr.kills) != 0 {
		t.Errorf("a builder that wrote its marker is never killed: %+v", fr.kills)
	}
}

// TestReconcileHeadlessReapsTheRoundsScopeWhenTheRunnerExited pins the exit
// route: a runner that is gone leaves its scope behind, so the daemon ends it
// before the exited region relaunches, switches or nudges under the same unit
// name.
func TestReconcileHeadlessReapsTheRoundsScopeWhenTheRunnerExited(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Errorf("scopeStops = %v, want [%s]", fr.scopeStops, unit)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the round closed", got.Round)
	}
	if got.Builder.PID != 0 {
		t.Errorf("Builder.PID = %d, want 0", got.Builder.PID)
	}
}

// TestReconcileHeadlessReapsTheRoundsScopeOnAMarkerCloseWithADeadRunner pins
// the marker route: a builder that wrote its marker and then died still holds
// its scope, so the close ends it.
func TestReconcileHeadlessReapsTheRoundsScopeOnAMarkerCloseWithADeadRunner(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}
	fr.script(b.Builder.PID, false)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Errorf("scopeStops = %v, want [%s]", fr.scopeStops, unit)
	}
	if got.Round != 2 || got.Builder.PID != 0 {
		t.Errorf("round=%d pid=%d, want round 2 with the pid cleared", got.Round, got.Builder.PID)
	}
}

// TestReconcileHeadlessMarkerCloseWithALiveRunnerReapsNothing pins the
// liveness guard: a live runner reaps its own scope on exit, so the daemon
// leaves it alone.
func TestReconcileHeadlessMarkerCloseWithALiveRunnerReapsNothing(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.scopeStops) != 0 {
		t.Errorf("scopeStops = %v, want none: the live runner reaps its own scope", fr.scopeStops)
	}
}

// TestReconcileWithScopesOffNeverProbesAScope pins the fallback: a scopes-off
// runtime never asks the runner about a scope, on the exit route or the marker
// route.
func TestReconcileWithScopesOffNeverProbesAScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.scopeQueries) != 0 {
		t.Errorf("scopeQueries = %v, want none (rt.Scope is nil)", fr.scopeQueries)
	}
	if len(fr.scopeStops) != 0 {
		t.Errorf("scopeStops = %v, want none (rt.Scope is nil)", fr.scopeStops)
	}
}

// escapeFixture seeds webshop headless with a Repo and a fake Git configured
// so a round that leaves the worktree's tree unchanged while the repo is
// dirty is detected as a worktree escape (#192): fg.snapshotTreeID is
// captured as the round's baseline by Send and compared against again at
// close, so leaving it alone between the two is what makes treeUnchanged
// hold.

func TestHeadlessMarkerCloseEscapedNote(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fg := &fakeGit{snapshotTreeID: "tree-1", dirtyResult: true}
	rt, b := escapeFixture(t, fr, fg, "/original/repo")

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2: an escape note still closes the round", got.Round)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != escapeNote {
		t.Errorf("note = %q, want %q", pending.Note, escapeNote)
	}
}

// TestHeadlessExitNoReportEscapedHalts: the tree is unchanged, the source
// repo is dirty, and the process exited without a report at all -- nothing
// suggests the builder ever touched its own tree, so relevo halts NEEDS YOU
// rather than dispatching a replacement into the same broken setup (#192).

func TestHeadlessExitNoReportEscapedHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	fg := &fakeGit{snapshotTreeID: "tree-1", dirtyResult: true}
	rt, b := escapeFixture(t, fr, fg, "/original/repo")
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you: an escape halts rather than switches", got.State)
	}
	if !strings.Contains(got.Halt, "worked outside its tree") {
		t.Errorf("Halt = %q, want it to name the escape", got.Halt)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0: an escape halt is not a switch", got.RoundSwitches)
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("switch entries = %+v, want none", switches(t, rt))
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %+v, want no second Start", fr.specs)
	}
}

// TestHeadlessExitNoReportNoRepoSwitches pins that a binding with no Repo
// (an old bind.json, a --cwd bind, or an adopted one) is untouched by #192:
// escapeCheck's precondition on b.Repo keeps the existing switch-on-exit
// behaviour exactly as it was.

func TestHeadlessExitNoReportNoRepoSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active after an ordinary switch", got.State)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 1 {
		t.Errorf("bookkeeping: cand=%q switches=%d, want %q / 1", got.BuilderCandidate, got.RoundSwitches, testClaudeRef)
	}
	if len(switches(t, rt)) != 1 {
		t.Errorf("switch entries = %+v, want 1", switches(t, rt))
	}
}

// twoBuilderJSON serves "builder" from exactly two candidates, so excluding
// both is reachable in one round -- testCandidatesJSON's third (unlisted)
// candidate would otherwise still be pickable and no halt would ever fire.
const twoBuilderJSON = `[
  {"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]},
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
]`

// TestReconcileHeadlessRoundExclusionThenAllGatedHalts is #191's end-to-end
// pin: A exits without a report and is excluded in favour of B; B then also
// exits without a report, and with every candidate serving "builder" now
// excluded, relevo halts instead of dispatching a third pick into the same
// broken round. A report that eventually appears still closes the round
// normally, and finishRound clears the exclusion with the switch count.

func TestReconcileHeadlessExitPermissionBlockedHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	_, err := Bind(context.Background(), rt, BindOptions{
		Name:      "webshop",
		Candidate: testClaudeRef,
		PlannerID: testPlannerName,
		CWD:       "/repo",
		Headless:  true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)

	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	denialText := "tool use was rejected: Bash command not allowed"
	if err := os.WriteFile(logPath, []byte("starting\n"+denialText+"\nrelevo-exit:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "permission denial") || !strings.Contains(got.Halt, `"`+denialText+`"`) {
		t.Errorf("Halt = %q, want it to contain 'permission denial' and %q", got.Halt, `"`+denialText+`"`)
	}
	ex := exits(t, rt)
	if len(ex) != 1 {
		t.Fatalf("got %d exit entries, want 1", len(ex))
	}
	wantSuffix := "; permission-blocked: " + denialText
	if !strings.HasSuffix(ex[0].Note, wantSuffix) {
		t.Errorf("exit entry Note = %q, want suffix %q", ex[0].Note, wantSuffix)
	}
	if got.RoundSwitches != b.RoundSwitches {
		t.Errorf("RoundSwitches = %d, want %d (unchanged)", got.RoundSwitches, b.RoundSwitches)
	}
	if l := loadLedger(t, rt); len(l.Entries) != 0 {
		t.Errorf("ledger entries = %+v, want none", l.Entries)
	}
	if len(fr.specs) != 1 {
		t.Errorf("specs = %d, want 1 (no new process started)", len(fr.specs))
	}
}

// TestGateHeadlessCallSiteHolds pins #132: the headless marker path holds
// the round while a configured gate runs -- no "exited without a report"
// handling (no KindExit, no switch) -- exactly as the pane call site does.

func TestGateHeadlessCallSiteHolds(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want 1: the gate holds the round open", got.Round)
	}
	if got.GateRun == nil {
		t.Fatal("GateRun must be set once the gate starts")
	}
	if len(exits(t, rt)) != 0 {
		t.Errorf("no KindExit while the gate runs: %+v", exits(t, rt))
	}
	if len(switches(t, rt)) != 0 {
		t.Errorf("no switch while the gate runs: %+v", switches(t, rt))
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
}

// TestRegateHeadlessStartsRepairProcess pins #132 part 2's intended case: a
// headless binding whose gate fails gets round N+1 started as a fresh process,
// with the repair plan as its prompt -- the same hand-off Send performs.

func TestRegateHeadlessStartsRepairProcess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Gate = "make check"
	b.Regate = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs after Send = %d, want 1", len(fr.specs))
	}

	// The round's marker closes it, and its gate fails.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile (start gate): %v", err)
	}
	if got.GateRun == nil {
		t.Fatal("the gate did not start")
	}
	pid := got.GateRun.PID
	if err := os.WriteFile(rt.Store.GateLogPath("webshop", 1), []byte("FAIL github.com/example/pkg2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(pid, false)
	fr.exit(pid, 2)

	// The gate's own process is a spec too, so the repair round is the last
	// one: three starts, the third carrying the round-2 plan.
	before := len(fr.specs)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (close gate): %v", err)
	}

	if got.Round != 2 || got.State != store.StateActive {
		t.Fatalf("round=%d state=%q, want a repair round 2, active", got.Round, got.State)
	}
	if len(fr.specs) != before+1 {
		t.Fatalf("specs = %d, want one more than %d: the repair round is a fresh process", len(fr.specs), before)
	}
	last := fr.specs[len(fr.specs)-1]
	if !strings.Contains(strings.Join(last.Argv, " "), "002-plan.md") {
		t.Errorf("repair process prompt does not name 002-plan.md: %v", last.Argv)
	}
	if got.Builder.PID == 0 {
		t.Error("the repair round's process must be recorded on the binding")
	}
	if got.RepairCount != 1 || got.LastGateSig == "" {
		t.Errorf("repairs=%d sig=%q, want 1 and a signature", got.RepairCount, got.LastGateSig)
	}
}

// claudeStreamLines is internal/usage/testdata/claude-stream.jsonl's lines,
// without their trailing newlines: the shape a claude builder's stream has,
// and the fixture TestDrainStreamRecordsSessionIDOnce feeds it.

func claudeStreamLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "usage", "testdata", "claude-stream.jsonl"))
	if err != nil {
		t.Fatalf("read claude fixture: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// sentClaudeHeadless is sentHeadless with a claude builder, so its stream is
// the claude fixture's shape -- the harness whose id arrives as session_id.

// escapeFixture seeds webshop headless with a Repo and a fake Git configured
// so a round that leaves the worktree's tree unchanged while the repo is
// dirty is detected as a worktree escape (#192): fg.snapshotTreeID is
// captured as the round's baseline by Send and compared against again at
// close, so leaving it alone between the two is what makes treeUnchanged
// hold.
func escapeFixture(t *testing.T, fr *fakeRunner, fg *fakeGit, repo string) (Runtime, store.Binding) {
	t.Helper()
	rt, b := seedHeadless(t, fr)
	rt.Git = fg
	b.Repo = repo
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.RoundBaselineTree == "" {
		t.Fatalf("baseline tree not captured; fixture assumes rt.Git is wired before Send")
	}
	return rt, b
}

func TestSendHeadlessStartsTheProcessInsteadOfPrompting(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := seedHeadless(t, fr)

	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "# do the thing"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("round = %d, want 1", res.Round)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	planPath := rt.Store.PlanPath("webshop", 1)
	reportPath := rt.Store.ReportPath("webshop", 1)
	donePath := rt.Store.DonePath("webshop", 1)
	b, _ := rt.Store.Load("webshop")
	wantPrompt := composePrompt(rt, b, planPath, reportPath, donePath)
	if spec.Argv[2] != wantPrompt {
		t.Errorf("prompt handed to the process:\n%q\nwant the composePrompt:\n%q", spec.Argv[2], wantPrompt)
	}
	if want := rt.Store.BuilderStreamPath("webshop", 1); spec.Dir != "/repo" || spec.LogPath != want {
		t.Errorf("spec = %+v", spec)
	}

	// The endpoint carries the handle; the round is stamped as today.
	if b.Builder.PID != fr.handles[0].PID || b.Builder.LogPath != spec.LogPath {
		t.Errorf("stored endpoint = %+v", b.Builder)
	}
	if b.RoundStartedAt.IsZero() || b.State != store.StateActive {
		t.Errorf("round not stamped: startedAt=%v state=%s", b.RoundStartedAt, b.State)
	}
	entries, _ := rt.Store.ReadLog("webshop")
	var plans int
	for _, e := range entries {
		if e.Kind == store.KindPlan && e.Round == 1 && e.Path == planPath {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("want exactly one plan entry for round 1, log = %+v", entries)
	}
}

func TestSwitchBuilderHeadlessStartsAProcessNotAPane(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	// Order claude first so the switch lands on a different candidate.
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	oldPID := b.Builder.PID

	got, err := switchHeadless(t, rt, b, "exited (code 3) without a report", false)
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a second Start on claude, specs = %+v", fr.specs)
	}
	if !got.Builder.Headless() || got.Builder.PID != fr.handles[1].PID || got.Builder.PID == oldPID {
		t.Errorf("new endpoint = %+v, want headless with the new pid %d", got.Builder, fr.handles[1].PID)
	}
	if got.Builder.LogPath != rt.Store.BuilderStreamPath("webshop", 1) {
		t.Errorf("LogPath = %q, want round 1's stream", got.Builder.LogPath)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 1 || got.Round != 1 || got.State != store.StateActive {
		t.Errorf("bookkeeping: cand=%q switches=%d round=%d state=%s", got.BuilderCandidate, got.RoundSwitches, got.Round, got.State)
	}
	if len(fr.kills) != 0 {
		t.Errorf("closeOld=false must not kill: %+v", fr.kills)
	}
	// #303 deleted the "switched builder to X" notification; the switch
	// log entry beside it survives and is what the human reads.
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.Contains(sw[0].Note, testClaudeRef) {
		t.Errorf("switch entries = %+v, want one naming %s", sw, testClaudeRef)
	}
	// The prompt handed to the new process is the same round's prompt.
	if !strings.Contains(fr.specs[1].Argv[2], rt.Store.PlanPath("webshop", 1)) {
		t.Errorf("new process prompt lacks the round's plan path: %q", fr.specs[1].Argv[2])
	}
}

func TestSwitchBuilderHeadlessCloseOldKillsTheProcess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	old := handleOf(b.Builder)

	if _, err := switchHeadless(t, rt, b, "rate-limited", true); err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the old handle %+v", fr.kills, old)
	}
}

func TestSwitchBuilderHeadlessStartFailureHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.startErr = errors.New("claude: not found")

	got, err := switchHeadless(t, rt, b, "exited", false)
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you when the replacement cannot start", got.State)
	}
	if got.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0", got.Builder.PID)
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d: the halt is reported once", got.HaltNotifiedRound, got.Round)
	}
}

func TestReconcileHeadlessIdleIsNotBroken(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr) // bound, nothing sent: no round open

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active: no process between rounds is normal, not broken", got.State)
	}
	if !got.BuilderMissingSince.IsZero() {
		t.Errorf("BuilderMissingSince must stay zero for a headless binding: %s", got.BuilderMissingSince)
	}
	if len(fr.specs) != 0 || len(fr.kills) != 0 {
		t.Errorf("an idle tick must do nothing: specs=%d kills=%d", len(fr.specs), len(fr.kills))
	}
	// A second idle tick, well after any grace, still does not switch.
	got, err = reconcile(t, at(rt, 5*time.Minute), got)
	if err != nil || got.State != store.StateActive || len(fr.specs) != 0 {
		t.Errorf("later idle tick: state=%s specs=%d err=%v", got.State, len(fr.specs), err)
	}
}

func TestReconcileHeadlessAliveWaits(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)

	got, err := reconcile(t, at(rt, 10*time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 || got.State != store.StateActive || got.Builder.PID != b.Builder.PID {
		t.Errorf("a live process is left alone: round=%d state=%s pid=%d", got.Round, got.State, got.Builder.PID)
	}
	if len(fr.kills) != 0 || len(fr.specs) != 1 || len(exits(t, rt)) != 0 {
		t.Errorf("nothing else may happen while it runs: kills=%d specs=%d exits=%d", len(fr.kills), len(fr.specs), len(exits(t, rt)))
	}
}

// TestReconcileHeadlessStampsStallWhenStreamQuiet pins #252's core under
// #135's shared clock: a live process whose stream file has not moved for
// stall_after_ms is stamped StalledSince = the stream's last activity, and
// nothing else happens. The second case is the mutation target: with the
// stream only 5m quiet the comparison must not fire, so inverting it (or
// comparing `<` for `>=`) makes both cases fail. The notice beside the
// stamp is gone (#303, closed-list item 4); the stamp itself and the
// builder_stalled hook event are what survive.
func TestReconcileHeadlessStampsStallWhenStreamQuiet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		quietFor time.Duration
		stalled  bool
	}{
		{"quiet past stall_after_ms", 20 * time.Minute, true},
		{"quiet under stall_after_ms", 5 * time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := newFakeRunner()
			rt, b := sentHeadless(t, fr)

			now := baseTime.Add(10 * time.Minute)
			rt = at(rt, 10*time.Minute)
			b.RoundStartedAt = now.Add(-30 * time.Minute)

			stream := rt.Store.BuilderStreamPath(b.Name, b.Round)
			if err := os.WriteFile(stream, []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644); err != nil {
				t.Fatalf("write stream: %v", err)
			}
			quietAt := now.Add(-tc.quietFor)
			if err := os.Chtimes(stream, quietAt, quietAt); err != nil {
				t.Fatalf("chtimes: %v", err)
			}

			got, err := reconcile(t, rt, b)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got.State != store.StateActive || got.Builder.PID != b.Builder.PID {
				t.Errorf("a stalled binding stays active and keeps its process: state=%s pid=%d", got.State, got.Builder.PID)
			}
			if len(fr.kills) != 0 || len(fr.specs) != 1 {
				t.Errorf("a stall is never an action: kills=%d specs=%d", len(fr.kills), len(fr.specs))
			}
			if tc.stalled {
				if !got.StalledSince.Equal(quietAt) {
					t.Errorf("StalledSince = %s, want the stream's mtime %s", got.StalledSince, quietAt)
				}
				// A second stalled tick keeps the same stamp.
				next, err := reconcile(t, at(rt, 11*time.Minute), got)
				if err != nil {
					t.Fatalf("Reconcile (second tick): %v", err)
				}
				if !next.StalledSince.Equal(quietAt) {
					t.Errorf("StalledSince after a second tick = %s, want it unchanged at %s", next.StalledSince, quietAt)
				}
			} else if !got.StalledSince.IsZero() {
				t.Errorf("StalledSince = %s, want zero: the stream is quiet but under stall_after_ms", got.StalledSince)
			}
		})
	}
}

func TestReconcileHeadlessBudgetHaltsButNeverKills(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you past the budget", got.State)
	}
	// The halt is the surviving half of the old notice: the reason on the
	// binding, and the per-round dedup.
	if !strings.Contains(got.Halt, "run past") {
		t.Errorf("Halt = %q, want it to name the budget", got.Halt)
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d", got.HaltNotifiedRound, got.Round)
	}
	if len(fr.kills) != 0 || got.Builder.PID != b.Builder.PID {
		t.Errorf("the budget never kills: kills=%+v pid=%d", fr.kills, got.Builder.PID)
	}
}

func TestReconcileHeadlessExitHaltsAfterMaxSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 2)
	b.RoundSwitches = rt.Policy.SwitchLimit()
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you at the switch limit", got.State)
	}
	if got.Halt == "" {
		t.Error("Halt is empty, want the max_switches reason recorded")
	}
	if len(fr.specs) != 1 {
		t.Errorf("no replacement may start past the limit: specs = %d", len(fr.specs))
	}
	if len(exits(t, rt)) != 1 {
		t.Error("the exit is still logged")
	}
	if got.HaltNotifiedRound != got.Round {
		t.Errorf("HaltNotifiedRound = %d, want %d: the halt is reported once", got.HaltNotifiedRound, got.Round)
	}
	if got.Builder.PID != 0 {
		t.Errorf("pid = %d, want 0 after the exit", got.Builder.PID)
	}
	// Second tick: nothing repeats.
	again, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatal(err)
	}
	if len(exits(t, rt)) != 1 || again.HaltNotifiedRound != got.HaltNotifiedRound {
		t.Errorf("a halted exit must not re-log or re-notify: exits=%d notified=%d", len(exits(t, rt)), again.HaltNotifiedRound)
	}
}

func TestReconcileHeadlessGatedKillsAndSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	old := handleOf(b.Builder)
	if _, err := availability.Unavailable(AvailabilityDeps(rt), "agy/other/m", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the gated builder's process %+v", fr.kills, old)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}
	if got.BuilderCandidate != testClaudeRef || got.RoundSwitches != 0 || got.Builder.PID != fr.handles[1].PID {
		t.Errorf("bookkeeping: cand=%q switches=%d (want 0; a gated switch is uncounted) pid=%d", got.BuilderCandidate, got.RoundSwitches, got.Builder.PID)
	}
}

func TestReconcileHeadlessBudgetOnLimitKillsAndSwitches(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	old := handleOf(b.Builder)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "working\nIndividual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s.\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.kills) != 1 || fr.kills[0] != old {
		t.Errorf("kills = %+v, want the old process %+v", fr.kills, old)
	}
	if len(fr.specs) != 2 || fr.specs[1].Argv[0] != "claude" {
		t.Fatalf("want a claude replacement: %+v", fr.specs)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	// One switch, no timeout halt: the rate limit won the tick.
	if n := len(switches(t, rt)); n != 1 {
		t.Errorf("switch entries = %d, want exactly 1 (no timeout halt)", n)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
	}
}

func TestReconcileHeadlessBudgetWithoutLimitStillHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	b.RoundTimeoutMS = 1000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("working hard\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, 2*time.Second), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !strings.Contains(got.Halt, "run past") {
		t.Errorf("Halt = %q, want the budget halt", got.Halt)
	}
	if len(fr.kills) != 0 || got.Builder.PID != b.Builder.PID {
		t.Errorf("the budget never kills: kills=%+v pid=%d", fr.kills, got.Builder.PID)
	}
	if l := loadLedger(t, rt); len(l.Entries) != 0 {
		t.Errorf("ledger entries = %+v, want none", l.Entries)
	}
}

func TestReconcileHeadlessRoundExclusionThenAllGatedHalts(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, twoBuilderJSON)
	rt.Policy = orderOf("builder", testAgyRef, testClaudeRef)

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, PlannerID: testPlannerName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Stage 1: A (agy) exits without a report -> switches to B (claude),
	// RoundExcluded == [A].
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile (A exits): %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Fatalf("candidate after switch = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testAgyRef {
		t.Fatalf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testAgyRef)
	}

	// Stage 2: B (claude) also exits without a report -> every candidate
	// serving builder is now excluded, so relevo halts instead of switching.
	fr.script(got.Builder.PID, false)
	fr.exit(got.Builder.PID, 4)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (B exits): %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "exited without a report") {
		t.Errorf("Halt = %q, want it to mention \"exited without a report\"", got.Halt)
	}
	if len(got.RoundExcluded) != 2 {
		t.Errorf("RoundExcluded = %v, want both candidates excluded", got.RoundExcluded)
	}

	// Stage 3: a report and marker eventually appear for round 1 -- the
	// marker still closes the round, and finishRound clears RoundExcluded
	// alongside RoundSwitches.
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (marker closes): %v", err)
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2", got.Round)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want nil after finishRound", got.RoundExcluded)
	}
}

// TestDrainStreamRecordsSessionIDOnce pins #147: the first drained line that
// names a session records it on the endpoint, and a later line naming another
// (a sub-agent's) leaves it alone.
func TestDrainStreamRecordsSessionIDOnce(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr) // round 1 open on a claude process
	lines := claudeStreamLines(t)
	streamWrite(t, rt, lines[0]+"\n"+lines[1]+"\n")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.StreamSessionID != "sess-1" {
		t.Fatalf("StreamSessionID = %q, want sess-1", got.Builder.StreamSessionID)
	}

	// A sub-agent's line carries a different session id; the first one stands.
	streamWrite(t, rt, `{"type":"assistant","session_id":"sub-agent-sess","message":{}}`+"\n")
	again, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if again.Builder.StreamSessionID != "sess-1" {
		t.Errorf("StreamSessionID = %q after a second session id; want the first, sess-1", again.Builder.StreamSessionID)
	}
}

// TestReportEntryCarriesHeadlessSession pins #147: the report entry of a
// closed headless round names the stream's session, and the id is cleared
// from the endpoint once the round has closed.
func TestReportEntryCarriesHeadlessSession(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr) // round 1 open on a claude process
	lines := claudeStreamLines(t)
	streamWrite(t, rt, lines[0]+"\n"+lines[1]+"\n")
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want the marker to close round 1", got.Round)
	}
	entry := roundReportEntry(t, rt, 1)
	if entry.BuilderSession == nil {
		t.Fatal("BuilderSession = nil, want {claude sess-1}")
	}
	if entry.BuilderSession.Kind != "claude" || entry.BuilderSession.ID != "sess-1" {
		t.Errorf("BuilderSession = %+v, want {claude sess-1}", *entry.BuilderSession)
	}
	if got.Builder.StreamSessionID != "" {
		t.Errorf("StreamSessionID after the close = %q, want it cleared", got.Builder.StreamSessionID)
	}
}

func TestStartProcessSetsStreamStart(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	b := store.Binding{
		Name:  "webshop",
		Round: 1,
		CWD:   t.TempDir(),
		Builder: store.Endpoint{
			Mode: store.ModeHeadless,
		},
	}
	c := candidate.Candidate{Harness: "agy"}
	argv := []string{"echo", "hi"}

	// 1. With no file -> StreamStart == 0
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		got, err := startProcess(context.Background(), rt, tx, b, argv, c)
		if err != nil {
			return err
		}
		if got.Builder.StreamStart != 0 {
			t.Errorf("StreamStart with no file = %d, want 0", got.Builder.StreamStart)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. A stream file of N bytes exists before spawn -> StreamStart == N
	streamPath := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("stream content of some bytes")
	if err := os.WriteFile(streamPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		got, err := startProcess(context.Background(), rt, tx, b, argv, c)
		if err != nil {
			return err
		}
		if got.Builder.StreamStart != int64(len(data)) {
			t.Errorf("StreamStart with file = %d, want %d", got.Builder.StreamStart, len(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSegmentKind is §7.2 N3: the last segment whose Start is at or before the
// line's offset names the harness that wrote it, and anything before the first
// Start falls back to the endpoint's own Kind.
func TestSegmentKind(t *testing.T) {
	t.Parallel()

	segs := []store.StreamSegment{{Start: 100, Kind: "agy"}, {Start: 300, Kind: "claude"}}
	cases := []struct {
		name string
		segs []store.StreamSegment
		off  int64
		want string
	}{
		{"no segments uses the fallback", nil, 0, "fallback"},
		{"empty list uses the fallback", []store.StreamSegment{}, 42, "fallback"},
		{"before the first start uses the fallback", segs, 99, "fallback"},
		{"at a start is that segment's kind", segs, 100, "agy"},
		{"between two segments is the earlier one", segs, 250, "agy"},
		{"at the second start is the second", segs, 300, "claude"},
		{"after the last is the last", segs, 9999, "claude"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := segmentKind(c.segs, c.off, "fallback"); got != c.want {
				t.Errorf("segmentKind(%+v, %d) = %q, want %q", c.segs, c.off, got, c.want)
			}
		})
	}
}

// TestCarryStream is §7.2 N4: the four cursor fields move onto the replacement
// endpoint, and the replacement's own identity fields are untouched.
func TestCarryStream(t *testing.T) {
	t.Parallel()

	from := store.Endpoint{
		AgentName:      "old-builder",
		Kind:           "agy",
		Mode:           store.ModeHeadless,
		PID:            7,
		StreamRound:    2,
		StreamStart:    512,
		StreamOffset:   600,
		StreamSegments: []store.StreamSegment{{Start: 0, Kind: "agy"}},
	}
	to := store.Endpoint{
		AgentName: "new-builder",
		Kind:      "claude",
		PaneID:    "w1:p2",
		Mode:      store.ModeHeadless,
		PID:       9,
	}

	out := carryStream(from, to)
	if out.StreamRound != from.StreamRound || out.StreamOffset != from.StreamOffset ||
		out.StreamStart != from.StreamStart || !reflect.DeepEqual(out.StreamSegments, from.StreamSegments) {
		t.Errorf("carryStream cursor = round %d offset %d start %d segs %+v; want %d/%d/%d/%+v",
			out.StreamRound, out.StreamOffset, out.StreamStart, out.StreamSegments,
			from.StreamRound, from.StreamOffset, from.StreamStart, from.StreamSegments)
	}
	if out.Kind != to.Kind || out.AgentName != to.AgentName || out.Mode != to.Mode || out.PID != to.PID {
		t.Errorf("carryStream changed the replacement's own fields: %+v, want Kind/AgentName/Mode/PID from %+v", out, to)
	}

	// The carried segment list must be a copy, not an alias of from's (§7.2 N4):
	// mutating the carried copy must not rewrite the outgoing endpoint's slice.
	out.StreamSegments[0].Kind = "changed"
	if from.StreamSegments[0].Kind != "agy" {
		t.Errorf("carryStream aliases the segment slice: mutating the copy changed from.StreamSegments to %+v", from.StreamSegments)
	}
}

// TestStartProcessAppendsSegments is §7.2 N5: each spawn records the byte
// offset it writes from and its harness kind; a retried spawn at the same
// offset replaces its predecessor's segment rather than appending a second
// one there, and a later round starts a fresh list.
func TestStartProcessAppendsSegments(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	argv := []string{"echo", "hi"}
	spawn := func(t *testing.T, b store.Binding, kind string) store.Binding {
		t.Helper()
		var got store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			got, err = startProcess(context.Background(), rt, tx, b, argv, candidate.Candidate{Harness: kind})
			return err
		})
		if err != nil {
			t.Fatalf("startProcess(%s): %v", kind, err)
		}
		return got
	}
	b := store.Binding{
		Name:  "webshop",
		Round: 1,
		CWD:   t.TempDir(),
		Builder: store.Endpoint{
			Mode: store.ModeHeadless,
			Kind: "agy",
		},
	}

	// 1. The round's first spawn in an empty round: one segment at 0.
	got := spawn(t, b, "agy")
	if want := []store.StreamSegment{{Start: 0, Kind: "agy"}}; !reflect.DeepEqual(got.Builder.StreamSegments, want) {
		t.Errorf("first-spawn segments = %+v, want %+v", got.Builder.StreamSegments, want)
	}

	// 2. N bytes on the stream, then a second spawn in the same round with a
	// different kind: two segments, the second at N.
	first := []byte(`{"event":"init","init":{}}` + "\n")
	streamPath := rt.Store.BuilderStreamPath("webshop", 1)
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(streamPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	got.Builder.Kind = "claude"
	got = spawn(t, got, "claude")
	want := []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(first)), Kind: "claude"}}
	if !reflect.DeepEqual(got.Builder.StreamSegments, want) {
		t.Errorf("same-round switch segments = %+v, want %+v", got.Builder.StreamSegments, want)
	}

	// 3. A retried spawn at the same offset replaces its predecessor.
	got.Builder.Kind = "opencode"
	got = spawn(t, got, "opencode")
	want = []store.StreamSegment{{Start: 0, Kind: "agy"}, {Start: int64(len(first)), Kind: "opencode"}}
	if !reflect.DeepEqual(got.Builder.StreamSegments, want) {
		t.Errorf("retried spawn segments = %+v, want the same-Start entry replaced: %+v", got.Builder.StreamSegments, want)
	}

	// 4. A later round starts a fresh list with one segment.
	got.Round = 2
	got.Builder.Kind = "claude"
	later := spawn(t, got, "claude")
	if want := []store.StreamSegment{{Start: 0, Kind: "claude"}}; !reflect.DeepEqual(later.Builder.StreamSegments, want) {
		t.Errorf("later-round segments = %+v, want a fresh %+v", later.Builder.StreamSegments, want)
	}
}

// TestDrainRendersEachSegmentWithItsKind is §7.2 N6: a round whose stream has
// two processes' bytes -- agy first, then claude -- renders each with the kind
// that wrote it, even though the endpoint's own Kind is the later one. Before
// this, the whole file was re-rendered with the new kind.
func TestDrainRendersEachSegmentWithItsKind(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	agyLines := agyToolActive + agyToolDone
	claudeLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	streamWrite(t, rt, agyLines+claudeLine)

	b.Builder.Kind = "claude"
	b.Builder.StreamRound = 1
	b.Builder.StreamOffset = 0
	b.Builder.StreamStart = 0
	b.Builder.StreamSegments = []store.StreamSegment{
		{Start: 0, Kind: "agy"},
		{Start: int64(len(agyLines)), Kind: "claude"},
	}

	got := drainStream(rt, b)
	want := "● run_command go test ./...\n  ⎿ ok\nhi\n"
	if readLog(t, rt) != want {
		t.Errorf("log = %q, want %q (each line rendered with its own segment's kind)", readLog(t, rt), want)
	}
	if wantOff := int64(len(agyLines) + len(claudeLine)); got.Builder.StreamOffset != wantOff {
		t.Errorf("offset = %d, want the whole file %d", got.Builder.StreamOffset, wantOff)
	}

	// Draining again appends nothing: every line was rendered exactly once.
	again := drainStream(rt, got)
	if readLog(t, rt) != want {
		t.Errorf("a second drain changed the log: %q", readLog(t, rt))
	}
	if again.Builder.StreamOffset != got.Builder.StreamOffset {
		t.Errorf("offset moved on an empty drain: %d -> %d", got.Builder.StreamOffset, again.Builder.StreamOffset)
	}
}

// TestDrainSessionIDComesOnlyFromTheCurrentProcess is the test M7 needs
// (§7.4): an undrained line from the round's earlier process must not set
// StreamSessionID once the cursor is carried over. Only bytes at or past
// StreamStart -- the current process's own -- may name the session.
func TestDrainSessionIDComesOnlyFromTheCurrentProcess(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	old := `{"event":"init","conversation_id":"old-sess","init":{}}` + "\n"
	current := `{"type":"assistant","session_id":"new-sess","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	streamWrite(t, rt, old+current)

	b.Builder.Kind = "claude"
	b.Builder.StreamRound = 1
	b.Builder.StreamOffset = 0 // the old process's bytes were never drained
	b.Builder.StreamStart = int64(len(old))
	b.Builder.StreamSegments = []store.StreamSegment{
		{Start: 0, Kind: "agy"},
		{Start: int64(len(old)), Kind: "claude"},
	}

	got := drainStream(rt, b)
	if got.Builder.StreamSessionID != "new-sess" {
		t.Errorf("StreamSessionID = %q, want new-sess; only the current process's bytes (off >= StreamStart) may set it", got.Builder.StreamSessionID)
	}
	if want := "hi\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
}

// TestStatusExitCodeReadsTheStream is §7.2 N7: `relevo status` asks the
// Runner for the exit code of the round's stream file, not of the log. The
// trailer is what records the code, and it lives in the stream; the log may
// not have drained it (or a later marker may have replaced it).
func TestStatusExitCodeReadsTheStream(t *testing.T) {
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)

	if _, err := Status(context.Background(), rt); err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := rt.Store.BuilderStreamPath("webshop", 1)
	if len(fr.exitPaths) == 0 || fr.exitPaths[0] != want {
		t.Errorf("ExitCode was asked about %v, want the round's stream %s", fr.exitPaths, want)
	}
	if logPath := rt.Store.BuilderLogPath("webshop", 1); len(fr.exitPaths) > 0 && fr.exitPaths[0] == logPath {
		t.Errorf("ExitCode read the log path %s; status must read the stream", logPath)
	}
}

// N1: a new round writes no builder.log. The spec's LogPath is the round's
// stream, so the harness's stderr joins its stdout, and the endpoint points
// there too (builder-log spec §4.4).
func TestStartProcessSendsStderrToTheStream(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)

	got, err := startRound(context.Background(), rt, nil, b, "the prompt")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	spec := fr.specs[0]
	want := rt.Store.BuilderStreamPath("webshop", 1)
	if spec.LogPath != want || spec.StreamPath != want {
		t.Errorf("spec LogPath/StreamPath = %q/%q, want %q", spec.LogPath, spec.StreamPath, want)
	}
	if got.Builder.LogPath != want {
		t.Errorf("b.Builder.LogPath = %q, want the stream %q", got.Builder.LogPath, want)
	}
	if _, err := os.Stat(rt.Store.BuilderLogPath("webshop", 1)); !os.IsNotExist(err) {
		t.Errorf("a new round must write no builder.log; stat err = %v", err)
	}
}

// N2: a round that already had a NNN-builder.log when the process started --
// history, or a round in flight across the upgrade -- keeps writing stderr to
// that log (builder-log spec §4.5).
func TestStartProcessKeepsALegacyRoundsLog(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	legacy := rt.Store.BuilderLogPath("webshop", 1)
	seedLegacyLog(t, rt, "webshop", 1)

	got, err := startRound(context.Background(), rt, nil, b, "the prompt")
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	if fr.specs[0].LogPath != legacy || got.Builder.LogPath != legacy {
		t.Errorf("spec/endpoint LogPath = %q/%q, want the legacy log %q", fr.specs[0].LogPath, got.Builder.LogPath, legacy)
	}
}

// N3: for a new round the drain writes no log. It still advances the cursor
// past every complete line and captures the session id (builder-log spec §4.4).
func TestDrainWritesNoLogForANewRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	announce := `{"event":"init","conversation_id":"new-sess","init":{}}` + "\n"
	streamWrite(t, rt, announce+agyToolActive)

	got := drainStream(rt, b)
	if _, err := os.Stat(rt.Store.BuilderLogPath("webshop", 1)); !os.IsNotExist(err) {
		t.Errorf("drainStream created a builder.log for a new round: stat err = %v", err)
	}
	if want := int64(len(announce + agyToolActive)); got.Builder.StreamOffset != want {
		t.Errorf("StreamOffset = %d, want the whole stream %d", got.Builder.StreamOffset, want)
	}
	if got.Builder.StreamSessionID != "new-sess" {
		t.Errorf("StreamSessionID = %q, want new-sess from the stream's own init line", got.Builder.StreamSessionID)
	}
}

// N4: a legacy round keeps its log: the drain appends the rendered lines to it,
// exactly as before (builder-log spec §4.4).
func TestDrainKeepsAppendingALegacyLog(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	seedLegacyLog(t, rt, "webshop", 1)
	streamWrite(t, rt, agyToolActive+agyToolDone)

	got := drainStream(rt, b)
	if want := "\u25cf run_command go test ./...\n  \u23bf ok\n"; readLog(t, rt) != want {
		t.Errorf("log = %q, want %q", readLog(t, rt), want)
	}
	if want := int64(len(agyToolActive + agyToolDone)); got.Builder.StreamOffset != want {
		t.Errorf("StreamOffset = %d, want the whole stream %d", got.Builder.StreamOffset, want)
	}
}

// N7: stderr-only agy limits must survive the move of stderr into the stream
// (builder-log spec §4.4, item 2): builderTail reads the raw stderr line out of
// the rendered stream, and the limit scan still matches it.
func TestStderrLimitTextStillDetected(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	const stderrLine = "error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 1h0m0s."
	streamWrite(t, rt, agyToolActive+agyToolDone+stderrLine+"\nrelevo-exit:1\n")

	tail := builderTail(rt, b, availability.LimitScanLines)
	if !strings.Contains(tail, stderrLine) {
		t.Fatalf("builderTail = %q, want it to contain the raw stderr line %q", tail, stderrLine)
	}
	if _, ok := availability.MatchLimit(tail, availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate), rt.Now(), 0); !ok {
		t.Errorf("availability.MatchLimit(%q, agy patterns) did not match; an stderr-only limit must survive the move", tail)
	}
}

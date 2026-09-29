package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// uiBuilderRow is the custom writer row this file's tests add: shape writer,
// a claude definition of my-ui, and its own candidate list. A round it runs
// proves the launch came from the role, not the built-in builder.
func uiBuilderRow(candidates ...string) roles.Row {
	return roles.Row{
		Shape:       ptr("writer"),
		Candidates:  candidates,
		Definitions: map[string]roles.DefRow{"claude": {Agent: "my-ui"}},
	}
}

// TestBindingRole pins #382 §5.1: one accessor names the writer role a binding
// runs. An empty stored role and the literal "builder" both mean builder, and
// normRole is the inverse for the stored form.
func TestBindingRole(t *testing.T) {
	t.Parallel()

	if got := bindingRole(store.Binding{}); got != "builder" {
		t.Errorf("bindingRole(Role \"\") = %q, want builder", got)
	}
	if got := bindingRole(store.Binding{Role: "builder"}); got != "builder" {
		t.Errorf("bindingRole(Role builder) = %q, want builder", got)
	}
	if got := bindingRole(store.Binding{Role: "ui-builder"}); got != "ui-builder" {
		t.Errorf("bindingRole(Role ui-builder) = %q, want ui-builder", got)
	}

	if got := normRole("builder"); got != "" {
		t.Errorf("normRole(builder) = %q, want empty", got)
	}
	if got := normRole(""); got != "" {
		t.Errorf("normRole(\"\") = %q, want empty", got)
	}
	if got := normRole("ui-builder"); got != "ui-builder" {
		t.Errorf("normRole(ui-builder) = %q, want ui-builder", got)
	}
}

// TestActorShape pins A5 §2: a writer role (or the builder default) reads as
// a writer, a reader as a reader, and an unknown name is refused with
// ErrUnknownRole.
func TestActorShape(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":    {Candidates: []string{testClaudeRef}},
		"ui-builder": uiBuilderRow(testClaudeRef),
	})

	for _, role := range []string{"builder", "", "ui-builder"} {
		shape, err := actorShape(reg, role)
		if err != nil {
			t.Errorf("actorShape(%q) = %v, want nil", role, err)
			continue
		}
		if shape != store.ShapeWriter {
			t.Errorf("actorShape(%q) = %q, want writer", role, shape)
		}
	}

	shape, err := actorShape(reg, "reviewer")
	if err != nil {
		t.Fatalf("actorShape(reviewer) err = %v, want nil", err)
	}
	if shape != store.ShapeReader {
		t.Errorf("actorShape(reviewer) = %q, want reader", shape)
	}

	if _, err := actorShape(reg, "nope"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("actorShape(nope) err = %v, want ErrUnknownRole", err)
	}
}

// TestBindUnknownRoleRefused pins #382 §6: an unknown role is refused before
// any candidate resolution or launch. The plan asked for this through the
// cmd/relevo CLI, but cmdBind resolves this session's mastermind (in BindResolved)
// before create runs actorShape, so a CLI run without a mastermind stops on
// ErrNoMasterMindSession -- the role refusal is only reachable through
// relevo.Bind, which is what this test drives (the plan's §7 test 3 fallback).
func TestBindUnknownRoleRefused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef}},
	})

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "n", Role: "nope", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/nope-repo",
	})
	if err == nil {
		t.Fatal("Bind(--actor nope) = nil, want an error")
	}
	if !strings.Contains(err.Error(), `unknown actor "nope"`) {
		t.Errorf("err = %q, want it to name unknown actor \"nope\"", err.Error())
	}
}

// TestBindCustomWriterLaunchesItsDefinition pins #382 §2: a binding's round
// runs its own role's definition, and the role is persisted.
func TestBindCustomWriterLaunchesItsDefinition(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	set := rt.Candidates
	rt.Registry = rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":    {Candidates: []string{testClaudeRef}},
		"ui-builder": uiBuilderRow(testClaudeRef),
	})

	fr := newFakeRunner()
	rt.Runner = fr
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "ui-bound", Role: "ui-builder", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/ui-repo", Headless: true,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := startRound(context.Background(), rt, nil, b, "the prompt"); err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %+v, want one Start", fr.specs)
	}
	if !containsAdjacentPair(fr.specs[0].Argv, "--agent", "my-ui") {
		t.Errorf("argv = %v, want --agent my-ui", fr.specs[0].Argv)
	}

	stored, err := rt.Store.Load("ui-bound")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Role != "ui-builder" {
		t.Errorf("stored Role = %q, want ui-builder", stored.Role)
	}
}

// TestBindCustomWriterPicksFromItsList pins #382 §2: with a custom writer role
// and no --candidate, the pick comes from the role's own candidates list, not
// the builder's.
func TestBindCustomWriterPicksFromItsList(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, rolesRuntimeCandidatesJSON)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder":    {Candidates: []string{"claude/test/a"}},
		"ui-builder": uiBuilderRow("claude/test/b"),
	})

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "picked", Role: "ui-builder", MasterMindID: testMasterMindName, CWD: "/picked-repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.BuilderCandidate != "claude/test/b" {
		t.Errorf("BuilderCandidate = %q, want claude/test/b (ui-builder's own list)", b.BuilderCandidate)
	}
}

// TestBindAReaderRecordsItsShape pins A5 §1 and §2: a reader actor can be
// bound locally, and the binding records shape "reader".
func TestBindAReaderRecordsItsShape(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "reader-bind", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/reader-repo",
	})
	if err != nil {
		t.Fatalf("Bind(--actor reviewer): %v", err)
	}
	if b.Shape != store.ShapeReader {
		t.Errorf("Shape = %q, want reader", b.Shape)
	}
	stored, err := rt.Store.Load("reader-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Shape != store.ShapeReader {
		t.Errorf("stored Shape = %q, want reader", stored.Shape)
	}
}

// TestReaderSharesAWritersTree pins A5 §3: a reader is never blocked by, and
// never blocks, another binding on the same CWD, while two writers are still
// refused.
func TestReaderSharesAWritersTree(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "writer", Candidate: testClaudeRef, MasterMindID: testMasterMindName, CWD: "/shared-tree",
	}); err != nil {
		t.Fatalf("bind writer: %v", err)
	}

	reader, err := Bind(context.Background(), rt, BindOptions{
		Name: "reviewer-bind", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/shared-tree",
	})
	if err != nil {
		t.Fatalf("bind a reader on a writer's tree: %v", err)
	}
	if reader.CWD != "/shared-tree" {
		t.Errorf("reader CWD = %q, want the writer's tree", reader.CWD)
	}

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "writer2", Candidate: testClaudeRef, MasterMindID: testMasterMindName, CWD: "/shared-tree",
	}); !errors.Is(err, store.ErrCWDTaken) {
		t.Fatalf("second writer err = %v, want ErrCWDTaken", err)
	}
}

// TestReaderRefusesGateRegateAndVerify pins A5 §2: the writer-only knobs are
// refused for a reader, each error naming its flag, and the policy defaults
// gate.default and verify.default do not reach a reader binding.
func TestReaderRefusesGateRegateAndVerify(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}
	rt.Policy.Verify = &policy.VerifyPolicy{Default: true}

	_, err := Bind(context.Background(), rt, BindOptions{
		Name: "rev-gate", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/rev-gate", Gate: "make check",
	})
	if err == nil || !strings.Contains(err.Error(), "--gate") || !strings.Contains(err.Error(), "a reader round has no check") {
		t.Fatalf("Bind(--gate on a reader) = %v, want the --gate refusal", err)
	}

	_, err = Bind(context.Background(), rt, BindOptions{
		Name: "rev-regate", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/rev-regate", Regate: ptr(3),
	})
	if err == nil || !strings.Contains(err.Error(), "--regate") || !strings.Contains(err.Error(), "a reader round has no check") {
		t.Fatalf("Bind(--regate on a reader) = %v, want the --regate refusal", err)
	}

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "rev-plain", Role: "reviewer", Candidate: testClaudeRef,
		MasterMindID: testMasterMindName, CWD: "/rev-plain",
	})
	if err != nil {
		t.Fatalf("Bind(a plain reader): %v", err)
	}
	if b.Gate != "" {
		t.Errorf("reader Gate = %q, want gate.default not to apply", b.Gate)
	}
	if b.RoundVerify {
		t.Error("reader RoundVerify = true, want verify.default not to apply")
	}

	_, err = Send(context.Background(), rt, "rev-plain", writePlan(t, "do it"), SendOptions{Verify: ptr(true)})
	if err == nil || !strings.Contains(err.Error(), "--verify") || !strings.Contains(err.Error(), "a reader round has no check") {
		t.Fatalf("Send(--verify on a reader) = %v, want the --verify refusal", err)
	}
}

// TestRemoteReaderRefusedByAPreReadersServer pins #607's client rule: a
// remote add of a reader is refused, with the old local-only wording, when the
// server does not advertise remote.FeatureReaders -- after the WhoAmI probe,
// and before any create call.
func TestRemoteReaderRefusedByAPreReadersServer(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{}
	rt.Remote = fr

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "remote-reader", Role: "reviewer", Server: "s",
		MasterMindID: testMasterMindName, Repo: "/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "reader actors run locally only; bind without --server") {
		t.Fatalf("Add(--server --actor reviewer) = %v, want the reader refusal", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Errorf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
}

// TestGateFollowsRole pins #382 §5.1's gate rule: a writer with gate false
// takes no policy default, a writer with no gate key takes it, builder keeps
// taking it, and an explicit --gate still wins.
func TestGateFollowsRole(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Policy.Gate = &policy.GatePolicy{Default: "make check"}

	noGateWriter := roles.Row{
		Shape:       ptr("writer"),
		Check:       ptr(false),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "my-ui"}},
	}
	defaultGateWriter := roles.Row{
		Shape:       ptr("writer"),
		Candidates:  []string{testClaudeRef},
		Definitions: map[string]roles.DefRow{"claude": {Agent: "my-writer"}},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, map[string]roles.Row{
		"builder":      {Candidates: []string{testClaudeRef}},
		"ui-builder":   noGateWriter,
		"plain-writer": defaultGateWriter,
	})

	bind := func(name, role, gate string) store.Binding {
		t.Helper()
		b, err := Bind(context.Background(), rt, BindOptions{
			Name: name, Role: role, Candidate: testClaudeRef,
			MasterMindID: testMasterMindName, CWD: "/" + name, Gate: gate,
		})
		if err != nil {
			t.Fatalf("Bind(%s, role %q): %v", name, role, err)
		}
		return b
	}

	if b := bind("no-gate", "ui-builder", ""); b.Gate != "" {
		t.Errorf("gate:false writer Gate = %q, want empty", b.Gate)
	}
	if b := bind("default-gate", "plain-writer", ""); b.Gate != "make check" {
		t.Errorf("writer with no gate key Gate = %q, want the policy default", b.Gate)
	}
	if b := bind("builder-gate", "", ""); b.Gate != "make check" {
		t.Errorf("builder Gate = %q, want the policy default", b.Gate)
	}
	if b := bind("explicit-gate", "ui-builder", "make x"); b.Gate != "make x" {
		t.Errorf("explicit --gate Gate = %q, want make x", b.Gate)
	}
}

// TestSwitchPicksFromRoleList pins #382 §5.1: a mid-round switch takes the
// next candidate from the binding's own role's list, never a builder-only one.
//
// The candidates sit on distinct providers so the rate limit gates only the
// one that ran.
func TestSwitchPicksFromRoleList(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, `[
	  {"harness":"claude","provider":"p1","model":"b","roles":["builder"]},
	  {"harness":"claude","provider":"p2","model":"c","roles":["builder"]},
	  {"harness":"claude","provider":"p3","model":"a","roles":["builder"]}
	]`)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder":    {Candidates: []string{"claude/p3/a"}},
		"ui-builder": uiBuilderRow("claude/p1/b", "claude/p2/c"),
	})
	fr := newFakeRunner()
	rt.Runner = fr

	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Role: "ui-builder", Candidate: "claude/p1/b",
		MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
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
	if b.BuilderCandidate != "claude/p1/b" {
		t.Fatalf("BuilderCandidate = %q, want claude/p1/b", b.BuilderCandidate)
	}

	if _, err := availability.Unavailable(AvailabilityDeps(rt), "claude/p1/b", time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != "claude/p2/c" {
		t.Errorf("BuilderCandidate = %q, want claude/p2/c from ui-builder's list", got.BuilderCandidate)
	}
	if got.BuilderCandidate == "claude/p3/a" {
		t.Error("switched to claude/p3/a, which only the builder row lists")
	}
}

// TestVanishedRoleFailsRoundStart pins #382 §5.4: a binding whose role
// roles.json no longer defines fails at round start with ErrUnknownRole, and
// no process is started -- there is no fallback to builder.
func TestVanishedRoleFailsRoundStart(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	fr := newFakeRunner()
	rt.Runner = fr

	if err := rt.Store.Save(store.Binding{
		Name: "gone-bind", CWD: "/gone-repo", MasterMindID: testMasterMindID,
		BuilderCandidate: testClaudeRef, Role: "gone",
		Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := rt.Store.Load("gone-bind")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = startRound(context.Background(), rt, nil, b, "the prompt")
	if err == nil {
		t.Fatal("startRound on a vanished role = nil, want an error")
	}
	if !errors.Is(err, ErrUnknownRole) {
		t.Errorf("err = %v, want ErrUnknownRole", err)
	}
	if !strings.Contains(err.Error(), "which config actors no longer defines") {
		t.Errorf("err = %q, want it to name the vanished role", err.Error())
	}
	if len(fr.specs) != 0 {
		t.Errorf("startRound started %d processes for a vanished role", len(fr.specs))
	}
}

// TestAddCustomRoleOnServerRefused pins #382 §4 (round 3; this is round 1's
// test ported): a remote add with a custom role is refused before any binding
// is created on the server, because the server does not advertise
// remote.FeatureRoles -- an old server would ignore the field and run its
// builder. The client's own registry knows ui-builder, which must not matter:
// the server's roles never come from here.
func TestAddCustomRoleOnServerRefused(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder":    {Candidates: []string{testClaudeRef}},
		"ui-builder": uiBuilderRow(testClaudeRef),
	})
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{}} // Features nil: a server without roles
	rt.Remote = fr

	_, err := Add(context.Background(), rt, AddOptions{
		Name: "remote-ui", Role: "ui-builder", Server: "s",
		MasterMindID: testMasterMindName, Repo: "/repo",
	})
	if err == nil {
		t.Fatal("Add(--server with a custom role) = nil, want the upgrade-it error")
	}
	want := `server s does not run custom actors (actor "ui-builder"); upgrade it`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
}

// TestStatusShowsRole pins #382 §4 and §5: a non-builder actor is on the
// status row and printed on the runner line, while a builder row's runner line
// has no actor suffix and its JSON names the actor "builder".
func TestStatusShowsRole(t *testing.T) {
	rt := newRuntime(t)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef}},
		"designer": uiBuilderRow(testClaudeRef),
	})

	if err := rt.Store.Save(store.Binding{
		Name: "designer-status", CWD: "/designer-status-repo",
		Builder:          store.Endpoint{Kind: "claude", Mode: store.ModeHeadless},
		BuilderCandidate: testClaudeRef, Role: "designer",
		Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save(designer): %v", err)
	}
	if err := rt.Store.Save(store.Binding{
		Name: "plain-status", CWD: "/plain-status-repo",
		Builder:          store.Endpoint{Kind: "claude", Mode: store.ModeHeadless},
		BuilderCandidate: testClaudeRef,
		Round:            1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save(plain): %v", err)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	rows := map[string]view.BindingStatus{}
	for _, row := range rep.Bindings {
		rows[row.Name] = row
	}
	if rows["designer-status"].Role != "designer" {
		t.Errorf("designer-status row Role = %q, want designer", rows["designer-status"].Role)
	}
	if rows["plain-status"].Role != "builder" {
		t.Errorf("plain-status row Role = %q, want builder", rows["plain-status"].Role)
	}

	designerOnly := view.RenderStatus(view.Report{Bindings: []view.BindingStatus{rows["designer-status"]}})
	if !strings.Contains(designerOnly, "actor designer") {
		t.Errorf("designer row line = %q, want `actor designer`", designerOnly)
	}
	plainOnly := view.RenderStatus(view.Report{Bindings: []view.BindingStatus{rows["plain-status"]}})
	if strings.Contains(plainOnly, "actor ") {
		t.Errorf("builder row line = %q, want no actor suffix", plainOnly)
	}

	plainJSON, err := json.Marshal(rows["plain-status"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainJSON), `"role"`) {
		t.Errorf("builder row JSON = %s, want no role key", plainJSON)
	}
	if !strings.Contains(string(plainJSON), `"actor":"builder"`) {
		t.Errorf("builder row JSON = %s, want an actor key of builder", plainJSON)
	}
	designerJSON, err := json.Marshal(rows["designer-status"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(designerJSON), `"actor":"designer"`) {
		t.Errorf("designer row JSON = %s, want an actor key", designerJSON)
	}
}

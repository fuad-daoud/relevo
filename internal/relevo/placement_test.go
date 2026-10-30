package relevo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

func rowsWithPlacement(placement ...string) map[string]roles.Row {
	return map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef, testAgyRef}, Placement: placement},
	}
}

func placementAddRuntime(t *testing.T, fr *fakeRemote, placement ...string) (Runtime, *store.Store) {
	t.Helper()
	rt := newTestRuntime(t, &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	})
	rt.Remote = fr
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rowsWithPlacement(placement...))
	return rt, rt.Store
}

func TestAddPlacementFallsThroughToLocal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{whoAmIErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt, st := placementAddRuntime(t, fr, "zen", "local")

	res, err := Add(ctx, rt, AddOptions{Name: "api", Repo: "/fake/repo", MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Binding.Builder.Server != "" || !res.Binding.Builder.Headless() {
		t.Fatalf("builder = %+v, want the local headless path", res.Binding.Builder)
	}
	want := resolve.PlacementResolution{
		Name: "local", How: resolve.PlacementActor,
		Skipped: []resolve.PlacementSkip{{Name: "zen", Reason: "unreachable"}},
	}
	if !reflect.DeepEqual(res.Resolution.Placement, want) {
		t.Fatalf("placement = %+v, want %+v", res.Resolution.Placement, want)
	}

	// The pick log explains why the binding landed here, not on the server the
	// actor asked for first.
	note := lastPickNote(t, st, "api")
	wantNote := "; placement local (actor); skipped zen (unreachable)"
	if !strings.HasSuffix(note, wantNote) {
		t.Fatalf("pick note = %q, want it to end with %q", note, wantNote)
	}
}

func TestAddPlacementPicksTheServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:        remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
		createBindingResp: remote.BindingView{Name: "api", Candidate: testClaudeRef, Tier: "edit"},
	}
	rt, _ := placementAddRuntime(t, fr, "zen", "local")

	res, err := Add(ctx, rt, AddOptions{Name: "api", Repo: "/fake/repo", MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Binding.Builder.Server != "zen" || res.Binding.Builder.Mode != store.ModeRemote {
		t.Fatalf("builder = %+v, want the remote path on zen", res.Binding.Builder)
	}
	want := resolve.PlacementResolution{Name: "zen", How: resolve.PlacementActor}
	if !reflect.DeepEqual(res.Resolution.Placement, want) {
		t.Fatalf("placement = %+v, want %+v", res.Resolution.Placement, want)
	}
	if !slicesContains(fr.calls, "Actor:zen:builder:") {
		t.Fatalf("calls = %v, want the actor probe on zen", fr.calls)
	}
}

func TestAddPlacementPrefersTheServerWhenLocalIsGated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "api", Candidate: testClaudeRef}}
	rt, _ := placementAddRuntime(t, fr, "local", "zen")
	if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: []availability.Entry{{
		Kind: availability.RateLimited, Subject: "test", At: baseTime, Source: "relevo",
	}}}); err != nil {
		t.Fatalf("SaveLedger: %v", err)
	}

	res, err := Add(ctx, rt, AddOptions{Name: "api", Repo: "/fake/repo", MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Binding.Builder.Server != "zen" {
		t.Fatalf("builder = %+v, want the remote path on zen", res.Binding.Builder)
	}
	if len(res.Resolution.Placement.Skipped) != 1 || res.Resolution.Placement.Skipped[0].Name != "local" {
		t.Fatalf("skipped = %+v, want local skipped", res.Resolution.Placement.Skipped)
	}
	if reason := res.Resolution.Placement.Skipped[0].Reason; !strings.Contains(reason, "is gated") {
		t.Fatalf("local skip reason = %q, want the gate refusal", reason)
	}
}

func TestAddExplicitServerNeverFallsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "api", Candidate: testClaudeRef}}
	rt, _ := placementAddRuntime(t, fr, "backup")

	res, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo", MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if res.Binding.Builder.Server != "zen" {
		t.Fatalf("builder = %+v, want zen", res.Binding.Builder)
	}
	want := resolve.PlacementResolution{Name: "zen", How: resolve.PlacementExplicit}
	if !reflect.DeepEqual(res.Resolution.Placement, want) {
		t.Fatalf("placement = %+v, want %+v", res.Resolution.Placement, want)
	}
	for _, c := range fr.calls {
		if strings.Contains(c, "backup") {
			t.Fatalf("calls = %v, want no attempt on the actor's own placement", fr.calls)
		}
	}
}

func TestAddLocalIgnoresTheActorsPlacement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{}
	rt, _ := placementAddRuntime(t, fr, "zen")

	res, err := Add(ctx, rt, AddOptions{Name: "api", Repo: "/fake/repo", Local: true, MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("Add --local: %v", err)
	}
	if res.Binding.Builder.Server != "" {
		t.Fatalf("builder = %+v, want the local path", res.Binding.Builder)
	}
	want := resolve.PlacementResolution{Name: "local", How: resolve.PlacementExplicit}
	if !reflect.DeepEqual(res.Resolution.Placement, want) {
		t.Fatalf("placement = %+v, want %+v", res.Resolution.Placement, want)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v, want no remote call at all", fr.calls)
	}
}

func TestBindResolvedPlacesARemoteActor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt, _ := placementAddRuntime(t, &fakeRemote{
		createBindingResp: remote.BindingView{Name: "api", Candidate: testClaudeRef},
	}, "zen")

	b, res, err := BindResolved(ctx, rt, BindOptions{Name: "api", CWD: "/repo", MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if b.Builder.Server != "zen" || b.Builder.Mode != store.ModeRemote {
		t.Fatalf("builder = %+v, want the remote path on zen", b.Builder)
	}
	if !reflect.DeepEqual(res.Placement, resolve.PlacementResolution{Name: "zen", How: resolve.PlacementActor}) {
		t.Fatalf("placement = %+v, want zen chosen from the actor's list", res.Placement)
	}
}

func TestBindResolvedWithoutAPlacementKeepsTodayPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newTestRuntime(t, &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	})

	b, res, err := BindResolved(ctx, rt, BindOptions{Name: "api", CWD: "/repo", Candidate: testClaudeRef, MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("BindResolved: %v", err)
	}
	if b.Builder.Server != "" || !b.Builder.Headless() {
		t.Fatalf("builder = %+v, want the local headless path", b.Builder)
	}
	if !reflect.DeepEqual(res.Placement, resolve.PlacementResolution{}) {
		t.Fatalf("placement = %+v, want the zero resolution", res.Placement)
	}
	if got, want := resolve.ExplainResolution("builder", res), "picked claude/test/m for builder: explicit, policy bypassed"; got != want {
		t.Fatalf("pick note = %q, want %q", got, want)
	}
}

func TestResumeKeepsThePlacementItWasCreatedWith(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{}
	rt, _ := placementAddRuntime(t, fr)

	// A local binding first: its actor then starts placing its rounds remote.
	if _, _, err := BindResolved(ctx, rt, BindOptions{Name: "api", CWD: "/repo", Candidate: testClaudeRef, MasterMindID: testMasterMindName}); err != nil {
		t.Fatalf("fresh bind: %v", err)
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rowsWithPlacement("zen"))

	b, res, err := BindResolved(ctx, rt, BindOptions{Name: "api", CWD: "/repo", Resume: true, MasterMindID: testMasterMindName})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if b.Builder.Server != "" || !b.Builder.Headless() {
		t.Fatalf("builder = %+v, want the binding to stay local", b.Builder)
	}
	if !reflect.DeepEqual(res.Placement, resolve.PlacementResolution{}) {
		t.Fatalf("placement = %+v, want the zero resolution: a resume never re-places", res.Placement)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v, want no probe on resume", fr.calls)
	}
}

func TestPlacementTextNamesOnlyTheActorsOwnChoice(t *testing.T) {
	t.Parallel()

	actor := resolve.PlacementResolution{
		Name: "zen", How: resolve.PlacementActor,
		Skipped: []resolve.PlacementSkip{{Name: "backup", Reason: "unreachable"}},
	}
	if got, want := PlacementText(actor), "; placement zen (actor); skipped backup (unreachable)"; got != want {
		t.Fatalf("PlacementText(actor) = %q, want %q", got, want)
	}

	if got := PlacementText(resolve.PlacementResolution{Name: "zen", How: resolve.PlacementExplicit}); got != "" {
		t.Fatalf("PlacementText(explicit) = %q, want empty", got)
	}

	if got := PlacementText(resolve.PlacementResolution{}); got != "" {
		t.Fatalf("PlacementText(zero) = %q, want empty", got)
	}
}

func lastPickNote(t *testing.T, st *store.Store, name string) string {
	t.Helper()
	entries, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	note := ""
	for _, e := range entries {
		if e.Kind == store.KindPick {
			note = e.Note
		}
	}
	if note == "" {
		t.Fatalf("no KindPick entry for %s", name)
	}
	return note
}

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
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// placementPtrStr returns a pointer to s, for a Row's optional shape.
func placementPtrStr(s string) *string { return &s }

// rowsWithPlacement is a builder row serving both test candidates, with the
// placement under test. An empty list is a row that names none: the actor keeps
// today's local path.
func rowsWithPlacement(placement ...string) map[string]roles.Row {
	return map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef, testAgyRef}, Placement: placement},
	}
}

// placementRuntime is a runtime whose builder actor places its rounds where its
// row says: the store, clocks, runner and mastermind registry are newRuntime's,
// and the gates are the case's own ledger entries.
func placementRuntime(t *testing.T, fr *fakeRemote, rows map[string]roles.Row, gates []availability.Entry) Runtime {
	t.Helper()
	rt := newRuntime(t)
	rt.Remote = fr
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	if len(gates) > 0 {
		if err := availability.SaveLedger(rt.Gates, availability.Ledger{Entries: gates}); err != nil {
			t.Fatalf("SaveLedger: %v", err)
		}
	}
	return rt
}

// placementAddRuntime is placementRuntime with a fake git and the registry a
// `bind`-style create needs, for the Add and BindResolved cases.
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

// TestChoosePlacement pins the resolver's contract as a table: the list is
// walked in order, the first viable entry wins, and each entry passed over is
// recorded with one line saying why. Only a changed certificate and an
// unexpected answer fail the whole bind.
func TestChoosePlacement(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		placement []string
		role      string
		rows      map[string]roles.Row
		remote    *fakeRemote
		gates     []availability.Entry
		flags     placementFlags
		want      PlacementResolution
		wantErr   string
	}{
		{
			name:   "an actor that names no placement resolves to nothing",
			rows:   map[string]roles.Row{"builder": {Candidates: []string{testClaudeRef}}},
			remote: &fakeRemote{},
			want:   PlacementResolution{},
		},
		{
			name:      "the first viable server in the list wins",
			placement: []string{"backup", "zen"},
			remote: &fakeRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp:  map[string]remote.ActorView{"backup": {Actor: "builder", Accepted: true}},
			},
			want: PlacementResolution{Name: "backup", How: placementHowActor},
		},
		{
			name:      "a refused actor is skipped and the next entry wins",
			placement: []string{"backup", "zen"},
			remote: &fakeRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp: map[string]remote.ActorView{
					"backup": {Actor: "builder", Accepted: false, Reason: "candidate x refused"},
				},
			},
			want: PlacementResolution{
				Name: "zen", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "backup", Reason: "candidate x refused"}},
			},
		},
		{
			name:      "an unreachable server is skipped and local is the fallback",
			placement: []string{"zen", "local"},
			remote:    &fakeRemote{whoAmIErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)},
			want: PlacementResolution{
				Name: "local", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "unreachable"}},
			},
		},
		{
			name:      "a client that is not enrolled is skipped",
			placement: []string{"zen", "local"},
			remote: &fakeRemote{whoAmIErr: &client.HTTPError{
				Status: 401, Body: remote.ErrorBody{Code: remote.CodeNotEnrolled, Message: "not enrolled"},
			}},
			want: PlacementResolution{
				Name: "local", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "not enrolled"}},
			},
		},
		{
			name:      "a changed certificate fails the bind loudly",
			placement: []string{"zen", "local"},
			remote:    &fakeRemote{whoAmIErr: client.ErrCertChanged},
			wantErr:   "pinned fingerprint mismatch",
		},
		{
			name:      "a server without the placement feature is reachable-only and viable",
			placement: []string{"zen", "local"},
			remote:    &fakeRemote{whoAmIResp: remote.WhoAmI{}},
			want:      PlacementResolution{Name: "zen", How: placementHowActor},
		},
		{
			name:      "a reader is skipped where the server does not run one",
			role:      "reviewer",
			placement: []string{"zen", "local"},
			rows: map[string]roles.Row{
				"reviewer": {Candidates: []string{testClaudeRef}, Placement: []string{"zen", "local"}},
			},
			remote: &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureRoles}}},
			want: PlacementResolution{
				Name: "local", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "reader rounds unsupported; upgrade the server"}},
			},
		},
		{
			name:      "a pinned tier above the server's max is skipped",
			placement: []string{"zen", "local"},
			remote: &fakeRemote{whoAmIResp: remote.WhoAmI{
				Features: []string{remote.FeatureTier, remote.FeaturePlacement},
				MaxTier:  "read",
			}},
			flags: placementFlags{Tier: "edit"},
			want: PlacementResolution{
				Name: "local", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "tier edit is above the server's max tier read"}},
			},
		},
		{
			name:      "a custom actor is skipped where the server cannot run one",
			role:      "coder",
			placement: []string{"zen", "local"},
			rows: map[string]roles.Row{
				"coder": {
					Shape:       placementPtrStr("writer"),
					Candidates:  []string{testClaudeRef},
					Definitions: map[string]roles.DefRow{"claude": {Agent: "plan-executor"}},
					Placement:   []string{"zen", "local"},
				},
			},
			remote: &fakeRemote{whoAmIResp: remote.WhoAmI{}},
			want: PlacementResolution{
				Name: "local", How: placementHowActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "custom actors unsupported; upgrade the server"}},
			},
		},
		{
			name:      "every placement skipped names each one and its reason",
			placement: []string{"zen", "local"},
			remote: &fakeRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp:  map[string]remote.ActorView{"zen": {Actor: "builder", Accepted: false, Reason: "actor not served"}},
			},
			gates: []availability.Entry{{
				Kind: availability.RateLimited, Subject: "test", At: baseTime, Source: "relevo",
			}},
			wantErr: `no viable placement for actor "builder": zen (actor not served); local (every candidate serving "builder" is gated: `,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows := tc.rows
			if rows == nil {
				rows = rowsWithPlacement(tc.placement...)
			}
			role := tc.role
			if role == "" {
				role = "builder"
			}
			rt := placementRuntime(t, tc.remote, rows, tc.gates)

			got, err := choosePlacement(context.Background(), rt, role, "", tc.flags)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("choosePlacement = %+v, want the error %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("choosePlacement: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("choosePlacement = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestChoosePlacementOldServerIsNotAsked pins the degradation: a server that
// does not advertise the placement route is never sent an actor probe, because
// it could not answer one.
func TestChoosePlacementOldServerIsNotAsked(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{}}
	rt := placementRuntime(t, fr, rowsWithPlacement("zen"), nil)

	got, err := choosePlacement(context.Background(), rt, "builder", "", placementFlags{})
	if err != nil {
		t.Fatalf("choosePlacement: %v", err)
	}
	if got.Name != "zen" {
		t.Fatalf("placement = %+v, want zen", got)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Actor:") {
			t.Fatalf("calls = %v, want no actor probe on a server without the feature", fr.calls)
		}
	}
}

// TestChoosePlacementNoListLeavesThePathAlone pins the no-change default: an
// actor that names no placement is not probed at all, so the bind is exactly
// what it was before placement existed.
func TestChoosePlacementNoListLeavesThePathAlone(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{}
	rt := placementRuntime(t, fr, map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef}},
	}, nil)

	got, err := choosePlacement(context.Background(), rt, "builder", "", placementFlags{})
	if err != nil {
		t.Fatalf("choosePlacement: %v", err)
	}
	if !reflect.DeepEqual(got, PlacementResolution{}) {
		t.Fatalf("placement = %+v, want the zero resolution", got)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v, want no probe", fr.calls)
	}
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
	want := PlacementResolution{
		Name: "local", How: placementHowActor,
		Skipped: []PlacementSkip{{Name: "zen", Reason: "unreachable"}},
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
	want := PlacementResolution{Name: "zen", How: placementHowActor}
	if !reflect.DeepEqual(res.Resolution.Placement, want) {
		t.Fatalf("placement = %+v, want %+v", res.Resolution.Placement, want)
	}
	if !slicesContain(fr.calls, "Actor:zen:builder:") {
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
	want := PlacementResolution{Name: "zen", How: placementHowExplicit}
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
	want := PlacementResolution{Name: "local", How: placementHowExplicit}
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
	if !reflect.DeepEqual(res.Placement, PlacementResolution{Name: "zen", How: placementHowActor}) {
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
	if !reflect.DeepEqual(res.Placement, PlacementResolution{}) {
		t.Fatalf("placement = %+v, want the zero resolution", res.Placement)
	}
	if got, want := ExplainResolution("builder", res), "picked claude/test/m for builder: explicit, policy bypassed"; got != want {
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
	if !reflect.DeepEqual(res.Placement, PlacementResolution{}) {
		t.Fatalf("placement = %+v, want the zero resolution: a resume never re-places", res.Placement)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v, want no probe on resume", fr.calls)
	}
}

// TestPickNotesNameThePlacementOnlyWhenItExists pins the note format: a
// resolution that carries no placement renders exactly as it did before
// placement existed, and one that carries a placement appends one clause per
// entry, the chosen one first and then every skip.
func TestPickNotesNameThePlacementOnlyWhenItExists(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	plain := Resolution{
		Candidate: rolesCandidate(t, set, testClaudeRef),
		How:       HowOrder,
		Position:  2,
	}

	if got, want := ExplainResolution("builder", plain), "picked claude/test/m for builder: order #2"; got != want {
		t.Fatalf("ExplainResolution = %q, want %q", got, want)
	}

	placed := plain
	placed.Placement = PlacementResolution{
		Name: "zen", How: placementHowActor,
		Skipped: []PlacementSkip{{Name: "backup", Reason: "unreachable"}},
	}
	want := "picked claude/test/m for builder: order #2; placement zen (actor); skipped backup (unreachable)"
	if got := ExplainResolution("builder", placed); got != want {
		t.Fatalf("ExplainResolution = %q, want %q", got, want)
	}
	wantClause := "; placement zen (actor); skipped backup (unreachable)"
	if got := PickText("builder", placed, set); !strings.HasSuffix(got, wantClause) {
		t.Fatalf("PickText = %q, want it to end with %q", got, wantClause)
	}
}

// TestPlacementTextNamesOnlyTheActorsOwnChoice pins the fresh-create clause: the
// actor's own list produces the same bytes the pick note carries, and a
// placement a flag named or nothing named produces none.
func TestPlacementTextNamesOnlyTheActorsOwnChoice(t *testing.T) {
	t.Parallel()

	actor := PlacementResolution{
		Name: "zen", How: placementHowActor,
		Skipped: []PlacementSkip{{Name: "backup", Reason: "unreachable"}},
	}
	if got, want := PlacementText(actor), "; placement zen (actor); skipped backup (unreachable)"; got != want {
		t.Fatalf("PlacementText(actor) = %q, want %q", got, want)
	}

	if got := PlacementText(PlacementResolution{Name: "zen", How: placementHowExplicit}); got != "" {
		t.Fatalf("PlacementText(explicit) = %q, want empty", got)
	}

	if got := PlacementText(PlacementResolution{}); got != "" {
		t.Fatalf("PlacementText(zero) = %q, want empty", got)
	}
}

// TestChoosePlacementChainMemberFeature pins the chain-member skip: a server
// that cannot carry one chain member's round is passed over with the upgrade
// wording, and the next entry wins.
func TestChoosePlacementChainMemberFeature(t *testing.T) {
	t.Parallel()

	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureLabels}}}
	rt := placementRuntime(t, fr, rowsWithPlacement("zen", "local"), nil)

	got, err := choosePlacement(context.Background(), rt, "builder", "", placementFlags{ChainMember: true, Feature: "auth"})
	if err != nil {
		t.Fatalf("choosePlacement: %v", err)
	}
	want := PlacementResolution{
		Name: "local", How: placementHowActor,
		Skipped: []PlacementSkip{{Name: "zen", Reason: "chain members unsupported; upgrade the server"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("choosePlacement = %+v, want %+v", got, want)
	}
}

// TestChoosePlacementSkipsReaderServers pins the chain reader rule: every server
// entry is a skip with one fixed reason and no probe, a local entry is still
// viable, and a list with no local entry refuses with that reason named.
func TestChoosePlacementSkipsReaderServers(t *testing.T) {
	t.Parallel()

	rows := map[string]roles.Row{
		"reviewer": {Candidates: []string{testClaudeRef}, Placement: []string{"zen", "local"}},
	}

	t.Run("a local entry wins after the server is skipped unprobed", func(t *testing.T) {
		t.Parallel()

		fr := &fakeRemote{}
		rt := placementRuntime(t, fr, rows, nil)

		got, err := choosePlacement(context.Background(), rt, "reviewer", "", placementFlags{ChainReader: true})
		if err != nil {
			t.Fatalf("choosePlacement: %v", err)
		}
		want := PlacementResolution{
			Name: "local", How: placementHowActor,
			Skipped: []PlacementSkip{{Name: "zen", Reason: chainReaderSkip}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("choosePlacement = %+v, want %+v", got, want)
		}
		if len(fr.calls) != 0 {
			t.Fatalf("calls = %v, want no probe for a reader's server entry", fr.calls)
		}
	})

	t.Run("no local entry refuses and names the skip", func(t *testing.T) {
		t.Parallel()

		fr := &fakeRemote{}
		rt := placementRuntime(t, fr, map[string]roles.Row{
			"reviewer": {Candidates: []string{testClaudeRef}, Placement: []string{"zen"}},
		}, nil)

		_, err := choosePlacement(context.Background(), rt, "reviewer", "", placementFlags{ChainReader: true})
		if err == nil {
			t.Fatal("choosePlacement = nil, want the refusal")
		}
		if !strings.Contains(err.Error(), `no viable placement for actor "reviewer"`) ||
			!strings.Contains(err.Error(), "zen ("+chainReaderSkip+")") {
			t.Fatalf("err = %q, want the reader skip named", err)
		}
		if len(fr.calls) != 0 {
			t.Fatalf("calls = %v, want no probe", fr.calls)
		}
	})
}

// lastPickNote returns the newest KindPick note a binding's log holds.
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

// slicesContain reports whether list holds want, for the call-record checks.
func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

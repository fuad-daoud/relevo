package resolve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
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

// fakeResolveRemote is the placement probe's server: the identity and actor
// answers the case fixes, recording every call. A missing actor answer is an
// acceptance, like a server with no opinion on the actor.
type fakeResolveRemote struct {
	calls      []string
	whoAmIResp remote.WhoAmI
	whoAmIErr  error
	actorResp  map[string]remote.ActorView
	actorErr   map[string]error
}

func (f *fakeResolveRemote) WhoAmI(_ context.Context, server string) (remote.WhoAmI, error) {
	f.calls = append(f.calls, "WhoAmI:"+server)
	return f.whoAmIResp, f.whoAmIErr
}

func (f *fakeResolveRemote) Actor(_ context.Context, server, actor, pinned string) (remote.ActorView, error) {
	f.calls = append(f.calls, "Actor:"+server+":"+actor+":"+pinned)
	if err, ok := f.actorErr[server]; ok {
		return remote.ActorView{}, err
	}
	if view, ok := f.actorResp[server]; ok {
		return view, nil
	}
	return remote.ActorView{Actor: actor, Accepted: true}, nil
}

// resolveTestGateKV returns a real temp database for a Deps Gates field, so
// a case can save a ledger and read it back through the same path the engine
// does.
func resolveTestGateKV(t *testing.T) db.KV {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// choosePlacementDeps builds the Deps choosePlacement reads: the set, a
// registry over rows, the fixed clock, the case's ledger and the fake server.
func choosePlacementDeps(t *testing.T, rows map[string]roles.Row, fr *fakeResolveRemote, gates []availability.Entry) Deps {
	t.Helper()
	set := candidateSet(t, testCandidatesJSON)
	reg, err := roles.Build(&roles.File{Rows: rows}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	kv := resolveTestGateKV(t)
	if len(gates) > 0 {
		if err := availability.SaveLedger(kv, availability.Ledger{Entries: gates}); err != nil {
			t.Fatalf("SaveLedger: %v", err)
		}
	}
	d := resolveTestDeps(set, reg)
	d.Gates = kv
	d.Remote = fr
	return d
}

type choosePlacementCase struct {
	name      string
	placement []string
	role      string
	rows      map[string]roles.Row
	remote    *fakeResolveRemote
	gates     []availability.Entry
	flags     PlacementFlags
	want      PlacementResolution
	wantErr   string
}

func runChoosePlacementCases(t *testing.T, cases []choosePlacementCase) {
	t.Helper()

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
			d := choosePlacementDeps(t, rows, tc.remote, tc.gates)

			got, err := choosePlacement(context.Background(), d, role, "", tc.flags)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("choosePlacement = %+v, want the error %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %q, want it to contain %q", err, tc.wantErr)
				}
				if strings.HasPrefix(tc.wantErr, "no viable placement") && !errors.Is(err, ErrNoViablePlacement) {
					t.Fatalf("err = %#v, want it to unwrap to ErrNoViablePlacement", err)
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

// TestChoosePlacementPrefersTheFirstViableEntry pins the walk: the list is
// walked in order, the first viable entry wins, and each entry passed over is
// recorded with one line saying why.
func TestChoosePlacementPrefersTheFirstViableEntry(t *testing.T) {
	t.Parallel()

	runChoosePlacementCases(t, []choosePlacementCase{
		{
			name:   "an actor that names no placement resolves to nothing",
			rows:   map[string]roles.Row{"builder": {Candidates: []string{testClaudeRef}}},
			remote: &fakeResolveRemote{},
			want:   PlacementResolution{},
		},
		{
			name:      "the first viable server in the list wins",
			placement: []string{"backup", "zen"},
			remote: &fakeResolveRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp:  map[string]remote.ActorView{"backup": {Actor: "builder", Accepted: true}},
			},
			want: PlacementResolution{Name: "backup", How: PlacementActor},
		},
		{
			name:      "a refused actor is skipped and the next entry wins",
			placement: []string{"backup", "zen"},
			remote: &fakeResolveRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp: map[string]remote.ActorView{
					"backup": {Actor: "builder", Accepted: false, Reason: "candidate x refused"},
				},
			},
			want: PlacementResolution{
				Name: "zen", How: PlacementActor,
				Skipped: []PlacementSkip{{Name: "backup", Reason: "candidate x refused"}},
			},
		},
		{
			name:      "an unreachable server is skipped and local is the fallback",
			placement: []string{"zen", "local"},
			remote:    &fakeResolveRemote{whoAmIErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)},
			want: PlacementResolution{
				Name: "local", How: PlacementActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "unreachable"}},
			},
		},
		{
			name:      "a client that is not enrolled is skipped",
			placement: []string{"zen", "local"},
			remote: &fakeResolveRemote{whoAmIErr: &client.HTTPError{
				Status: 401, Body: remote.ErrorBody{Code: remote.CodeNotEnrolled, Message: "not enrolled"},
			}},
			want: PlacementResolution{
				Name: "local", How: PlacementActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "not enrolled"}},
			},
		},
	})
}

// TestChoosePlacementSkipsWhatTheServerCannotCarry pins the skips: a failure
// the human must see fails the bind loudly, and a server that cannot carry
// the invocation is passed over with the upgrade wording.
func TestChoosePlacementSkipsWhatTheServerCannotCarry(t *testing.T) {
	t.Parallel()

	runChoosePlacementCases(t, []choosePlacementCase{
		{
			name:      "a changed certificate fails the bind loudly",
			placement: []string{"zen", "local"},
			remote:    &fakeResolveRemote{whoAmIErr: client.ErrCertChanged},
			wantErr:   "pinned fingerprint mismatch",
		},
		{
			name:      "a server without the placement feature is reachable-only and viable",
			placement: []string{"zen", "local"},
			remote:    &fakeResolveRemote{whoAmIResp: remote.WhoAmI{}},
			want:      PlacementResolution{Name: "zen", How: PlacementActor},
		},
		{
			name:      "a reader is skipped where the server does not run one",
			role:      "reviewer",
			placement: []string{"zen", "local"},
			rows: map[string]roles.Row{
				"reviewer": {Candidates: []string{testClaudeRef}, Placement: []string{"zen", "local"}},
			},
			remote: &fakeResolveRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureRoles}}},
			want: PlacementResolution{
				Name: "local", How: PlacementActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "reader rounds unsupported; upgrade the server"}},
			},
		},
		{
			name:      "a pinned tier above the server's max is skipped",
			placement: []string{"zen", "local"},
			remote: &fakeResolveRemote{whoAmIResp: remote.WhoAmI{
				Features: []string{remote.FeatureTier, remote.FeaturePlacement},
				MaxTier:  "read",
			}},
			flags: PlacementFlags{Tier: "edit"},
			want: PlacementResolution{
				Name: "local", How: PlacementActor,
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
			remote: &fakeResolveRemote{whoAmIResp: remote.WhoAmI{}},
			want: PlacementResolution{
				Name: "local", How: PlacementActor,
				Skipped: []PlacementSkip{{Name: "zen", Reason: "custom actors unsupported; upgrade the server"}},
			},
		},
	})
}

// TestChoosePlacementRefusesWhenNothingIsViable pins the refusal: when every
// placement is skipped the bind fails naming each one and its reason, and the
// error unwraps to ErrNoViablePlacement.
func TestChoosePlacementRefusesWhenNothingIsViable(t *testing.T) {
	t.Parallel()

	runChoosePlacementCases(t, []choosePlacementCase{
		{
			name:      "every placement skipped names each one and its reason",
			placement: []string{"zen", "local"},
			remote: &fakeResolveRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
				actorResp:  map[string]remote.ActorView{"zen": {Actor: "builder", Accepted: false, Reason: "actor not served"}},
			},
			gates: []availability.Entry{{
				Kind: availability.RateLimited, Subject: "test", At: baseTime, Source: "relevo",
			}},
			wantErr: `no viable placement for actor "builder": zen (actor not served); local (every candidate serving "builder" is gated: `,
		},
	})
}

// TestProbePlacementRetypesAnUnknownActor pins the probe refusal's class: the
// actor route's 404 is the server's unknown actor, and it must surface as
// ErrUnknownRole -- the policy refusal a local start gives -- not internal.
func TestProbePlacementRetypesAnUnknownActor(t *testing.T) {
	t.Parallel()

	fr := &fakeResolveRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeaturePlacement}},
		actorErr: map[string]error{"zen": &client.HTTPError{
			Status: http.StatusNotFound,
			Body:   remote.ErrorBody{Code: remote.CodeNotFound, Message: `unknown actor "ghost" (known: [builder]): unknown actor`},
		}},
	}
	d := choosePlacementDeps(t, rowsWithPlacement("zen"), fr, nil)

	_, err := choosePlacement(context.Background(), d, "builder", "", PlacementFlags{})
	if err == nil {
		t.Fatal("choosePlacement = nil, want the unknown-actor refusal")
	}
	if !errors.Is(err, ErrUnknownRole) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownRole)", err)
	}
	if !strings.Contains(err.Error(), `unknown actor "ghost"`) {
		t.Errorf("err = %q, want the server's own message kept", err)
	}
}

// TestChoosePlacementOldServerIsNotAsked pins the degradation: a server that
// does not advertise the placement route is never sent an actor probe, because
// it could not answer one.
func TestChoosePlacementOldServerIsNotAsked(t *testing.T) {
	t.Parallel()

	fr := &fakeResolveRemote{whoAmIResp: remote.WhoAmI{}}
	d := choosePlacementDeps(t, rowsWithPlacement("zen"), fr, nil)

	got, err := choosePlacement(context.Background(), d, "builder", "", PlacementFlags{})
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

	fr := &fakeResolveRemote{}
	d := choosePlacementDeps(t, map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef}},
	}, fr, nil)

	got, err := choosePlacement(context.Background(), d, "builder", "", PlacementFlags{})
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

// TestChoosePlacementChainMemberFeature pins the chain-member skip: a server
// that cannot carry one chain member's round is passed over with the upgrade
// wording, and the next entry wins.
func TestChoosePlacementChainMemberFeature(t *testing.T) {
	t.Parallel()

	fr := &fakeResolveRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureLabels}}}
	d := choosePlacementDeps(t, rowsWithPlacement("zen", "local"), fr, nil)

	got, err := choosePlacement(context.Background(), d, "builder", "", PlacementFlags{ChainMember: true, Feature: "auth"})
	if err != nil {
		t.Fatalf("choosePlacement: %v", err)
	}
	want := PlacementResolution{
		Name: "local", How: PlacementActor,
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

		fr := &fakeResolveRemote{}
		d := choosePlacementDeps(t, rows, fr, nil)

		got, err := choosePlacement(context.Background(), d, "reviewer", "", PlacementFlags{ChainReader: true})
		if err != nil {
			t.Fatalf("choosePlacement: %v", err)
		}
		want := PlacementResolution{
			Name: "local", How: PlacementActor,
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

		fr := &fakeResolveRemote{}
		d := choosePlacementDeps(t, map[string]roles.Row{
			"reviewer": {Candidates: []string{testClaudeRef}, Placement: []string{"zen"}},
		}, fr, nil)

		_, err := choosePlacement(context.Background(), d, "reviewer", "", PlacementFlags{ChainReader: true})
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

// TestPickNotesNameThePlacementOnlyWhenItExists pins the note format: a
// resolution that carries no placement renders exactly as it did before
// placement existed, and one that carries a placement appends one clause per
// entry, the chosen one first and then every skip.
func TestPickNotesNameThePlacementOnlyWhenItExists(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	ref, err := candidate.ParseRef(testClaudeRef)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", testClaudeRef, err)
	}
	claude, err := set.Lookup(ref)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", testClaudeRef, err)
	}
	plain := Resolution{
		Candidate: claude,
		How:       HowOrder,
		Position:  2,
	}

	if got, want := ExplainResolution("builder", plain), "picked claude/test/m for builder: order #2"; got != want {
		t.Fatalf("ExplainResolution = %q, want %q", got, want)
	}

	placed := plain
	placed.Placement = PlacementResolution{
		Name: "zen", How: PlacementActor,
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

func TestPlacementErrorUnwrapsToNoViablePlacementWithFullText(t *testing.T) {
	t.Parallel()

	err := placementError("builder", []PlacementSkip{{Name: "zen", Reason: "gone"}, {Name: "local", Reason: "gated"}})
	if !errors.Is(err, ErrNoViablePlacement) {
		t.Fatalf("err = %#v, want ErrNoViablePlacement", err)
	}
	want := `no viable placement for actor "builder": zen (gone); local (gated)`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

package relevo

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// repinRuntime is chainPinRuntime with the reviewer row also listing the
// reviewer-only alt token, so a start pins reviewer explicitly and a resume
// can re-pin it: an explicit pin never hits the order ambiguity a policy
// pick of two listed candidates would.
func repinRuntime(t *testing.T) Runtime {
	t.Helper()

	rt, _ := chainRuntime(t)
	rt.Candidates = candidateSet(t, chainPinCandidatesJSON)
	rows := chainRows()
	rows["builder"] = roles.Row{Candidates: []string{"opencode/test/m", "claude/test/m"}}
	rows["reviewer"] = roles.Row{Candidates: []string{"claude/test/m", "claude/test/alt"}}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	return rt
}

// haltedPinnedChain starts a chain with pinned builder and reviewer and
// halts it the way the swap tests do: a done builder close and a no-verdict
// reviewer close, leaving the planner step unsent for the resume.
func haltedPinnedChain(t *testing.T, rt Runtime) {
	t.Helper()

	startedChain(t, rt, ChainOptions{Candidates: map[string]string{
		"builder":  "opencode/test/m",
		"reviewer": "claude/test/m",
	}})
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainNoVerdictBody())
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) && row.Status != string(chain.StatusStopped) {
		t.Fatalf("chain status = %q, want halted or stopped", row.Status)
	}
}

// repinCandidates snapshots every member's stored candidate and pin flag.
func repinCandidates(t *testing.T, rt Runtime) map[string]store.Binding {
	t.Helper()

	out := map[string]store.Binding{}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		out[name] = chainBinding(t, rt, name)
	}
	return out
}

// TestChainResumeRepinsReviewerCandidate pins the re-pin: a resume with
// --candidate reviewer=<alt> stores the alt token on the reviewer with
// BuilderPinned set and the actor unchanged, while the other members keep
// their stored candidate, and the chain runs again.
func TestChainResumeRepinsReviewerCandidate(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	before := repinCandidates(t, rt)

	res, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"reviewer": "claude/test/alt"},
	})
	if err != nil {
		t.Fatalf("ChainResume: %v", err)
	}
	if res.Chain.Status != string(chain.StatusRunning) {
		t.Errorf("result chain status = %q, want running", res.Chain.Status)
	}

	reviewer := chainBinding(t, rt, "shop-rev")
	if reviewer.BuilderCandidate != "claude/test/alt" {
		t.Errorf("reviewer candidate = %q, want the re-pinned claude/test/alt", reviewer.BuilderCandidate)
	}
	if !reviewer.BuilderPinned {
		t.Error("reviewer BuilderPinned = false, want true for an explicit re-pin")
	}
	if reviewer.Role != before["shop-rev"].Role {
		t.Errorf("reviewer role = %q, want the stored %q: a re-pin keeps the actor", reviewer.Role, before["shop-rev"].Role)
	}
	if reviewer.Builder.Kind == "" {
		t.Error("reviewer Builder.Kind is empty, want the pin's rebuilt endpoint")
	}

	for _, name := range []string{"shop", "shop-plan"} {
		after := chainBinding(t, rt, name)
		if after.BuilderCandidate != before[name].BuilderCandidate || after.BuilderPinned != before[name].BuilderPinned {
			t.Errorf("%s candidate = %q pinned %v, want the stored %q pinned %v",
				name, after.BuilderCandidate, after.BuilderPinned, before[name].BuilderCandidate, before[name].BuilderPinned)
		}
	}
}

// TestChainResumeWithoutCandidateKeepsStoredCandidates pins the default: a
// resume without --candidate keeps every stored candidate and pin flag
// byte-for-byte.
func TestChainResumeWithoutCandidateKeepsStoredCandidates(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	before := repinCandidates(t, rt)

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop"}); err != nil {
		t.Fatalf("ChainResume: %v", err)
	}

	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		after := chainBinding(t, rt, name)
		if after.BuilderCandidate != before[name].BuilderCandidate || after.BuilderPinned != before[name].BuilderPinned {
			t.Errorf("%s candidate = %q pinned %v, want the stored %q pinned %v",
				name, after.BuilderCandidate, after.BuilderPinned, before[name].BuilderCandidate, before[name].BuilderPinned)
		}
	}
}

// TestChainResumeBadPinRefusesBeforeAnyWrite pins the bad pin: a builder pin
// serving only the reviewer refuses with bind's own wording, and the member
// rows and stored candidates are unchanged.
func TestChainResumeBadPinRefusesBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	rowBefore := chainStoredRow(t, rt, "shop")
	rowsBefore, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers before: %v", err)
	}
	before := repinCandidates(t, rt)

	_, err = ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"builder": "claude/test/alt"},
	})
	if err == nil {
		t.Fatal("ChainResume(cross-part pin) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), `does not serve actor "builder"`) {
		t.Errorf("err = %q, want bind's does-not-serve-actor wording", err.Error())
	}

	if rowAfter := chainStoredRow(t, rt, "shop"); rowAfter.Status != rowBefore.Status {
		t.Errorf("chain status = %q, want untouched %q", rowAfter.Status, rowBefore.Status)
	}
	rowsAfter, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	if !reflect.DeepEqual(rowsAfter, rowsBefore) {
		t.Errorf("member rows = %+v, want untouched %+v", rowsAfter, rowsBefore)
	}
	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		after := chainBinding(t, rt, name)
		if after.BuilderCandidate != before[name].BuilderCandidate {
			t.Errorf("%s candidate = %q, want untouched %q", name, after.BuilderCandidate, before[name].BuilderCandidate)
		}
	}
}

// TestChainResumePinWithoutMemberRefuses pins the missing member: a pin
// naming a part the chain has no member for is refused before anything is
// written.
func TestChainResumePinWithoutMemberRefuses(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	rowsBefore, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers before: %v", err)
	}

	_, err = ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"security": "claude/test/m"},
	})
	if err == nil {
		t.Fatal("ChainResume(security pin without a security member) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "has no security member to pin") {
		t.Errorf("err = %q, want it to name the missing security member", err.Error())
	}

	rowsAfter, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	if !reflect.DeepEqual(rowsAfter, rowsBefore) {
		t.Errorf("member rows = %+v, want untouched %+v", rowsAfter, rowsBefore)
	}
}

// TestChainResumePinOnRemoteMemberRefuses pins the placement guard: a pin
// for a member running on a server is refused, and the stored candidate is
// unchanged.
func TestChainResumePinOnRemoteMemberRefuses(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	rev := chainBinding(t, rt, "shop-rev")
	rev.Owner = "alice"
	if err := rt.Store.Save(rev); err != nil {
		t.Fatalf("place the reviewer remotely: %v", err)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"reviewer": "claude/test/alt"},
	})
	if err == nil {
		t.Fatal("ChainResume(pin for a remote member) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "re-pins a local member only") {
		t.Errorf("err = %q, want the local-member-only refusal", err.Error())
	}

	if after := chainBinding(t, rt, "shop-rev"); after.BuilderCandidate != rev.BuilderCandidate {
		t.Errorf("reviewer candidate = %q, want untouched %q", after.BuilderCandidate, rev.BuilderCandidate)
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) && row.Status != string(chain.StatusStopped) {
		t.Errorf("chain status = %q, want it still halted or stopped", row.Status)
	}
}

// TestChainResumePinOnServedChainRefuses pins the served rule: a --resume
// --candidate targeting a chain the server runs is refused naming the change
// that carries served pins, and the mirror stays halted. The pin is locally
// valid, so the refusal can only come from the served guard.
func TestChainResumePinOnServedChainRefuses(t *testing.T) {
	t.Parallel()

	rt := repinRuntime(t)
	haltedPinnedChain(t, rt)
	// Plant the mirror marker: the row runs on a server, so the resume
	// would ship the wire request, which carries no pins.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		row, err := tx.Chain("shop")
		if err != nil {
			return err
		}
		row.Server = "zen"
		return tx.ChainPut(row)
	}); err != nil {
		t.Fatalf("mark the chain served: %v", err)
	}

	_, err := ChainResume(context.Background(), rt, ResumeOptions{
		Name:       "shop",
		Candidates: map[string]string{"reviewer": "claude/test/alt"},
	})
	if err == nil {
		t.Fatal("ChainResume(pin on a served chain) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "served resume pins arrive in Plan 3") {
		t.Errorf("err = %q, want the served-pins refusal", err.Error())
	}

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want it still halted", row.Status)
	}
}

package relevo

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestVerifyConsultScope pins #313: the verify reviewer runs in its own
// relevo-verify-* scope, with the template's CPUQuota and the gate quota left
// behind in the template.
func TestVerifyConsultScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Git = &fakeGit{headCommitID: "head1"}
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Policy.Order = map[string][]string{"reviewer": {testClaudeRef}}
	rt.NewID = func() string { return verifyConsultID }
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100, CPUQuota: "150%", GateCPUQuota: "300%", AllowedCPUs: "0-3"}

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

	var c *store.Consult
	for i := range got.Consults {
		if got.Consults[i].Role == consult.VerifyRole {
			c = &got.Consults[i]
		}
	}
	if c == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, got.Consults)
	}

	// specs[0] is the builder's own process; the verify consult is second.
	if len(fr.specs) != 2 {
		t.Fatalf("Start calls = %d, want the builder's plus one verify consult", len(fr.specs))
	}
	spec := fr.specs[1]
	if spec.Scope == nil {
		t.Fatal("verify spec.Scope = nil, want a scope from the template")
	}
	if !strings.HasPrefix(spec.Scope.Unit, "relevo-verify-local-webshop-1-") {
		t.Errorf("Scope.Unit = %q, want it to start relevo-verify-local-webshop-1-", spec.Scope.Unit)
	}
	if !strings.HasSuffix(spec.Scope.Unit, c.ID) {
		t.Errorf("Scope.Unit = %q, want it to end with the consult id %q", spec.Scope.Unit, c.ID)
	}
	if spec.Scope.CPUQuota != "150%" {
		t.Errorf("Scope.CPUQuota = %q, want the template's 150%%, not the gate quota", spec.Scope.CPUQuota)
	}
	if spec.Scope.GateCPUQuota != "" {
		t.Errorf("Scope.GateCPUQuota = %q, want it zeroed", spec.Scope.GateCPUQuota)
	}
	if spec.Scope.AllowedCPUs != "0-3" {
		t.Errorf("Scope.AllowedCPUs = %q, want the whole pool 0-3: verify starts after the core is released", spec.Scope.AllowedCPUs)
	}
}

// TestVerifyStillRunsTheReviewer pins that `send --verify` survives the
// removal of `relevo ask`: closing a verify round still starts the reviewer
// through consult.StartVerify, and that reviewer's final message is reconciled
// into the round's findings like any consult's. Deleting StartVerify's call
// site in the close path fails this test.
func TestVerifyStillRunsTheReviewer(t *testing.T) {
	t.Parallel()

	rt, fr, _, closed := startVerifyRound(t)

	// The close started the reviewer in its throwaway worktree.
	if len(fr.handles) != 1 {
		t.Fatalf("verify processes = %d, want 1: the close must start the reviewer", len(fr.handles))
	}
	var c *store.Consult
	for i := range closed.Consults {
		if closed.Consults[i].Role == consult.VerifyRole {
			c = &closed.Consults[i]
		}
	}
	if c == nil {
		t.Fatalf("no %q consult on the binding: %+v", consult.VerifyRole, closed.Consults)
	}
	if c.State != store.ConsultRunning {
		t.Fatalf("verify consult state = %q, want running", c.State)
	}

	// Its final message is reconciled into the round's findings.
	leaveVerifyStream(t, rt, fr, "the reviewer's findings")
	tickConsults(t, rt)

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatal(err)
	}
	var findings *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindFindings {
			findings = &entries[i]
		}
	}
	if findings == nil {
		t.Fatalf("no findings entry queued: %+v", entries)
	}
	body, err := rt.Store.ReadFile(findings.Path)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if !strings.Contains(string(body), "the reviewer's findings") {
		t.Errorf("findings = %q, want the reviewer's final message", body)
	}
}

// TestVerifyConsultStderrSharesTheStream is the surviving consult path's
// version of the removed ask tests: a consult's stderr goes into its stream
// file, and no separate -consult.log is written.
func TestVerifyConsultStderrSharesTheStream(t *testing.T) {
	t.Parallel()

	_, fr, _, _ := startVerifyRound(t)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	spec := fr.specs[0]
	if spec.LogPath != spec.StreamPath {
		t.Errorf("LogPath = %q, StreamPath = %q; want the stderr on the stream file", spec.LogPath, spec.StreamPath)
	}
	if !strings.HasSuffix(spec.LogPath, "-consult.jsonl") {
		t.Errorf("LogPath = %q, want it to end in -consult.jsonl", spec.LogPath)
	}
}

// TestVerifyConsultCarriesRunnerMarker pins #662: the reviewer a verify close
// starts is a runner too, so its spec carries exactly the one RELEVO_RUNNER
// entry -- no GIT_* identity and no other variable it could inherit. Deleting
// the Env from the verify spawn fails this test.
func TestVerifyConsultCarriesRunnerMarker(t *testing.T) {
	t.Parallel()

	_, fr, _, _ := startVerifyRound(t)
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1: the verify consult's own spawn", len(fr.specs))
	}
	want := []string{"RELEVO_RUNNER=webshop"}
	if got := fr.specs[0].Env; !reflect.DeepEqual(got, want) {
		t.Errorf("verify spec.Env = %v, want %v", got, want)
	}
}

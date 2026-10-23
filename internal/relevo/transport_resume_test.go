package relevo

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	transportErrLine = `{"type":"error","error":{"type":"provider.transport","message":"read ECONNRESET"}}` + "\n"
	limitErrLine     = `{"type":"error","error":{"type":"provider.rate-limit","message":"Error 429: You have reached your weekly limit."}}` + "\n"
	outageErrLine    = `{"type":"error","error":{"type":"provider.transport","message":"ECONNRESET: upstream returned 503 Service Unavailable, the provider has no capacity"}}` + "\n"
)

// transportExitOf stages an exited opencode builder with session sess whose
// stream ends in raw, scripted dead with code 1.
func transportExitOf(t *testing.T, rt Runtime, b store.Binding, sess, raw string) store.Binding {
	t.Helper()
	b.Builder.StreamSessionID = sess
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return outageExit(t, rt, b, raw, 1)
}

func TestTransportErrorResumesSameSessionOnce(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", transportErrLine)
	switchesBefore := b.RoundSwitches
	keep := b.RoundStartedAt

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 || !containsArg(fr.specs[1].Argv, "--session", "ses_1") {
		t.Fatalf("specs = %d, argv = %v, want a resume of ses_1", len(fr.specs), fr.specs[len(fr.specs)-1].Argv)
	}
	if got.BuilderCandidate != testOpencodeRef || got.RoundSwitches != switchesBefore || len(got.RoundExcluded) != 0 {
		t.Errorf("candidate=%q switches=%d excluded=%v, want same candidate, uncounted, none excluded", got.BuilderCandidate, got.RoundSwitches, got.RoundExcluded)
	}
	if !got.RoundStartedAt.Equal(keep) || got.State != store.StateActive {
		t.Errorf("RoundStartedAt=%s state=%s, want the kept clock and active", got.RoundStartedAt, got.State)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.HasPrefix(sw[0].Note, sameSessionNotePrefix+" ses_1 of "+testOpencodeRef+" after transport error: ") ||
		!strings.HasSuffix(sw[0].Note, ": same candidate, not counted") {
		t.Fatalf("switch entries = %+v, want one same-session note naming ses_1", sw)
	}
}

func TestSecondTransportErrorSwitchesCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", transportErrLine)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	got = transportExitOf(t, rt, got, "ses_1", transportErrLine)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got.RoundSwitches != 1 || got.BuilderCandidate != testClaudeRef {
		t.Errorf("switches=%d candidate=%q, want one counted switch to %q", got.RoundSwitches, got.BuilderCandidate, testClaudeRef)
	}
	if len(got.RoundExcluded) != 1 || got.RoundExcluded[0] != testOpencodeRef {
		t.Errorf("RoundExcluded = %v, want [%s]", got.RoundExcluded, testOpencodeRef)
	}
	if n := sameSessionResumes(mustReadLog(t, rt), 1, "ses_1"); n != 1 {
		t.Errorf("same-session resumes = %d, want 1", n)
	}
}

func TestTransportResumeKeepsLimitPrecedence(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", transportErrLine+limitErrLine)

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, sameSessionNotePrefix) {
			t.Fatalf("switch note %q: a limit must gate, not resume", e.Note)
		}
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 1 {
		t.Errorf("rate_limited entries = %d, want 1", len(rl))
	}
}

func TestTransportResumeKeepsOutagePrecedence(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", outageErrLine)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, sameSessionNotePrefix) {
			t.Fatalf("switch note %q: an outage must gate, not resume", e.Note)
		}
	}
	if got.RoundSwitches != 0 || len(got.RoundExcluded) != 0 {
		t.Errorf("switches=%d excluded=%v, want the timed outage gate", got.RoundSwitches, got.RoundExcluded)
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 1 {
		t.Errorf("rate_limited entries = %d, want 1 (the outage gate)", len(rl))
	}
}

func TestTransportResumeNeedsSessionId(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "", transportErrLine)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.RoundSwitches != 1 || got.BuilderCandidate != testClaudeRef {
		t.Errorf("switches=%d candidate=%q, want the counted switch to %q", got.RoundSwitches, got.BuilderCandidate, testClaudeRef)
	}
}

func TestTransportResumeSkipsOtherHarnesses(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := seedClaudeHeadless(t, fr)
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	rt = at(rt, time.Minute)
	b = transportExitOf(t, rt, b, "S1", "read ECONNRESET\n")

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, e := range switches(t, rt) {
		if strings.HasPrefix(e.Note, sameSessionNotePrefix) {
			t.Fatalf("switch note %q: only opencode resumes after a transport error", e.Note)
		}
	}
}

func TestNewCandidateAfterSwitchGetsItsOwnResume(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	rt = at(rt, 10*time.Minute)
	b = transportExitOf(t, rt, b, "ses_1", transportErrLine)
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	got = transportExitOf(t, rt, got, "ses_2", transportErrLine)
	if _, err := reconcile(t, rt, got); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	log := mustReadLog(t, rt)
	if sameSessionResumes(log, 1, "ses_1") != 1 || sameSessionResumes(log, 1, "ses_2") != 1 {
		t.Errorf("resumes ses_1=%d ses_2=%d, want one each", sameSessionResumes(log, 1, "ses_1"), sameSessionResumes(log, 1, "ses_2"))
	}
}

func mustReadLog(t *testing.T, rt Runtime) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	return entries
}

package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// pendingAtDone stages a binding whose round has produced two payloads its
// MasterMind has never been told about -- a findings entry pointing at a real
// file and a halt -- and returns the findings text that must surface.
func pendingAtDone(t *testing.T, rt Runtime, b store.Binding) (store.Binding, string) {
	t.Helper()
	const fid = "abc"
	path := rt.Store.FindingsPath(b.Name, b.Round, fid)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir findings dir: %v", err)
	}
	const body = "# abc findings\n\nthe security scan found nothing\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindFindings,
		Payload: "Findings from critic consult " + fid + ": relevo show " + b.Name + " --findings " + fid,
		Path:    path,
	}); err != nil {
		t.Fatalf("AppendLog findings: %v", err)
	}
	// The halt a builder that timed out leaves behind, still pending too.
	next := haltOnce(t, rt, b, "builder timed out after 30m0s")
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return next, body
}

// TestDoneDeliversPendingEntries pins step 7: `relevo done` is the last chance
// this binding has to hand its MasterMind something, and today the payloads
// left pending there are stranded -- reconcile's DONE gate only admits what a
// push route already took, and `relevo wait` answers gone without pulling. The
// entries must surface in the result and nothing may remain pending.
//
// Mutation check: delete the claim/confirm block in Done and the payload comes
// back empty with both entries still unconfirmed.
func TestDoneDeliversPendingEntries(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{}
	b, body := pendingAtDone(t, rt, b)

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}

	if res.Payload == "" {
		t.Fatal("Payload = \"\", want the entries that were pending at done")
	}
	if !strings.Contains(res.Payload, "Findings from critic consult abc") {
		t.Errorf("Payload = %q, want it to name the pending findings entry", res.Payload)
	}
	if !strings.Contains(res.Payload, "security scan found nothing") {
		t.Errorf("Payload = %q, want the findings file's own text expanded into it", res.Payload)
	}
	if !strings.Contains(res.Payload, "timed out after 30m0s") {
		t.Errorf("Payload = %q, want the pending halt's reason in it", res.Payload)
	}
	if body == "" {
		t.Fatal("fixture wrote no findings text; the test would prove nothing")
	}

	// Nothing may remain pending: that is the stranding this fixes.
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed {
			t.Errorf("entry kind=%s round=%d is still unconfirmed after Done", e.Kind, e.Round)
		}
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}

	// Done is idempotent about the payloads: a second run has nothing left.
	again, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("second Done: %v", err)
	}
	if again.Payload != "" {
		t.Errorf("second Done payload = %q, want empty: nothing was pending", again.Payload)
	}
}

// TestDoneWithNothingPendingDeliversNothing guards the ordinary case: a done
// with an empty queue must not invent a payload, and must leave the binding
// done exactly as before.
func TestDoneWithNothingPendingDeliversNothing(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.Payload != "" {
		t.Errorf("Payload = %q, want empty when nothing was pending", res.Payload)
	}
}

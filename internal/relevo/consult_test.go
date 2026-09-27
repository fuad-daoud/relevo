package relevo

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// verifyConsultID is the deterministic id the verify tests mint.
const verifyConsultID = "7f2a3c1d"

// claudeStream renders a claude harness stream whose last assistant text is
// text: the shape transcript.FinalText reads (#147).
func claudeStream(t *testing.T, text string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	return append(line, '\n')
}

// startVerifyRound puts webshop one reconcile past a verify round's close:
// the round closed, the reviewer was started in its throwaway worktree, and
// the process is scripted to have exited 0. The caller writes the stream the
// reviewer left behind, then ticks the consult.
// leaveVerifyStream writes what the reviewer's process left on its stream and
// scripts it as exited with code 0.
func leaveVerifyStream(t *testing.T, rt Runtime, fr *fakeRunner, text string) {
	t.Helper()
	streamPath := rt.Store.ConsultStreamPath("webshop", 1, verifyConsultID)
	if err := os.WriteFile(streamPath, claudeStream(t, text), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
}

// tickConsults runs one reconcile pass over the consults only.
func tickConsults(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	var out store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		out, err = consult.Reconcile(context.Background(), consultDeps(rt), tx, b)
		if err != nil {
			return err
		}
		return tx.Save(out)
	})
	if err != nil {
		t.Fatalf("consult.Reconcile: %v", err)
	}
	return out
}

// This is the mutation-test target named in the spec: delete the
// `State != ConsultRunning` guard at the top of consult.Reconcile and this
// fails. Without the guard every tick re-queues findings already delivered.
func TestTerminalConsultsAreNeverRevisited(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, fr)

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relevo-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	tickConsults(t, rt) // -> done, one findings entry

	countFindings := func() int {
		entries, err := rt.Store.ReadLog("webshop")
		if err != nil {
			t.Fatalf("ReadLog: %v", err)
		}
		n := 0
		for _, e := range entries {
			if e.Kind == store.KindFindings {
				n++
			}
		}
		return n
	}
	if countFindings() != 1 {
		t.Fatalf("got %d findings entries after one tick, want 1", countFindings())
	}

	specsBefore := len(fr.specs)
	for i := 0; i < 5; i++ {
		tickConsults(t, rt)
	}

	if got := countFindings(); got != 1 {
		t.Errorf("got %d findings entries after 6 ticks, want 1: a terminal consult must never be re-queued", got)
	}
	if len(fr.specs) != specsBefore {
		t.Errorf("a terminal consult was started %d more times", len(fr.specs)-specsBefore)
	}
}

func seedSpawning(t *testing.T) (Runtime, *fakeClock) {
	t.Helper()
	rt, _ := seedBound(t)
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	clock := &fakeClock{now: baseTime}
	b.Consults = []store.Consult{
		{
			ID:           "7f2a3c1d",
			Role:         "reviewer",
			Round:        1,
			AskPath:      "/repo/.relevo/consults/7f2a3c1d-ask.md",
			FindingsPath: rt.Store.FindingsPath("webshop", 1, "7f2a3c1d"),
			Endpoint:     store.Endpoint{AgentName: "reviewer", Kind: "claude"},
			State:        store.ConsultSpawning,
			SpawnedAt:    baseTime,
		},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return withClock(rt, clock), clock
}

func TestReconcileSkipsAFreshReservation(t *testing.T) {
	t.Parallel()

	rt, _ := seedSpawning(t)

	b := tickConsults(t, rt)
	if b.Consults[0].State != store.ConsultSpawning {
		t.Fatalf("state = %q, want spawning", b.Consults[0].State)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	// seedSpawning's underlying seedBound already wrote the builder bind's
	// pick entry; the reservation itself queues nothing further.
	if len(entries) != 1 || entries[0].Kind != store.KindPick {
		t.Errorf("got %d log entries queued, want the single pick entry from the bind: %+v", len(entries), entries)
	}
}

func TestReconcileExpiresAStaleReservation(t *testing.T) {
	t.Parallel()

	rt, clock := seedSpawning(t)
	clock.Advance(consult.SpawnTimeout + time.Second)

	b := tickConsults(t, rt)
	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "spawn did not complete") {
		t.Errorf("note = %q, want it to mention 'spawn did not complete'", b.Consults[0].Note)
	}

	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var findings []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindFindings {
			findings = append(findings, e)
		}
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings entries, want 1", len(findings))
	}
	if strings.Contains(findings[0].Payload, "pane") {
		t.Errorf("payload %q contains 'pane'", findings[0].Payload)
	}
	want := "wrote no findings: " + b.Consults[0].Note + "."
	if !strings.HasSuffix(findings[0].Payload, want) {
		t.Errorf("payload %q does not end with %q", findings[0].Payload, want)
	}
}

// seedHeadlessConsult puts a running headless consult on the webshop binding
// and returns the runtime and the record relevo made. The record is seeded
// directly -- the consult machinery no longer has a verb to drive it -- with
// fr standing in as the runtime's Runner.
func seedHeadlessConsult(t *testing.T, fr *fakeRunner) (Runtime, store.Consult) {
	t.Helper()
	rt, _ := seedBound(t)
	rt.Runner = fr
	rt.NewID = func() string { return "7f2a3c1d" }

	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	handle, err := fr.Start(context.Background(), spawn.ProcSpec{Dir: b.CWD, LogPath: rt.Store.ConsultStreamPath(b.Name, b.Round, "7f2a3c1d")})
	if err != nil {
		t.Fatalf("start fake consult: %v", err)
	}
	c := store.Consult{
		ID:           "7f2a3c1d",
		Role:         "reviewer",
		Round:        b.Round,
		AskPath:      rt.Store.AskPath(b.Name, b.Round, "7f2a3c1d"),
		FindingsPath: rt.Store.FindingsPath(b.Name, b.Round, "7f2a3c1d"),
		Endpoint: store.Endpoint{
			AgentName: b.Name + "-reviewer-7f2a3c1d",
			Kind:      "claude",
			Mode:      store.ModeHeadless,
			PID:       handle.PID,
			StartedAt: handle.StartedAt.Unix(),
			LogPath:   rt.Store.ConsultStreamPath(b.Name, b.Round, "7f2a3c1d"),
		},
		State:     store.ConsultRunning,
		SpawnedAt: rt.Now().UTC(),
	}
	b.Consults = append(b.Consults, c)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, c
}

// TestHeadlessConsultFinalMessageBecomesFindings: the process's last
// assistant message is the findings. Deleting the WriteFile leaves the
// consult running and this fails.

// TestHeadlessConsultFinalMessageBecomesFindings: the process's last
// assistant message is the findings. Deleting the PutRoundFile leaves the
// consult running and this fails.
func TestHeadlessConsultFinalMessageBecomesFindings(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, fr)

	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"FINDINGS BODY"}]}}` + "\n" +
		"relevo-exit:0\n"
	if err := os.WriteFile(c.Endpoint.LogPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 0)

	b := tickConsults(t, rt)

	if b.Consults[0].State != store.ConsultDone {
		t.Fatalf("state = %q, want done", b.Consults[0].State)
	}
	if _, err := os.Stat(c.FindingsPath); !os.IsNotExist(err) {
		t.Fatalf("expected no findings file on disk, got err: %v", err)
	}
	body, err := rt.Store.ReadFile(c.FindingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if !strings.Contains(string(body), "FINDINGS BODY") {
		t.Errorf("findings = %q, want it to contain the final message", body)
	}
	pending, found, err := rt.Store.PendingForPlanner("webshop")
	if err != nil || !found {
		t.Fatalf("findings were not queued: found=%v err=%v", found, err)
	}
	if pending.Kind != store.KindFindings || pending.Path != c.FindingsPath {
		t.Errorf("entry = %s path=%q, want findings at %q", pending.Kind, pending.Path, c.FindingsPath)
	}
}

// TestHeadlessConsultExitWithoutTextIsSilent: a process that died without a
// final message is reported silent with its exit code and where to look.

// TestHeadlessConsultExitWithoutTextIsSilent: a process that died without a
// final message is reported silent with its exit code and where to look.
func TestHeadlessConsultExitWithoutTextIsSilent(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, fr)

	if err := os.WriteFile(c.Endpoint.LogPath, []byte("relevo-exit:1\n"), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	fr.script(c.Endpoint.PID, false)
	fr.exit(c.Endpoint.PID, 1)

	b := tickConsults(t, rt)

	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	note := b.Consults[0].Note
	if !strings.Contains(note, "code 1") {
		t.Errorf("note = %q, want it to name the exit code", note)
	}
	if !strings.Contains(note, c.Endpoint.LogPath) {
		t.Errorf("note = %q, want it to point at the stream %s", note, c.Endpoint.LogPath)
	}
	if _, err := rt.Store.ReadFile(c.FindingsPath); err == nil {
		t.Error("a silent consult must write no findings file")
	}
}

// TestHeadlessConsultTimesOut: a process still alive past consult.Timeout is
// killed and reported silent.

// TestHeadlessConsultTimesOut: a process still alive past consult.Timeout is
// killed and reported silent.
func TestHeadlessConsultTimesOut(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, c := seedHeadlessConsult(t, fr)
	clock := &fakeClock{now: baseTime}
	rt = withClock(rt, clock)

	clock.Advance(consult.Timeout + time.Second)
	b := tickConsults(t, rt)

	if len(fr.kills) != 1 {
		t.Fatalf("kills = %d, want 1", len(fr.kills))
	}
	if len(fr.killStreams) < 1 || fr.killStreams[0] != c.Endpoint.LogPath {
		t.Errorf("killStreams[0] = %q, want %q", fr.killStreams, c.Endpoint.LogPath)
	}
	if b.Consults[0].State != store.ConsultSilent {
		t.Fatalf("state = %q, want silent", b.Consults[0].State)
	}
	if !strings.Contains(b.Consults[0].Note, "timed out") {
		t.Errorf("note = %q, want it to say timed out", b.Consults[0].Note)
	}
	if c.Endpoint.PID == 0 {
		t.Error("seeded headless consult has no pid")
	}
}

// TestHeadlessConsultNoTrailerIsSilentDespiteText pins #370, spec §4.5: a
// consult whose stream already holds assistant text but that was killed before
// writing its exit trailer is Silent with a note naming the missing trailer,
// and the findings file is not written -- partial text is never delivered as
// findings, whether the daemon's restart killed it or anything else did.
//
// Mutation check: move FinalText back before the trailer check and this fails:
// the partial text becomes a findings file and the record is Done.
func TestHeadlessConsultNoTrailerIsSilentDespiteText(t *testing.T) {
	t.Parallel()

	// run leaves an assistant message on the stream and kills the process
	// without a trailer, then ticks the consults.
	run := func(t *testing.T, rt Runtime, fr *fakeRunner, c store.Consult) store.Binding {
		t.Helper()
		if err := os.WriteFile(c.Endpoint.LogPath, claudeStream(t, "PARTIAL FINDINGS"), 0o644); err != nil {
			t.Fatalf("write stream: %v", err)
		}
		fr.script(c.Endpoint.PID, false) // exited; no exit() set: killed before the trailer
		return tickConsults(t, rt)
	}

	assertSilent := func(t *testing.T, rt Runtime, c store.Consult, b store.Binding, wants ...string) {
		t.Helper()
		if b.Consults[0].State != store.ConsultSilent {
			t.Fatalf("state = %q, want silent", b.Consults[0].State)
		}
		note := b.Consults[0].Note
		if !strings.Contains(note, "exit trailer") {
			t.Errorf("note = %q, want it to name the missing exit trailer", note)
		}
		for _, want := range wants {
			if !strings.Contains(note, want) {
				t.Errorf("note = %q, want it to contain %q", note, want)
			}
		}
		if !strings.Contains(note, c.Endpoint.LogPath) {
			t.Errorf("note = %q, want it to point at the partial output %s", note, c.Endpoint.LogPath)
		}
		if _, err := rt.Store.ReadFile(c.FindingsPath); err == nil {
			t.Error("a consult with no exit trailer must write no findings file")
		}
	}

	t.Run("lost to a daemon restart", func(t *testing.T) {
		fr := newFakeRunner()
		rt, c := seedHeadlessConsult(t, fr)
		rt.StartedAt = baseTime
		rt.Watched = NewWatched()
		b := run(t, rt, fr, c)
		assertSilent(t, rt, c, b, "lost to a daemon restart before it finished", "no exit trailer")
	})

	t.Run("killed by anything else", func(t *testing.T) {
		fr := newFakeRunner()
		rt, c := seedHeadlessConsult(t, fr)
		b := run(t, rt, fr, c)
		assertSilent(t, rt, c, b, "ended without an exit trailer (killed before it finished)")
	})
}

// TestVerifyVerdictParsedOntoFindingsAndBinding pins #144's verdict path: a
// verify consult whose findings end with `verdict: rejected` records the
// verdict and its two reasons on the findings entry and on the binding, names
// them in the payload, removes the throwaway worktree, and shows in `relevo
// status` until the round after next.

// TestVerifyVerdictParsedOntoFindingsAndBinding pins #144's verdict path: a
// verify consult whose findings end with `verdict: rejected` records the
// verdict and its two reasons on the findings entry and on the binding, names
// them in the payload, removes the throwaway worktree, and shows in `relevo
// status` until the round after next.
func TestVerifyVerdictParsedOntoFindingsAndBinding(t *testing.T) {
	rt, fr, fg, _ := startVerifyRound(t)
	leaveVerifyStream(t, rt, fr, "checked it\n\n```relevo\nverdict: rejected\nreasons: [\"a\",\"b\"]\n```\n")

	b := tickConsults(t, rt)

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
	if findings.Verdict != "rejected" {
		t.Errorf("findings verdict = %q, want rejected", findings.Verdict)
	}
	if len(findings.Reasons) != 2 || findings.Reasons[0] != "a" || findings.Reasons[1] != "b" {
		t.Errorf("findings reasons = %v, want [a b]", findings.Reasons)
	}
	if !strings.Contains(findings.Payload, "verdict rejected · 2 reasons") {
		t.Errorf("payload = %q, want it to carry the verdict line", findings.Payload)
	}

	if b.LastVerdict == nil {
		t.Fatal("LastVerdict is nil after a verdict")
	}
	if b.LastVerdict.Round != 1 || b.LastVerdict.Verdict != "rejected" {
		t.Errorf("LastVerdict = %+v, want round 1 rejected", b.LastVerdict)
	}

	wantWT := rt.Store.VerifyWorktreePath("webshop", 1)
	removed := false
	for _, c := range fg.removeWorktreeCalls {
		if c.Path == wantWT && c.Force {
			removed = true
		}
	}
	if !removed {
		t.Errorf("verify worktree %s not removed with force: %+v", wantWT, fg.removeWorktreeCalls)
	}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Verdict; got != "verdict: rejected (2 reasons)" {
		t.Errorf("status verdict = %q, want \"verdict: rejected (2 reasons)\"", got)
	}

	// The next round closes without a verdict of its own, and the stale one
	// stops being shown: it judged round 1, and b.Round-1 is now 2.
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it again"), SendOptions{}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 2), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 2))
	closed, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile round 2: %v", err)
	}
	// The daemon persists what Reconcile returned; status reads the store.
	if err := rt.Store.Save(closed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if closed.Round != 3 {
		t.Fatalf("round = %d, want 3 after the second close", closed.Round)
	}

	rep, err = Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := rep.Bindings[0].Verdict; got != "" {
		t.Errorf("status verdict = %q after the next round, want it dropped", got)
	}
}

// TestVerifyUnstructuredWhenNoBlock pins #144's prose case: findings without
// a readable block are delivered as unstructured rather than guessed at.

// TestVerifyUnstructuredWhenNoBlock pins #144's prose case: findings without
// a readable block are delivered as unstructured rather than guessed at.
func TestVerifyUnstructuredWhenNoBlock(t *testing.T) {
	t.Parallel()

	rt, fr, _, _ := startVerifyRound(t)
	leaveVerifyStream(t, rt, fr, "I read it; it looks fine to me, no block here.\n")

	b := tickConsults(t, rt)

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
	if findings.Verdict != "unstructured" {
		t.Errorf("findings verdict = %q, want unstructured", findings.Verdict)
	}
	if len(findings.Reasons) != 0 {
		t.Errorf("findings reasons = %v, want none", findings.Reasons)
	}
	if findings.Path == "" {
		t.Errorf("unstructured findings must still name the findings file")
	}

	if b.LastVerdict == nil || b.LastVerdict.Verdict != "unstructured" {
		t.Errorf("LastVerdict = %+v, want unstructured", b.LastVerdict)
	}
}

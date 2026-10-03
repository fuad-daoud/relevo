package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestReaderDeliverablePresent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		runnerFile bool
		streamText string
		want       bool
	}{
		{
			name:       "block-carrying final message",
			streamText: "Here is the work.\n\n```relevo\nstatus: done\n```\n",
			want:       true,
		},
		{
			name:       "runner-written output file",
			runnerFile: true,
			want:       true,
		},
		{
			name:       "chain-block message",
			streamText: "# Review\n\n```relevo\nverdict: pass\n```\n",
			want:       true,
		},
		{
			name:       "narration sentence",
			streamText: "I am analyzing the code now.",
			want:       false,
		},
		{
			name:       "DSML text",
			streamText: "<thought>analyzing dependencies</thought>",
			want:       false,
		},
		{
			name:       "empty stream",
			streamText: "",
			want:       false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime(t)
			b := testReaderBinding()
			b.Name = strings.ReplaceAll(tc.name, " ", "-")
			if tc.runnerFile {
				out := reportPathFor(rt, b)
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(out, []byte("# Findings\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.streamText != "" {
				writeReaderStream(t, rt, b.Name, b.Round, tc.streamText)
			}
			got := readerDeliverablePresent(rt, b)
			if got != tc.want {
				t.Errorf("readerDeliverablePresent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReaderWithoutBlockIsContinuedOnceAndClosesWithContinuedOutput(t *testing.T) {
	t.Parallel()

	rt, b := bindReader(t, readerRepo(t))
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, "I am reviewing the repo now.")

	first, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs after 1st exit = %d, want 2 (one continuation resume)", len(fr.specs))
	}
	resume := fr.specs[1].Argv
	if !anyArgContains(resume, "you stopped before your deliverable") {
		t.Errorf("resume argv = %v, want continuation prompt", resume)
	}
	sw := switchNotes(t, rt, b.Name)
	if len(sw) != 1 || !strings.HasPrefix(sw[0], nudgeNotePrefix) {
		t.Fatalf("switch notes = %+v, want 1 starting %q", sw, nudgeNotePrefix)
	}
	out := reportPathFor(rt, b)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output file %s exists after first exit with no block: %v", out, err)
	}

	// Resumed runner exits carrying a relevo block.
	fr.script(first.Builder.PID, false)
	fr.exit(first.Builder.PID, 0)
	first.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(first); err != nil {
		t.Fatal(err)
	}
	writeReaderMessages(t, rt, first.Name, first.Round, "I am reviewing the repo now.", plainReaderBlock)

	second, err := reconcile(t, at(rt, 2*time.Minute), first)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if second.Round != 2 {
		t.Fatalf("round = %d, want 2 (round closed)", second.Round)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read artifact %s: %v", out, err)
	}
	if !strings.Contains(string(data), "The code is sound") {
		t.Errorf("artifact = %q, want continued message", string(data))
	}
	if strings.Contains(string(data), "I am reviewing the repo now.") {
		t.Errorf("artifact = %q, must not contain initial narration", string(data))
	}
}

func TestReaderContinuationWithoutBlockIsContinuedOnceAndClosesWithContinuedOutput(t *testing.T) {
	TestReaderWithoutBlockIsContinuedOnceAndClosesWithContinuedOutput(t)
}

func TestReaderWithoutBlockHaltsAfterTwoContinuations(t *testing.T) {
	t.Parallel()

	rt, b := bindReader(t, readerRepo(t))
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, "Exit 1 narration.")

	first, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs after 1st exit = %d, want 2 (continuation 1)", len(fr.specs))
	}

	// Second exit: narration only.
	fr.script(first.Builder.PID, false)
	fr.exit(first.Builder.PID, 0)
	first.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(first); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, first.Name, first.Round, "Exit 2 narration.")

	second, err := reconcile(t, at(rt, 2*time.Minute), first)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fr.specs) != 3 {
		t.Fatalf("specs after 2nd exit = %d, want 3 (continuation 2)", len(fr.specs))
	}

	// Third exit: narration only. Must halt after 2 continuations.
	fr.script(second.Builder.PID, false)
	fr.exit(second.Builder.PID, 0)
	second.Builder.StreamSessionID = "S3"
	if err := rt.Store.Save(second); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, second.Name, second.Round, "Exit 3 narration.")

	third, err := reconcile(t, at(rt, 3*time.Minute), second)
	if err != nil {
		t.Fatalf("third Reconcile: %v", err)
	}
	if len(fr.specs) != 3 {
		t.Fatalf("specs after 3rd exit = %d, want 3 (no new process spawned)", len(fr.specs))
	}
	if third.State != store.StateNeedsYou {
		t.Errorf("state = %s, want needs_you", third.State)
	}
	if third.Halt == "" {
		t.Fatal("third.Halt is empty, want halted reason")
	}
	label := readerOutputLabel(rt, third)
	if !strings.Contains(third.Halt, label) {
		t.Errorf("Halt = %q, want it to name output label %q", third.Halt, label)
	}
	if !strings.Contains(third.Halt, "not delivered") {
		t.Errorf("Halt = %q, want it to contain 'not delivered'", third.Halt)
	}
	if !strings.Contains(third.Halt, "after 2 continuations") {
		t.Errorf("Halt = %q, want it to name 'after 2 continuations'", third.Halt)
	}
	out := reportPathFor(rt, third)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output file %s exists, want none", out)
	}
	entries, _ := rt.Store.ReadLog(third.Name)
	for _, e := range entries {
		if strings.Contains(e.Note, "unmarked") {
			t.Errorf("log contains unmarked close: %+v", e)
		}
	}
}

func TestReaderContinuationWithoutBlockHaltsAfterTwoContinuations(t *testing.T) {
	TestReaderWithoutBlockHaltsAfterTwoContinuations(t)
}

func TestReaderWithoutBlockAndNoSessionHaltsWithoutResume(t *testing.T) {
	t.Parallel()

	rt, b := bindReader(t, readerRepo(t))
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, "Narration only.")

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1 (no resume)", len(fr.specs))
	}
	if got.State != store.StateNeedsYou || got.Halt == "" {
		t.Fatalf("state = %s, halt = %q; want needs_you and halted", got.State, got.Halt)
	}
	label := readerOutputLabel(rt, got)
	if !strings.Contains(got.Halt, label) || !strings.Contains(got.Halt, "not delivered") {
		t.Errorf("Halt = %q, want label %q and 'not delivered'", got.Halt, label)
	}
	out := reportPathFor(rt, got)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output file %s exists, want none", out)
	}
}

func TestReaderContinuationWithoutBlockAndNoSessionHaltsWithoutResume(t *testing.T) {
	TestReaderWithoutBlockAndNoSessionHaltsWithoutResume(t)
}

func TestReaderNonZeroExitKeepsSwitchPath(t *testing.T) {
	t.Parallel()

	const twoReviewerJSON = `[
	  {"harness":"claude","provider":"test","model":"m1","roles":["reviewer"]},
	  {"harness":"claude","provider":"test","model":"m2","roles":["reviewer"]}
	]`
	const firstRef = "claude/test/m1"
	const secondRef = "claude/test/m2"

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	rt.Candidates = candidateSet(t, twoReviewerJSON)
	rt.Policy = orderOf("reviewer", firstRef, secondRef)
	b.BuilderCandidate = firstRef
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 1)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, "Narration before crash.")

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs = %d, want 2 (switch spawned replacement)", len(fr.specs))
	}
	if anyArgContains(fr.specs[1].Argv, "--resume") {
		t.Errorf("specs[1].Argv = %v, want fresh start not resume", fr.specs[1].Argv)
	}
	if got.BuilderCandidate != secondRef || got.RoundSwitches != 1 {
		t.Errorf("candidate=%q switches=%d, want %q and 1", got.BuilderCandidate, got.RoundSwitches, secondRef)
	}
	for _, note := range switchNotes(t, rt, b.Name) {
		if strings.HasPrefix(note, nudgeNotePrefix) {
			t.Fatalf("exit code 1 was nudged: %s", note)
		}
	}
}

func TestReaderContinuationNonZeroExitKeepsSwitchPath(t *testing.T) {
	TestReaderNonZeroExitKeepsSwitchPath(t)
}

func TestResendResetsReaderContinuationCount(t *testing.T) {
	t.Parallel()

	rt, b := bindReader(t, readerRepo(t))
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	if err := os.WriteFile(b.Builder.LogPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, b.Name, b.Round, "Narration 1.")

	first, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(fr.specs) != 2 {
		t.Fatalf("specs after 1st exit = %d, want 2 (nudge 1)", len(fr.specs))
	}

	// Resend the prompt for round 1: resets the count.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog(first.Name, store.LogEntry{
			TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder,
			Kind: store.KindPrompt, Confirmed: true, Note: "resend",
		})
	}); err != nil {
		t.Fatalf("append resend prompt: %v", err)
	}

	// Next exit without block: count is reset, so it can be nudged again.
	fr.script(first.Builder.PID, false)
	fr.exit(first.Builder.PID, 0)
	first.Builder.StreamSessionID = "S2"
	if err := rt.Store.Save(first); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, first.Name, first.Round, "Narration 2.")

	second, err := reconcile(t, at(rt, 2*time.Minute), first)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(fr.specs) != 3 {
		t.Fatalf("specs after resend exit 1 = %d, want 3 (nudge 2)", len(fr.specs))
	}

	// Another exit without block: second nudge after resend.
	fr.script(second.Builder.PID, false)
	fr.exit(second.Builder.PID, 0)
	second.Builder.StreamSessionID = "S3"
	if err := rt.Store.Save(second); err != nil {
		t.Fatal(err)
	}
	writeReaderStream(t, rt, second.Name, second.Round, "Narration 3.")

	got, err := reconcile(t, at(rt, 3*time.Minute), second)
	if err != nil {
		t.Fatalf("third Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Errorf("state = %s, want active", got.State)
	}
	if len(fr.specs) != 4 {
		t.Fatalf("specs after resend exit 2 = %d, want 4 (nudge 3)", len(fr.specs))
	}
	var nudges int
	for _, note := range switchNotes(t, rt, b.Name) {
		if strings.HasPrefix(note, nudgeNotePrefix) {
			nudges++
		}
	}
	if nudges != 3 {
		t.Errorf("nudges = %d, want 3 (1 before resend, 2 after)", nudges)
	}
}

func TestReaderWithMarkerIsUnaffected(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)
	fr, ok := rt.Runner.(*fakeRunner)
	if !ok {
		t.Fatalf("Runtime.Runner = %T, want *fakeRunner", rt.Runner)
	}
	b.Builder.StreamSessionID = "S1"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath(b.Name, b.Round))
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 0)
	writeReaderStream(t, rt, b.Name, b.Round, "Finished review.\n\n```relevo\nstatus: done\n```\n")

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1 (no continuation when marker present)", len(fr.specs))
	}
	if got.Round != 2 {
		t.Errorf("round = %d, want 2 (closed through marker path)", got.Round)
	}
}

func TestReaderContinuationWithMarkerIsUnaffected(t *testing.T) {
	TestReaderWithMarkerIsUnaffected(t)
}

package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

const wantDiskNote = `environment: disk full: the output reports "no space left on device"`

func TestDiskFullIn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"enospc phrase", "write /tmp/x: no space left on device\n", true},
		{"storage phrase", "copy: no storage space for the file", true},
		{"upper case", "ERROR: No Space Left On Device", true},
		{"near miss", "no space left on disk\nno storage available", false},
		{"empty", "", false},
		{"error form", "write /tmp/x: No space left on device", true},
		{"quoted constant", `phraseNoSpace = "no space left on device"`, false},
		{"quoted want", `want "no space left on device"`, false},
		{"line start without prefix", "ok\nno space left on device\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := diskFullIn(tc.tail); got != tc.want {
				t.Errorf("diskFullIn(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}

	t.Run("phrase outside the bounded tail", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "check.log")
		body := "w: " + phraseNoSpace + "\n" + strings.Repeat("x", diskFullScanBytes+10)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := diskFullLogNote(path); got != "" {
			t.Errorf("note = %q, want none for a phrase before the last %d bytes", got, diskFullScanBytes)
		}
		body = strings.Repeat("x", diskFullScanBytes+10) + "\n" + "w: " + phraseNoSpace + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := diskFullLogNote(path); got != wantDiskNote {
			t.Errorf("note = %q, want %q", got, wantDiskNote)
		}
	})
}

// diskFullGateRun scripts one finished or timed-out check process whose log
// holds body, and returns what advanceGateProc made of it.
func diskFullGateRun(t *testing.T, body string, exitCode int, timedOut bool) *store.GateRecord {
	t.Helper()
	fr := newFakeRunner()
	rt, _ := sentBinding(t)
	rt.Runner = fr
	rt.StartedAt = baseTime
	rt.Watched = NewWatched()
	logPath := filepath.Join(t.TempDir(), "gate.log")
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pid, started := 9001, baseTime.Add(-time.Hour).Unix()
	run := store.GateRun{PID: pid, StartedAt: started, Command: "make check"}
	spec := gateProcSpec{Command: "make check", LogPath: logPath, Timeout: time.Minute}
	if timedOut {
		fr.script(pid, true)
	} else {
		fr.script(pid, false)
		fr.exit(pid, exitCode)
	}
	_, done, rec, err := advanceGateProc(context.Background(), rt, run, spec)
	if err != nil || !done || rec == nil {
		t.Fatalf("advanceGateProc: done=%v rec=%+v err=%v", done, rec, err)
	}
	return rec
}

func TestAdvanceGateProcNamesDiskFullOnFail(t *testing.T) {
	t.Parallel()

	rec := diskFullGateRun(t, "compile\nlink: No space left on device\n", 1, false)
	if rec.Result != "fail" || rec.Note != wantDiskNote {
		t.Errorf("rec = %+v, want result fail and note %q", rec, wantDiskNote)
	}
	if rec := diskFullGateRun(t, "all fine\n", 1, false); rec.Note != "" {
		t.Errorf("note = %q, want none for a failure without the phrase", rec.Note)
	}
	if rec := diskFullGateRun(t, "no space left on device\n", 0, false); rec.Result != "pass" || rec.Note != "" {
		t.Errorf("rec = %+v, want a pass left alone", rec)
	}
}

func TestAdvanceGateProcNamesDiskFullOnTimeout(t *testing.T) {
	t.Parallel()

	rec := diskFullGateRun(t, "tar: no space left on device\n", 0, true)
	if rec.Result != "timeout" || rec.Note != wantDiskNote {
		t.Errorf("rec = %+v, want result timeout and note %q", rec, wantDiskNote)
	}
}

func TestChainHaltsOnDiskFullCheck(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	fr := rt.Runner.(*fakeRunner)
	run, row := startCheckChain(t, rt)
	if err := os.WriteFile(row.Log, []byte("cc: no space left on device\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(row.PID, false)
	fr.exit(row.PID, 2)
	starts := len(fr.specs)

	tickChainChecks(context.Background(), rt)

	c := flowChainRow(t, rt)
	if c.Status != string(workflow.StatusHalted) || !strings.Contains(c.Reason, "check check: "+wantDiskNote) {
		t.Errorf("chain = %s %q, want halted with the environment note", c.Status, c.Reason)
	}
	if len(fr.specs) != starts {
		t.Errorf("starts = %d, want %d: no repair round may start", len(fr.specs), starts)
	}
	if got := loadChainCheck(t, rt, run); got.Note != wantDiskNote {
		t.Errorf("check note = %q", got.Note)
	}
}

// diskFullGateBinding runs a failing round gate with a regate budget of one and
// returns the binding after the gate closed.
func diskFullGateBinding(t *testing.T, logBody string) (store.Binding, *fakeRunner, int) {
	t.Helper()
	fr := newFakeRunner()
	rt, b := seedHeadless(t, fr)
	b.Gate = "make check"
	b.Regate = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.ReportPath("webshop", 1), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, rt.Store.DonePath("webshop", 1))
	got, err := reconcile(t, rt, b)
	if err != nil || got.GateRun == nil {
		t.Fatalf("start gate: %v %+v", err, got.GateRun)
	}
	if err := os.WriteFile(rt.Store.GateLogPath("webshop", 1), []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fr.script(got.GateRun.PID, false)
	fr.exit(got.GateRun.PID, 2)
	before := len(fr.specs)
	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("close gate: %v", err)
	}
	return got, fr, before
}

func TestGateDiskFullDoesNotSpendRegate(t *testing.T) {
	t.Parallel()

	got, fr, before := diskFullGateBinding(t, "FAIL pkg\nwrite: no space left on device\n")
	if got.State != store.StateNeedsYou || !strings.Contains(got.Halt, wantDiskNote) {
		t.Errorf("state=%s halt=%q, want a halt naming the full disk", got.State, got.Halt)
	}
	if got.RepairCount != 0 || got.LastGateSig != "" {
		t.Errorf("repairs=%d sig=%q, want the regate budget untouched", got.RepairCount, got.LastGateSig)
	}
	if len(fr.specs) != before {
		t.Errorf("specs = %d, want %d: no repair process", len(fr.specs), before)
	}

	got, _, _ = diskFullGateBinding(t, "FAIL pkg\n")
	if got.RepairCount != 1 {
		t.Errorf("RepairCount = %d, want 1 for an ordinary red gate", got.RepairCount)
	}
}

// diskFullExit runs a builder that exited with code 3 and left logBody behind.
func diskFullExit(t *testing.T, logBody string, free func(string) (uint64, error)) (store.Binding, Runtime, *fakeRunner) {
	t.Helper()
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.DiskFree = free
	rt.Policy = orderOf("builder", testClaudeRef, testAgyRef)
	fr.script(b.Builder.PID, false)
	fr.exit(b.Builder.PID, 3)
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return got, rt, fr
}

func TestExitWithDiskFullTailHaltsWithoutSwitch(t *testing.T) {
	t.Parallel()

	got, rt, fr := diskFullExit(t, "starting\ncp: No space left on device\nrelevo-exit:3\n", lowDisk)
	if got.State != store.StateNeedsYou || !strings.Contains(got.Halt, "environment: disk full: ") || !strings.Contains(got.Halt, "no space left on device") {
		t.Errorf("state=%s halt=%q, want a halt naming the full disk", got.State, got.Halt)
	}
	if got.RoundSwitches != 0 || len(got.RoundExcluded) != 0 || len(switches(t, rt)) != 0 {
		t.Errorf("switches=%d excluded=%v, want no switch and no exclusion", got.RoundSwitches, got.RoundExcluded)
	}
	if len(fr.specs) != 1 {
		t.Errorf("starts = %d, want only the original builder", len(fr.specs))
	}
}

func TestExitWithoutDiskFullStillSwitches(t *testing.T) {
	t.Parallel()

	got, rt, fr := diskFullExit(t, "starting\nno space left on disk\nrelevo-exit:3\n", plentyDisk)
	if got.State != store.StateActive || got.RoundSwitches != 1 || len(switches(t, rt)) != 1 {
		t.Errorf("state=%s switches=%d, want the usual switch", got.State, got.RoundSwitches)
	}
	if len(fr.specs) != 2 {
		t.Errorf("starts = %d, want a replacement builder", len(fr.specs))
	}
}

func TestExitWithDiskFullTailButFreeDiskStillSwitches(t *testing.T) {
	t.Parallel()

	got, rt, fr := diskFullExit(t, "starting\ncp: No space left on device\nrelevo-exit:3\n", plentyDisk)
	if got.State != store.StateActive || got.RoundSwitches != 1 || len(switches(t, rt)) != 1 {
		t.Errorf("state=%s switches=%d, want the usual switch", got.State, got.RoundSwitches)
	}
	if len(fr.specs) != 2 {
		t.Errorf("starts = %d, want a replacement builder", len(fr.specs))
	}
}

func TestExitWithQuotedPhraseInTailStillSwitches(t *testing.T) {
	t.Parallel()

	got, rt, _ := diskFullExit(t, "starting\nphraseNoSpace = \"no space left on device\"\nwant \"no space left on device\"\nrelevo-exit:3\n", lowDisk)
	if strings.Contains(got.Halt, "also reports") || len(switches(t, rt)) != 1 {
		t.Errorf("halt=%q switches=%d, want the usual switch and no disk-full halt", got.Halt, len(switches(t, rt)))
	}
}

func TestChainRepairsCheckThatOnlyQuotesDiskFullPhrase(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	startFlowChain(t, rt, flowLocalRepairWorkflow)
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainRedCheck(t, rt, "shop", "--- FAIL: TestX\n    want \"no space left on device\"\n")

	if c := flowChainRow(t, rt); c.Status == string(workflow.StatusHalted) {
		t.Errorf("chain halted %q, want the repair loop", c.Reason)
	}
	if builder := chainBinding(t, rt, "shop"); builder.Round != 2 {
		t.Errorf("builder round = %d, want the repair round 2", builder.Round)
	}
}

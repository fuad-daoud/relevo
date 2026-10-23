package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	windowOpenRef                 = testClaudeRef
	windowFallback                = "opencode/other/m"
	windowOpenClock time.Duration = 0
	windowClosedFor               = 9 * time.Hour
)

// windowCandidatesJSON puts claude first in the order behind an only-window
// 14:00Z-00:00Z, and opencode behind it with no window. baseTime is 15:33Z, so
// baseTime is inside the window and baseTime+9h (00:33Z) is outside.
const windowCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"],
   "windows":{"only":[{"from":"14:00Z","to":"00:00Z"}]}},
  {"harness":"opencode","provider":"other","model":"m","roles":["builder"]}
]`

const noWindowCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"other","model":"m","roles":["builder"]}
]`

func windowRuntime(t *testing.T, body string, offset time.Duration) Runtime {
	t.Helper()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, body)
	rt.Policy = orderOf("builder", windowOpenRef, windowFallback)
	return at(rt, offset)
}

func bindWindow(t *testing.T, rt Runtime, candidate string) store.Binding {
	t.Helper()
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: candidate, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return b
}

func loadWebshop(t *testing.T, rt Runtime) store.Binding {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestSendRepicksOffAClosedWindow(t *testing.T) {
	t.Parallel()

	rt := windowRuntime(t, windowCandidatesJSON, windowOpenClock)
	if b := bindWindow(t, rt, ""); b.BuilderCandidate != windowOpenRef {
		t.Fatalf("bound on %q, want %q inside the window", b.BuilderCandidate, windowOpenRef)
	}

	rt = at(rt, windowClosedFor)
	res, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	b := loadWebshop(t, rt)
	if b.BuilderCandidate != windowFallback {
		t.Errorf("BuilderCandidate = %q, want %q after the window closed", b.BuilderCandidate, windowFallback)
	}
	if !strings.Contains(res.Pick, "outside window") || !strings.Contains(res.Pick, "opencode") {
		t.Errorf("pick line = %q, want it to say the claude candidate is outside its window and name the new pick", res.Pick)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := ""
	for _, e := range entries {
		if e.Kind == store.KindPick {
			last = e.Note
		}
	}
	if !strings.Contains(last, windowFallback) {
		t.Errorf("last pick entry = %q, want it to name %q", last, windowFallback)
	}
}

func TestSendRepicksOntoAnOpenedWindow(t *testing.T) {
	t.Parallel()

	rt := windowRuntime(t, windowCandidatesJSON, windowClosedFor)
	if b := bindWindow(t, rt, ""); b.BuilderCandidate != windowFallback {
		t.Fatalf("bound on %q, want %q outside the window", b.BuilderCandidate, windowFallback)
	}

	rt = at(rt, windowOpenClock)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := loadWebshop(t, rt).BuilderCandidate; got != windowOpenRef {
		t.Errorf("BuilderCandidate = %q, want %q once the window opened", got, windowOpenRef)
	}
}

func TestNoWindowConfiguredNeverMovesARateLimitedBinding(t *testing.T) {
	t.Parallel()

	rt := windowRuntime(t, noWindowCandidatesJSON, windowOpenClock)
	bindWindow(t, rt, "")
	if _, err := availability.Unavailable(AvailabilityDeps(rt), windowOpenRef, time.Time{}, "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := loadWebshop(t, rt).BuilderCandidate; got != windowOpenRef {
		t.Errorf("BuilderCandidate = %q, want %q: a rate limit alone never moves a binding between rounds", got, windowOpenRef)
	}
}

func TestPinnedBindingDoesNotRepick(t *testing.T) {
	t.Parallel()

	rt := windowRuntime(t, windowCandidatesJSON, windowOpenClock)
	if b := bindWindow(t, rt, windowOpenRef); !b.BuilderPinned {
		t.Fatal("a bind that names a candidate must pin the binding")
	}

	rt = at(rt, windowClosedFor)
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := loadWebshop(t, rt).BuilderCandidate; got != windowOpenRef {
		t.Errorf("BuilderCandidate = %q, want the pinned %q", got, windowOpenRef)
	}
}

func TestDryRunWindowNoteDoesNotClaimASwitch(t *testing.T) {
	t.Parallel()

	rt := windowRuntime(t, windowCandidatesJSON, windowOpenClock)
	bindWindow(t, rt, windowOpenRef)
	rt = at(rt, windowClosedFor)

	d, err := SendDryRun(context.Background(), rt, "webshop", writePlan(t, "# x"), SendOptions{})
	if err != nil {
		t.Fatalf("SendDryRun: %v", err)
	}
	if !strings.Contains(d.GateNote, "outside window") || !strings.Contains(d.GateNote, "next round re-picks") {
		t.Errorf("GateNote = %q, want the window and that the next round re-picks", d.GateNote)
	}
	if strings.Contains(d.GateNote, "would switch") {
		t.Errorf("GateNote = %q must not claim the daemon would switch", d.GateNote)
	}
}

func TestChainMemberAndRepairRepickForWindows(t *testing.T) {
	t.Parallel()

	t.Run("chain member", func(t *testing.T) {
		t.Parallel()
		rt, _ := chainRuntime(t)
		rt.Candidates = candidateSet(t, strings.ReplaceAll(windowCandidatesJSON, `"roles":["builder"]`, `"roles":["builder","reviewer"]`))
		rows := chainRows()
		rows["reviewer"] = roles.Row{Candidates: []string{windowOpenRef, windowFallback}}
		rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
		startedChain(t, rt, ChainOptions{})

		rt = at(rt, windowClosedFor)
		var sent store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			m, err := tx.Load("shop-rev")
			if err != nil {
				return err
			}
			m.BuilderPinned = false
			sent, err = sendChainRound(context.Background(), rt, tx, m, "next")
			return err
		})
		if err != nil {
			t.Fatalf("sendChainRound: %v", err)
		}
		if sent.BuilderCandidate != windowFallback {
			t.Errorf("member BuilderCandidate = %q, want %q after the window closed", sent.BuilderCandidate, windowFallback)
		}
	})

	t.Run("repair round", func(t *testing.T) {
		t.Parallel()
		rt := windowRuntime(t, windowCandidatesJSON, windowOpenClock)
		bindWindow(t, rt, "")
		if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
			t.Fatalf("Send: %v", err)
		}

		rt = at(rt, windowClosedFor)
		b := loadWebshop(t, rt)
		b.Round, b.Regate = 2, 1
		var next store.Binding
		err := rt.Store.WithLock(func(tx *store.Tx) error {
			var err error
			next, err = startRepairRound(context.Background(), rt, tx, b, store.GateRecord{Result: "fail"}, 1)
			return err
		})
		if err != nil {
			t.Fatalf("startRepairRound: %v", err)
		}
		if next.BuilderCandidate != windowFallback {
			t.Errorf("BuilderCandidate = %q (state %s), want %q for the repair round", next.BuilderCandidate, next.State, windowFallback)
		}
	})
}

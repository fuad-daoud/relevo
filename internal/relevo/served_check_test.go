package relevo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// servedCheckEnv is a served binding with a worktree of its own and a fake
// runner: the shape every check here runs against. It returns the binding as it
// was saved, so a test can read the round its run was keyed to.
//
// The clock follows the runner's own start time, so a run the fake started is
// never already past its timeout; a test that wants a timeout moves the clock
// itself.
func servedCheckEnv(t *testing.T) (Runtime, store.Binding, *fakeRunner) {
	t.Helper()
	rt, b := sentBinding(t)
	fr := newFakeRunner()
	rt.Runner = fr
	rt.Watched = NewWatched()
	rt.Scope = &spawn.ScopeSpec{CPUWeight: 100}
	rt.Now = func() time.Time {
		if n := len(fr.handles); n > 0 {
			return fr.handles[n-1].StartedAt
		}
		return baseTime
	}
	b.Owner = "tenant-a"
	b.Worktree = t.TempDir()
	b.Gate = ""
	b.Serve = &store.ServeFacts{
		RepoID:   strings.Repeat("a", 64),
		BareRepo: t.TempDir(),
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return rt, b, fr
}

// tick advances the binding's stored check run through the daemon sweep and
// returns the run as it stands afterwards. A tick that changed nothing is a
// stable read, so a test may call it again.
func tickCheck(t *testing.T, rt Runtime) store.CheckRun {
	t.Helper()
	tickServedChecks(context.Background(), rt)
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.CheckRun == nil {
		t.Fatal("CheckRun = nil after the tick")
	}
	return *b.CheckRun
}

// startCheck posts a check and fails the test if the server refused to start it.
func startCheck(t *testing.T, rt Runtime, req remote.CreateCheckRequest) remote.CheckView {
	t.Helper()
	view, _, err := ServedCheckStart(context.Background(), rt, "webshop", req)
	if err != nil {
		t.Fatalf("ServedCheckStart %+v: %v", req, err)
	}
	return view
}

// TestServedCheckPassFailTimeout pins the three endings a check run reaches:
// exit 0 settles as pass, a non-zero exit as fail with the code, and a run past
// the policy timeout is killed and settled as timeout. It also pins that a run
// still going settles nothing, so a tick that sees it alive is a stable read.
//
// Mutation check: drop the timeout branch from the advance and the timeout
// subtest runs to the process instead of settling.
func TestServedCheckPassFailTimeout(t *testing.T) {
	t.Parallel()

	endings := []struct {
		name     string
		exit     int
		timedOut bool
		want     string
	}{
		{name: "pass", exit: 0, want: "pass"},
		{name: "fail", exit: 2, want: "fail"},
		{name: "timeout", timedOut: true, want: "timeout"},
	}
	for _, e := range endings {
		t.Run(e.name, func(t *testing.T) {
			t.Parallel()
			rt, b, fr := servedCheckEnv(t)

			view := startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check", Step: "verify"})
			if view.ID != "c1" || view.Step != "verify" {
				t.Fatalf("view = %+v, want the id and step it was posted with", view)
			}
			if view.Result != "" {
				t.Fatalf("Result = %q on a fresh run, want none", view.Result)
			}
			pid := fr.handles[len(fr.handles)-1].PID

			// A tick that sees the run alive settles nothing.
			fr.script(pid, true)
			if got := tickCheck(t, rt); got.Result != "" {
				t.Fatalf("Result = %q on a live run, want none", got.Result)
			}

			// A run past the policy timeout is killed; the clock is moved
			// forward rather than slept for.
			if e.timedOut {
				rt.Now = func() time.Time { return baseTime.Add(2 * policy.DefaultGateTimeout) }
				fr.script(pid, true)
				tickServedChecks(context.Background(), rt)
			} else {
				fr.script(pid, false)
				fr.exit(pid, e.exit)
				tickServedChecks(context.Background(), rt)
			}

			got := tickCheck(t, rt)
			if got.Result != e.want {
				t.Fatalf("Result = %q, want %q", got.Result, e.want)
			}
			if got.Command != "make check" || got.Step != "verify" {
				t.Errorf("run = %+v, want the command and step it was started with", got)
			}
			if got.Round != b.Round {
				t.Errorf("Round = %d, want the round it started in (%d)", got.Round, b.Round)
			}
			if e.timedOut && len(fr.kills) == 0 {
				t.Error("a timed-out run must kill its process")
			}
			if !e.timedOut && got.ExitCode != e.exit {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, e.exit)
			}
			// A settled run is a stable read: a further tick changes nothing.
			fr.script(pid, true)
			if again := tickCheck(t, rt); again.Result != got.Result {
				t.Errorf("Result changed to %q on a settled run", again.Result)
			}
		})
	}
}

// TestServedCheckIdempotentPost pins that a repeated request naming the run the
// binding already holds answers from it, and starts nothing: a client that lost
// the first answer must not double the run.
//
// Mutation check: answer a repeated id with a fresh run and the second Start
// count becomes 2.
func TestServedCheckIdempotentPost(t *testing.T) {
	t.Parallel()

	rt, _, fr := servedCheckEnv(t)
	req := remote.CreateCheckRequest{ID: "c1", Command: "make check"}

	first, created := mustStart(t, rt, req)
	if !created {
		t.Error("the first POST must report that it started the run")
	}
	second, created := mustStart(t, rt, req)
	if created {
		t.Error("a repeated id must not report that it started a run")
	}
	if len(fr.specs) != 1 {
		t.Fatalf("Start calls = %d, want the one the first POST made", len(fr.specs))
	}
	if second.ID != first.ID || second.Command != first.Command {
		t.Errorf("repeated POST = %+v, want the stored run %+v", second, first)
	}

	// The same holds once the run has settled: the record is the answer, not
	// an invitation to start over.
	pid := fr.handles[0].PID
	fr.script(pid, false)
	fr.exit(pid, 0)
	if got := tickCheck(t, rt); got.Result != "pass" {
		t.Fatalf("Result = %q, want pass", got.Result)
	}
	third, created := mustStart(t, rt, req)
	if created {
		t.Error("a repeated id on a settled run must not start a second one")
	}
	if third.Result != "pass" {
		t.Errorf("repeated POST after settling = %+v, want the stored pass", third)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("Start calls = %d, want still the one the first POST made", len(fr.specs))
	}
}

// mustStart is startCheck with the created flag the caller needs.
func mustStart(t *testing.T, rt Runtime, req remote.CreateCheckRequest) (remote.CheckView, bool) {
	t.Helper()
	view, created, err := ServedCheckStart(context.Background(), rt, "webshop", req)
	if err != nil {
		t.Fatalf("ServedCheckStart %+v: %v", req, err)
	}
	return view, created
}

// TestServedCheckSecondRunConflicts pins that a check runs one at a time: a
// different id while a run is in flight is refused, and it is refused without
// starting anything. A settled run is the one case a new id may replace.
//
// Mutation check: drop the Settled clause and a fresh run replaces an
// unfinished one instead of being refused.
func TestServedCheckSecondRunConflicts(t *testing.T) {
	t.Parallel()

	t.Run("a different id while one runs is refused", func(t *testing.T) {
		t.Parallel()
		rt, _, fr := servedCheckEnv(t)
		startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		pid := fr.handles[0].PID
		fr.script(pid, true) // the first run is still going

		_, _, err := ServedCheckStart(context.Background(), rt, "webshop", remote.CreateCheckRequest{ID: "c2", Command: "go test ./..."})
		if !errors.Is(err, ErrCheckRunning) {
			t.Fatalf("err = %v, want ErrCheckRunning", err)
		}
		if len(fr.specs) != 1 {
			t.Fatalf("Start calls = %d, want the refused POST to start nothing", len(fr.specs))
		}
		if got := tickCheck(t, rt); got.ID != "c1" {
			t.Errorf("stored run = %q, want the first run untouched", got.ID)
		}
	})

	t.Run("a new id replaces a settled run", func(t *testing.T) {
		t.Parallel()
		rt, _, fr := servedCheckEnv(t)
		startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		pid := fr.handles[0].PID
		fr.script(pid, false)
		fr.exit(pid, 0)
		if got := tickCheck(t, rt); got.Result != "pass" {
			t.Fatalf("Result = %q, want pass", got.Result)
		}

		view, created := mustStart(t, rt, remote.CreateCheckRequest{ID: "c2", Command: "go test ./..."})
		if !created {
			t.Error("a new id on a settled run must start a run")
		}
		if view.ID != "c2" || view.Result != "" {
			t.Errorf("view = %+v, want the new run with no result", view)
		}
		if len(fr.specs) != 2 {
			t.Fatalf("Start calls = %d, want the second run started", len(fr.specs))
		}
	})
}

// TestServedCheckLogTailCapped pins that a check's log reaches its view whole
// while it fits, and as its last bytes with the truncation said when it does
// not. The cut keeps the end: a failed check says why in its last lines.
//
// Mutation check: drop the cap and a log past it travels whole, with
// LogTruncated false.
func TestServedCheckLogTailCapped(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, path string, n int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a log within the cap travels whole", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := dir + "/check.log"
		write(t, path, servedCheckLogCap-1)

		tail, cut := servedCheckLogTail(os.ReadFile, path)
		if cut {
			t.Error("LogTruncated = true on a log within the cap")
		}
		if len(tail) != servedCheckLogCap-1 {
			t.Fatalf("tail = %d bytes, want the whole log (%d)", len(tail), servedCheckLogCap-1)
		}
	})

	t.Run("a log past the cap travels as its last bytes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := dir + "/check.log"
		write(t, path, servedCheckLogCap*3)

		tail, cut := servedCheckLogTail(os.ReadFile, path)
		if !cut {
			t.Fatal("LogTruncated = false on a log past the cap")
		}
		if len(tail) != servedCheckLogCap {
			t.Fatalf("tail = %d bytes, want the last %d", len(tail), servedCheckLogCap)
		}
	})

	t.Run("the cut travels with the view", func(t *testing.T) {
		t.Parallel()
		rt, b, fr := servedCheckEnv(t)
		startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		logPath := rt.Store.ServedCheckLogPath(b.Name, b.Round)
		write(t, logPath, servedCheckLogCap+1)

		view, err := ServedCheckGet(rt, b.Name, "c1")
		if err != nil {
			t.Fatalf("ServedCheckGet: %v", err)
		}
		if !view.LogTruncated {
			t.Error("LogTruncated = false, want the view to say its log was cut")
		}
		if len(view.LogTail) != servedCheckLogCap {
			t.Errorf("LogTail = %d bytes, want the last %d", len(view.LogTail), servedCheckLogCap)
		}
		if len(fr.specs) != 1 {
			t.Errorf("Start calls = %d, want the read to start nothing", len(fr.specs))
		}
	})
}

// TestServedCheckRerunsOnceAfterDaemonRestart pins the single re-run allowance: a
// check whose process this daemon never saw alive, and whose start predates the
// daemon, is started once more as Attempt 1 and settles on that run. A second
// loss is an error, never a third process.
//
// Mutation check: drop the re-run from the advance and the first tick settles
// as an error with no second Start.
func TestServedCheckRerunsOnceAfterDaemonRestart(t *testing.T) {
	t.Parallel()

	t.Run("a check lost to a restart runs once more", func(t *testing.T) {
		t.Parallel()
		rt, b, fr := servedCheckEnv(t)
		rt.StartedAt = baseTime
		pid, started := 9001, baseTime.Add(-time.Minute).Unix()
		b.CheckRun = &store.CheckRun{
			ID:        "c1",
			Command:   "make check",
			Round:     b.Round,
			PID:       pid,
			StartedAt: started,
			LogPath:   rt.Store.ServedCheckLogPath(b.Name, b.Round),
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		fr.script(pid, false) // exited; no exit() set, so there is no trailer

		got := tickCheck(t, rt)
		if got.Result != "" {
			t.Fatalf("Result = %q, want none while the re-run goes", got.Result)
		}
		if got.Attempt != 1 {
			t.Errorf("Attempt = %d, want 1", got.Attempt)
		}
		if len(fr.specs) != 1 {
			t.Fatalf("Start calls = %d, want the one re-run", len(fr.specs))
		}
		if want := []string{"sh", "-c", "make check 2>&1"}; !equalStrings(fr.specs[0].Argv, want) {
			t.Errorf("re-run Argv = %v, want the first run's %v", fr.specs[0].Argv, want)
		}

		// The re-run settles the run.
		reRun := fr.handles[0].PID
		fr.script(reRun, false)
		fr.exit(reRun, 0)
		if got := tickCheck(t, rt); got.Result != "pass" {
			t.Fatalf("Result = %q after the re-run, want pass", got.Result)
		}
	})

	t.Run("a second loss is an error, not a third process", func(t *testing.T) {
		t.Parallel()
		rt, b, fr := servedCheckEnv(t)
		rt.StartedAt = baseTime
		pid, started := 9001, baseTime.Add(-time.Minute).Unix()
		b.CheckRun = &store.CheckRun{
			ID: "c1", Command: "make check", Round: b.Round,
			PID: pid, StartedAt: started, Attempt: 1,
			LogPath: rt.Store.ServedCheckLogPath(b.Name, b.Round),
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		fr.script(pid, false)

		got := tickCheck(t, rt)
		if got.Result != "error" {
			t.Fatalf("Result = %q, want error", got.Result)
		}
		if len(fr.specs) != 0 {
			t.Errorf("Start calls = %d, want none: Attempt 1 gets no second re-run", len(fr.specs))
		}
	})
}

// TestServedSetGateUpdatesBindingAndRefusesReader pins the gate route's own
// rule: a writer takes the gate and repair budget it was sent, a nil field
// leaves the stored one alone, and a reader is refused because its rounds have
// no gate to set.
func TestServedSetGateUpdatesBindingAndRefusesReader(t *testing.T) {
	t.Parallel()

	t.Run("a writer takes both fields and keeps what it did not name", func(t *testing.T) {
		t.Parallel()
		rt, _, _ := servedCheckEnv(t)
		gate, regate := "make check", 3

		if err := ServedSetGate(context.Background(), rt, "webshop", remote.SetGateRequest{
			Gate: &gate, Regate: &regate,
		}); err != nil {
			t.Fatalf("ServedSetGate: %v", err)
		}
		b, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if b.Gate != gate || b.Regate != regate {
			t.Fatalf("Gate = %q, Regate = %d; want %q, %d", b.Gate, b.Regate, gate, regate)
		}

		// A second call that names only the budget leaves the gate alone.
		other := 5
		if err := ServedSetGate(context.Background(), rt, "webshop", remote.SetGateRequest{Regate: &other}); err != nil {
			t.Fatalf("ServedSetGate: %v", err)
		}
		b, err = rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if b.Gate != gate || b.Regate != other {
			t.Fatalf("Gate = %q, Regate = %d; want %q, %d", b.Gate, b.Regate, gate, other)
		}
	})

	t.Run("a reader is refused", func(t *testing.T) {
		t.Parallel()
		rt, b, _ := servedCheckEnv(t)
		b.Shape = store.ShapeReader
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		gate := "make check"
		err := ServedSetGate(context.Background(), rt, "webshop", remote.SetGateRequest{Gate: &gate})
		if !errors.Is(err, ErrReaderHasNoCheck) {
			t.Fatalf("err = %v, want ErrReaderHasNoCheck", err)
		}
		got, lerr := rt.Store.Load("webshop")
		if lerr != nil {
			t.Fatal(lerr)
		}
		if got.Gate != "" {
			t.Errorf("Gate = %q, want the refusal to leave it empty", got.Gate)
		}
	})
}

// TestServedCheckRefusesUnrunnableBinding pins the two bindings a check can
// never run against: a reader, which has no check, and a binding with no
// worktree, which has no tree to run one in. It also pins the request bounds --
// an unbounded id, an unbounded command, and a request that names neither are
// refused before anything is started.
func TestServedCheckRefusesUnrunnableBinding(t *testing.T) {
	t.Parallel()

	t.Run("a reader has no check", func(t *testing.T) {
		t.Parallel()
		rt, b, fr := servedCheckEnv(t)
		b.Shape = store.ShapeReader
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, _, err := ServedCheckStart(context.Background(), rt, "webshop", remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		if !errors.Is(err, ErrReaderHasNoCheck) {
			t.Fatalf("err = %v, want ErrReaderHasNoCheck", err)
		}
		if len(fr.specs) != 0 {
			t.Errorf("Start calls = %d, want none", len(fr.specs))
		}
	})

	t.Run("a binding with no worktree has nowhere to run one", func(t *testing.T) {
		t.Parallel()
		rt, b, fr := servedCheckEnv(t)
		b.Worktree = ""
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, _, err := ServedCheckStart(context.Background(), rt, "webshop", remote.CreateCheckRequest{ID: "c1", Command: "make check"})
		if !errors.Is(err, ErrNoWorktree) {
			t.Fatalf("err = %v, want ErrNoWorktree", err)
		}
		if len(fr.specs) != 0 {
			t.Errorf("Start calls = %d, want none", len(fr.specs))
		}
	})

	t.Run("a request that cannot be stored is refused", func(t *testing.T) {
		t.Parallel()
		rt, _, fr := servedCheckEnv(t)
		cases := map[string]remote.CreateCheckRequest{
			"no id":           {Command: "make check"},
			"an oversized id": {ID: strings.Repeat("i", maxServedCheckID+1), Command: "make check"},
			"no command":      {ID: "c1"},
			"an oversized command": {
				ID:      "c1",
				Command: strings.Repeat("c", maxServedCheckCommand+1),
			},
		}
		for name, req := range cases {
			_, _, err := ServedCheckStart(context.Background(), rt, "webshop", req)
			if !errors.Is(err, ErrInvalidCheck) {
				t.Errorf("%s: err = %v, want ErrInvalidCheck", name, err)
			}
		}
		if len(fr.specs) != 0 {
			t.Errorf("Start calls = %d, want none", len(fr.specs))
		}
	})
}

// TestServedCheckGetNamesOneRun pins that a read answers from the run the
// binding holds: the id it was started with, and ErrNoCheck for any other, so a
// client polling after the fact still reads its own result and never another
// run's.
func TestServedCheckGetNamesOneRun(t *testing.T) {
	t.Parallel()

	rt, _, _ := servedCheckEnv(t)
	if _, err := ServedCheckGet(rt, "webshop", "c1"); !errors.Is(err, ErrNoCheck) {
		t.Fatalf("err = %v before any run, want ErrNoCheck", err)
	}
	startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})

	view, err := ServedCheckGet(rt, "webshop", "c1")
	if err != nil {
		t.Fatalf("ServedCheckGet: %v", err)
	}
	if view.ID != "c1" || view.Command != "make check" {
		t.Errorf("view = %+v, want the stored run", view)
	}
	if _, err := ServedCheckGet(rt, "webshop", "other"); !errors.Is(err, ErrNoCheck) {
		t.Errorf("err = %v for an id the binding never held, want ErrNoCheck", err)
	}
}

// TestServedCheckRunInTheBindingsWorktree pins where a check runs and how it is
// bounded: the binding's own worktree, in a gate scope of its own, and the
// policy's gate timeout rather than the binding's stored gate timeout. A check
// is the client's own acceptance command, so the binding's round gate timeout
// does not bound it -- which the last assertion shows by making the binding's
// timeout too short to be the one in force.
func TestServedCheckRunInTheBindingsWorktree(t *testing.T) {
	t.Parallel()

	rt, b, fr := servedCheckEnv(t)
	b.GateTimeoutMS = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	startCheck(t, rt, remote.CreateCheckRequest{ID: "c1", Command: "make check"})

	if len(fr.specs) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(fr.specs))
	}
	if got := fr.specs[0].Dir; got != b.Worktree {
		t.Errorf("Dir = %q, want the binding's worktree %q", got, b.Worktree)
	}
	wantUnit := scopeUnitNameFor(scopeGate, b.Owner, b.Name+"-check", b.Round, "")
	if fr.specs[0].Scope == nil || fr.specs[0].Scope.Unit != wantUnit {
		t.Errorf("Scope = %+v, want the check's own gate scope %q", fr.specs[0].Scope, wantUnit)
	}

	// The binding's 1ms gate timeout would have killed this run on its next
	// tick. The policy's does not, so the run is still going.
	pid := fr.handles[0].PID
	rt.Now = func() time.Time { return fr.handles[0].StartedAt.Add(time.Second) }
	fr.script(pid, true)
	tickServedChecks(context.Background(), rt)
	if len(fr.kills) != 0 {
		t.Errorf("kills = %d, want none: a check is bounded by the policy's gate timeout", len(fr.kills))
	}
	if got := tickCheck(t, rt); got.Result != "" {
		t.Errorf("Result = %q a second in, want none", got.Result)
	}
}

// equalStrings is reflect.DeepEqual over two string slices, spelled out so the
// spec assertions above read as the equality they are.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestSharedStoresScopeByOwner pins that owner-scoped stores never see each other's rows.
func TestSharedStoresScopeByOwner(t *testing.T) {
	root := t.TempDir()
	d, err := db.Open(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	storeA := NewShared(filepath.Join(root, "bindings", "a"), "owner-A", d)
	storeB := NewShared(filepath.Join(root, "bindings", "b"), "owner-B", d)

	if err := storeA.Save(newBinding("api", "/repo/a")); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := storeB.Save(newBinding("api", "/repo/b")); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	gotA, err := storeA.Load("api")
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if gotA.CWD != "/repo/a" {
		t.Errorf("A's api CWD = %q, want /repo/a", gotA.CWD)
	}
	gotB, err := storeB.Load("api")
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}
	if gotB.CWD != "/repo/b" {
		t.Errorf("B's api CWD = %q, want /repo/b", gotB.CWD)
	}

	listA, err := storeA.List()
	if err != nil {
		t.Fatalf("List A: %v", err)
	}
	if len(listA) != 1 || listA[0].Name != "api" || listA[0].CWD != "/repo/a" {
		t.Fatalf("A.List = %+v, want only A's api", listA)
	}
	listB, err := storeB.List()
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(listB) != 1 || listB[0].Name != "api" || listB[0].CWD != "/repo/b" {
		t.Fatalf("B.List = %+v, want only B's api", listB)
	}

	// A shared store opens no handle of its own.
	if _, err := os.Stat(filepath.Join(root, "bindings", "a", "relevo.db")); !os.IsNotExist(err) {
		t.Errorf("shared store A created a relevo.db under its root: %v", err)
	}
}

func checkCoreBindingDefaults(t *testing.T, want, got Binding) {
	t.Helper()
	if got.CWD != want.CWD || got.Builder.AgentName != want.Builder.AgentName {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("Save must stamp CreatedAt and UpdatedAt")
	}
	if got.Worktree != "" || got.ForkedFrom != "" || got.ForkedAtRound != 0 {
		t.Errorf("fork fields must default to zero: %+v", got)
	}
	if got.BuilderScreen != "" || !got.BuilderScreenAt.IsZero() {
		t.Errorf("builder screen fields must default to zero: %+v", got)
	}
	if !got.StalledSince.IsZero() {
		t.Errorf("StalledSince must default to zero: %+v", got)
	}
}

func checkStalledSince(t *testing.T, want, got Binding) {
	t.Helper()
	if !got.StalledSince.Equal(want.StalledSince) {
		t.Errorf("StalledSince = %s, want %s", got.StalledSince, want.StalledSince)
	}
}

func checkBuilderScreen(t *testing.T, want, got Binding) {
	t.Helper()
	if got.BuilderScreen != want.BuilderScreen || !got.BuilderScreenAt.Equal(want.BuilderScreenAt) {
		t.Errorf("screen = %q at %v, want %q at %v", got.BuilderScreen, got.BuilderScreenAt, want.BuilderScreen, want.BuilderScreenAt)
	}
}

func checkForkProvenance(t *testing.T, want, got Binding) {
	t.Helper()
	if got.Worktree != want.Worktree || got.ForkedFrom != want.ForkedFrom || got.ForkedAtRound != want.ForkedAtRound {
		t.Errorf("fork fields = %+v, want %+v", got, want)
	}
}

// TestBindingFieldsRoundTrip pins that a binding's fields survive Save and Load
// and that unstamped fields keep their defaults.
func TestBindingFieldsRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Binding)
		check  func(*testing.T, Binding, Binding)
	}{
		{"core fields and stamps", func(*Binding) {}, checkCoreBindingDefaults},
		{
			name:   "StalledSince",
			mutate: func(b *Binding) { b.StalledSince = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) },
			check:  checkStalledSince,
		},
		{
			name: "builder screen",
			mutate: func(b *Binding) {
				b.BuilderScreen = "abc123def456"
				b.BuilderScreenAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			},
			check: checkBuilderScreen,
		},
		{
			name: "fork provenance",
			mutate: func(b *Binding) {
				b.Worktree = "/state/.worktrees/forked"
				b.ForkedFrom = "webshop"
				b.ForkedAtRound = 3
			},
			check: checkForkProvenance,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			want := newBinding("webshop", "/home/dev/projects/webshop")
			tc.mutate(&want)
			if err := s.Save(want); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := s.Load("webshop")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.check(t, want, got)
		})
	}
}

func TestSaveRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	err := s.Save(newBinding("webshop2", "/repo"))
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken", err)
	}
}

func TestSaveAllowsRewritingSameBinding(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("rewriting the same name must not trip ErrCWDTaken: %v", err)
	}
}

func TestLoadMissingIsErrNotFound(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestPathShapes pins every path helper's shape: zero-padded round files under
// the binding directory, and consult files carrying the round and id.
func TestPathShapes(t *testing.T) {
	s := New("/state")
	for _, tc := range []struct {
		name, got, want string
	}{
		{"PlanPath", s.PlanPath("webshop", 3), "/state/webshop/003-plan.md"},
		{"ReportPath", s.ReportPath("webshop", 12), "/state/webshop/012-report.md"},
		{"DonePath", s.DonePath("webshop", 7), "/state/webshop/007-done"},
		{"DiffPath", s.DiffPath("ai", 2), "/state/ai/002-diff.patch"},
		{"QuestionPath", s.QuestionPath("ai", 2), "/state/ai/002-question.md"},
		{"DriftPath", s.DriftPath("webshop", 5), "/state/webshop/005-drift.patch"},
		{"BuilderLogPath", s.BuilderLogPath("webshop", 3), "/state/webshop/003-builder.log"},
		{"RunnerStreamPath", s.RunnerStreamPath("webshop", 3), "/state/webshop/003-runner.jsonl"},
		{"BuilderStreamPath", s.BuilderStreamPath("webshop", 3), "/state/webshop/003-builder.jsonl"},
		{"AskPath", s.AskPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-ask.md"},
		{"FindingsPath", s.FindingsPath("webshop", 12, "7f2a3c1d"), "/state/webshop/012-7f2a3c1d-findings.md"},
		{"ConsultStreamPath", s.ConsultStreamPath("webshop", 3, "7f2a3c1d"), "/state/webshop/003-7f2a3c1d-consult.jsonl"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// Fork copies round files by their leading NNN-, so every round artifact
	// must parse as one.
	for _, tc := range []struct {
		name string
		path string
	}{
		{"builder log", s.BuilderLogPath("webshop", 12)},
		{"runner stream", s.RunnerStreamPath("webshop", 12)},
		{"builder stream", s.BuilderStreamPath("webshop", 12)},
		{"consult stream", s.ConsultStreamPath("webshop", 12, "7f2a3c1d")},
	} {
		if r, ok := roundOfFile(filepath.Base(tc.path)); !ok || r != 12 {
			t.Errorf("roundOfFile(%s) = %d, %v; want 12, true", tc.name, r, ok)
		}
	}
	if s.AskPath("webshop", 3, "7f2a3c1d") == s.QuestionPath("webshop", 3) {
		t.Error("AskPath collides with QuestionPath")
	}

	worktree := New("/tmp/relevo-state-test")
	if got, want := worktree.WorktreeDir(), "/tmp/relevo-state-test/.worktrees"; got != want {
		t.Errorf("WorktreeDir = %q, want %q", got, want)
	}
	if got, want := worktree.WorktreePath("myfork"), "/tmp/relevo-state-test/.worktrees/myfork"; got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}

// TestViewedRoundTrip pins the viewed stamp: no stamp reads ok false,
// MarkViewed creates it, and ViewedAt then reads it back within a second.
func TestViewedRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, ok := s.ViewedAt("webshop"); ok {
		t.Fatal("ViewedAt before any stamp: got ok true, want false")
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkViewed("webshop", at); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}

	got, ok := s.ViewedAt("webshop")
	if !ok {
		t.Fatal("ViewedAt after MarkViewed: got ok false, want true")
	}
	if diff := got.Sub(at); diff < -time.Second || diff > time.Second {
		t.Errorf("ViewedAt = %s, want within a second of %s", got, at)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"webshop", "a", "money-ai", "x_1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "1abc", "Upjo", "has space", "way-too-long-a-binding-name-for-a-pane"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", bad)
		}
	}
}

func TestConcurrentSaveRaceRefusesDuplicateCWD(t *testing.T) {
	s := New(t.TempDir())
	cwd := "/repo"

	var successCount int32
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := s.Save(newBinding("b1", cwd)); err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}()
	go func() {
		defer wg.Done()
		if err := s.Save(newBinding("b2", cwd)); err == nil {
			atomic.AddInt32(&successCount, 1)
		}
	}()

	wg.Wait()

	if atomic.LoadInt32(&successCount) != 1 {
		t.Errorf("expected exactly 1 Save to succeed, got %d", atomic.LoadInt32(&successCount))
	}
}

func TestWithLockSerializesLoadModifySave(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("counter", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// N goroutines each doing load-modify-save of the same binding.
	n := 10
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			err := s.WithLock(func(tx *Tx) error {
				b, err := tx.Load("counter")
				if err != nil {
					return err
				}
				b.Round++
				return tx.Save(b)
			})
			if err != nil {
				t.Errorf("WithLock: %v", err)
			}
		}()
	}

	wg.Wait()

	final, err := s.Load("counter")
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if final.Round != n+1 { // started at 1, incremented n times
		t.Errorf("Round = %d, want %d (lost updates detected)", final.Round, n+1)
	}
}

// TestReadsDoNotWaitForTheStateLock pins that a read does not wait on the
// state lock another goroutine holds.
func TestReadsDoNotWaitForTheStateLock(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("frozen", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entry := LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindPlan, Payload: "hello"}
	if err := s.AppendLog("frozen", entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	release := holdStateLock(t, s)
	defer release()

	// Each read is bounded by a goroutine and a timeout rather than the lock's
	// own 90s bound, so a regression fails fast instead of hanging the suite.
	check := func(name string, fn func() error) {
		done := make(chan error, 1)
		go func() { done <- fn() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s waited for the state lock", name)
		}
	}

	check("Load", func() error {
		got, err := s.Load("frozen")
		if err != nil {
			return err
		}
		if got.Name != b.Name || got.CWD != b.CWD {
			return fmt.Errorf("Load = %+v, want %q at %q", got, b.Name, b.CWD)
		}
		return nil
	})
	check("List", func() error {
		got, err := s.List()
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Name != b.Name {
			return fmt.Errorf("List = %+v, want one binding %q", got, b.Name)
		}
		return nil
	})
	check("ReadLog", func() error {
		got, err := s.ReadLog("frozen")
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Payload != entry.Payload {
			return fmt.Errorf("ReadLog = %+v, want one entry %q", got, entry.Payload)
		}
		return nil
	})
	check("FindByCWD", func() error {
		got, ok, err := s.FindByCWD(b.CWD)
		if err != nil {
			return err
		}
		if !ok || got.Name != b.Name {
			return fmt.Errorf("FindByCWD = (%+v, %v), want %q", got, ok, b.Name)
		}
		return nil
	})
}

// TestReadImportsLegacyFileUnderTheLock pins that a legacy bind.json still
// takes the state lock and is imported.
func TestReadImportsLegacyFileUnderTheLock(t *testing.T) {
	s := New(t.TempDir())
	dir := s.Dir("old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"round":1,"state":"active","round_cap":20,"round_timeout_ms":1800000}`
	legacy := filepath.Join(dir, "bind.json")
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}

	release := holdStateLock(t, s)

	loaded := make(chan error, 1)
	go func() {
		got, err := s.Load("old")
		if err != nil {
			loaded <- err
			return
		}
		if got.Name != "old" || got.CWD != "/repo" {
			loaded <- fmt.Errorf("Load = %+v, want the legacy record", got)
			return
		}
		loaded <- nil
	}()

	select {
	case err := <-loaded:
		t.Fatalf("Load returned while the state lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	release()
	if err := <-loaded; err != nil {
		t.Fatalf("Load after release: %v", err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy bind.json survived the import: %v", err)
	}
	if raw := bindingRecordJSON(t, s, "old"); len(raw) == 0 {
		t.Error("no record was written for the imported binding")
	}
}

func TestNestedAccessDoesNotDeadlock(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("nested", "/repo")
	if err := s.Save(b); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.WithLock(func(tx *Tx) error {
			loaded, err := tx.Load("nested")
			if err != nil {
				return err
			}
			loaded.Round++
			return tx.Save(loaded)
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested access failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested access deadlocked (timeout after 5s)")
	}

	final, err := s.Load("nested")
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if final.Round != 2 {
		t.Errorf("Round = %d, want 2", final.Round)
	}
}

// TestFindByCWDSkipsDoneBindings pins that resolving `relevo send` onto a done
// binding would point it at a finished session.
func TestFindByCWDSkipsDoneBindings(t *testing.T) {
	s := New(t.TempDir())
	done := newBinding("webshop", "/repo")
	done.State = StateDone
	if err := s.Save(done); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, found, err := s.FindByCWD("/repo"); err != nil || found {
		t.Fatalf("found=%v err=%v, want a done binding to be invisible here", found, err)
	}

	if err := s.Save(newBinding("webshop2", "/repo")); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, found, err := s.FindByCWD("/repo")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want the active binding", found, err)
	}
	if got.Name != "webshop2" {
		t.Errorf("name = %q, want webshop2", got.Name)
	}
}

// TestSaveStillRefusesASecondActiveBindingBesideADoneOne pins that skipping
// done bindings must not relax the two-builders-in-one-tree refusal.
func TestSaveStillRefusesASecondActiveBindingBesideADoneOne(t *testing.T) {
	s := New(t.TempDir())
	done := newBinding("webshop", "/repo")
	done.State = StateDone
	if err := s.Save(done); err != nil {
		t.Fatalf("Save done: %v", err)
	}
	if err := s.Save(newBinding("webshop2", "/repo")); err != nil {
		t.Fatalf("Save active beside done: %v", err)
	}

	if err := s.Save(newBinding("webshop3", "/repo")); !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken from the still-active binding", err)
	}
}

// TestAssertCWDFreeIgnoresRemote pins the CWD-uniqueness exemption: a remote
// binding's CWD is never a working tree a builder writes in, while two local
// builders in one tree are still refused.
func TestAssertCWDFreeIgnoresRemote(t *testing.T) {
	s := New(t.TempDir())

	if err := s.Save(newBinding("a", "/repo")); err != nil {
		t.Fatalf("Save local a: %v", err)
	}
	for _, name := range []string{"b", "c"} {
		remote := newBinding(name, "/repo")
		remote.Builder.Mode = ModeRemote
		if err := s.Save(remote); err != nil {
			t.Fatalf("Save remote %s: %v", name, err)
		}
	}

	err := s.Save(newBinding("d", "/repo"))
	if !errors.Is(err, ErrCWDTaken) {
		t.Fatalf("got %v, want ErrCWDTaken from local d", err)
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("error %q must name a, not b", err)
	}
	if strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error %q must not name remote binding b", err)
	}
}

// TestFindByCWDSkipsRemote pins that the cwd-addressed verbs never resolve
// onto a remote binding.
func TestFindByCWDSkipsRemote(t *testing.T) {
	s := New(t.TempDir())

	remoteOnly := newBinding("remoteonly", "/repo")
	remoteOnly.Builder.Mode = ModeRemote
	if err := s.Save(remoteOnly); err != nil {
		t.Fatalf("Save remote: %v", err)
	}

	if _, found, err := s.FindByCWD("/repo"); err != nil || found {
		t.Fatalf("found=%v err=%v, want a remote-only CWD to be invisible here", found, err)
	}

	if err := s.Save(newBinding("local", "/repo2")); err != nil {
		t.Fatalf("Save local: %v", err)
	}
	remoteAlso := newBinding("remotealso", "/repo2")
	remoteAlso.Builder.Mode = ModeRemote
	if err := s.Save(remoteAlso); err != nil {
		t.Fatalf("Save remote beside local: %v", err)
	}

	got, found, err := s.FindByCWD("/repo2")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want the local binding", found, err)
	}
	if got.Name != "local" {
		t.Errorf("name = %q, want local", got.Name)
	}
}

// TestFindByCWDPrefersTheWriter pins that a working tree shared by a writer
// and readers resolves to the writer, whatever order the bindings were saved
// in. The reader names sort first, so a first-match implementation would
// return one of them.
func TestFindByCWDPrefersTheWriter(t *testing.T) {
	s := New(t.TempDir())

	reader := newBinding("a-reader", "/repo")
	reader.Shape = ShapeReader
	if err := s.Save(reader); err != nil {
		t.Fatalf("Save reader: %v", err)
	}
	writer := newBinding("z-writer", "/repo")
	writer.Shape = ShapeWriter
	if err := s.Save(writer); err != nil {
		t.Fatalf("Save writer: %v", err)
	}
	other := newBinding("b-reader", "/repo")
	other.Shape = ShapeReader
	if err := s.Save(other); err != nil {
		t.Fatalf("Save second reader: %v", err)
	}

	got, found, err := s.FindByCWD("/repo")
	if err != nil {
		t.Fatalf("FindByCWD: %v", err)
	}
	if !found || got.Name != "z-writer" {
		t.Fatalf("FindByCWD = (%q, %v), want the writer z-writer", got.Name, found)
	}
}

// TestFindByCWDAmbiguousReaders pins that with no writer one reader resolves;
// several readers are an error naming the flag that fixes it.
func TestFindByCWDAmbiguousReaders(t *testing.T) {
	s := New(t.TempDir())

	only := newBinding("only-reader", "/repo")
	only.Shape = ShapeReader
	if err := s.Save(only); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := s.FindByCWD("/repo")
	if err != nil || !found || got.Name != "only-reader" {
		t.Fatalf("FindByCWD = (%q, %v, %v), want the only reader", got.Name, found, err)
	}

	second := newBinding("second-reader", "/repo")
	second.Shape = ShapeReader
	if err := s.Save(second); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	_, found, err = s.FindByCWD("/repo")
	if !errors.Is(err, ErrAmbiguousCWD) {
		t.Fatalf("FindByCWD with two readers = (found %v, err %v), want ErrAmbiguousCWD", found, err)
	}
	if !strings.Contains(err.Error(), "several readers are bound to /repo: pass --name") {
		t.Errorf("err = %q, want it to name the readers and --name", err.Error())
	}
}

func TestListSkipsTheArchiveDirectory(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List must not see archived bindings, got %+v", got)
	}
}

func TestArchiveRefusesAnUnknownBinding(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Archive("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestLoadIgnoresRemovedLegacyKeys pins that keys from removed features still
// load and are dropped on the next write.
func TestLoadIgnoresRemovedLegacyKeys(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(*testing.T, *Store)
	}{
		{
			name: "builder_alias becomes adopted",
			body: `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"builder_alias":"abuilder","round":1,"state":"active","round_cap":20,"round_timeout_ms":1800000}`,
			check: func(t *testing.T, s *Store) {
				got, err := s.Load("old")
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if got.Name != "old" {
					t.Errorf("Name = %q, want old", got.Name)
				}
				if got.BuilderCandidate != "" {
					t.Errorf("BuilderCandidate = %q, want empty", got.BuilderCandidate)
				}
			},
		},
		{
			name: "preamble_pending is dropped",
			body: `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"preamble_pending":true,"round":3,"state":"active","round_cap":20,"round_timeout_ms":1800000}`,
			check: func(t *testing.T, s *Store) {
				got, err := s.Load("old")
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if got.Round != 3 {
					t.Errorf("Round = %d, want 3", got.Round)
				}
				if err := s.Save(got); err != nil {
					t.Fatalf("Save: %v", err)
				}
				if raw := bindingRecordJSON(t, s, "old"); strings.Contains(string(raw), "preamble_pending") {
					t.Errorf("round-trip must drop preamble_pending, got:\n%s", raw)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(t.TempDir())
			dir := s.Dir("old")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write bind.json: %v", err)
			}
			tc.check(t, s)
		})
	}
}

// TestLoadKeepsALegacyEdgesRecord pins that a bind.json carrying an "edges"
// key still loads: Binding.Edges is a read-only shim now.
func TestLoadKeepsALegacyEdgesRecord(t *testing.T) {
	s := New(t.TempDir())
	dir := s.Dir("old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"name":"old","cwd":"/repo","planner":{"pane_id":"w2:p3","kind":"claude"},"builder":{"pane_id":"w2:p4","kind":"agy"},"edges":[{"id":"a1b2c3","round":1,"when":"report","then":"send","target":"client","prompt":"/plans/client.md","mode":"queue","added_at":"2026-09-01T10:00:00Z","fired":true,"result":"queued"}],"round":1,"state":"active","round_cap":20,"round_timeout_ms":1800000}`
	if err := os.WriteFile(filepath.Join(dir, "bind.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}

	got, err := s.Load("old")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("got %d edges, want the record's one", len(got.Edges))
	}
	if got.Edges[0].ID != "a1b2c3" || got.Edges[0].Target != "client" || !got.Edges[0].Fired {
		t.Errorf("edge did not decode: %+v", got.Edges[0])
	}
}

// TestLegacyPaneBindingReSavesByteIdentical pins the pre-pane-removal record shape.
func TestLegacyPaneBindingReSavesByteIdentical(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := bindingRecordJSON(t, s, "webshop")
	for _, key := range []string{`"mode"`, `"pid"`, `"started_at"`, `"log_path"`} {
		if strings.Contains(string(before), key) {
			t.Errorf("a pane binding's record must not carry %s:\n%s", key, before)
		}
	}
	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.Headless() || got.Builder.Mode != "" {
		t.Errorf("legacy builder must read as pane with empty Mode: %+v", got.Builder)
	}
	if got.Builder.PID != 0 || got.Builder.StartedAt != 0 || got.Builder.LogPath != "" {
		t.Errorf("legacy builder must have zero process fields: %+v", got.Builder)
	}
}

// TestPruneWorktreeDirs pins that the worktree parents go only once they are empty.
func TestPruneWorktreeDirs(t *testing.T) {
	mkdir := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", path, err)
		}
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	t.Run("both empty are removed", func(t *testing.T) {
		s := New(t.TempDir())
		verify := filepath.Join(s.WorktreeDir(), ".verify")
		mkdir(t, verify)

		s.PruneWorktreeDirs()

		if exists(verify) {
			t.Errorf("%s still exists, want it removed", verify)
		}
		if exists(s.WorktreeDir()) {
			t.Errorf("%s still exists, want it removed", s.WorktreeDir())
		}
	})

	t.Run("a sibling worktree keeps .worktrees but not .verify", func(t *testing.T) {
		s := New(t.TempDir())
		verify := filepath.Join(s.WorktreeDir(), ".verify")
		mkdir(t, verify)
		other := filepath.Join(s.WorktreeDir(), "other")
		mkdir(t, other)

		s.PruneWorktreeDirs()

		if exists(verify) {
			t.Errorf("%s still exists, want the empty .verify removed", verify)
		}
		if !exists(s.WorktreeDir()) {
			t.Errorf("%s was removed though it still held a worktree", s.WorktreeDir())
		}
		if !exists(other) {
			t.Errorf("sibling worktree %s was touched", other)
		}
	})

	t.Run("a worktree under .verify keeps everything", func(t *testing.T) {
		s := New(t.TempDir())
		verify := filepath.Join(s.WorktreeDir(), ".verify")
		mkdir(t, filepath.Join(verify, "x-001"))

		s.PruneWorktreeDirs()

		if !exists(verify) {
			t.Errorf("%s was removed though it still held a worktree", verify)
		}
		if !exists(s.WorktreeDir()) {
			t.Errorf("%s was removed though %s still held a worktree", s.WorktreeDir(), verify)
		}
	})

	t.Run("neither exists is a no-op", func(t *testing.T) {
		s := New(t.TempDir())

		s.PruneWorktreeDirs() // must not panic

		if exists(s.WorktreeDir()) {
			t.Errorf("%s exists after the prune of nothing", s.WorktreeDir())
		}
	})
}

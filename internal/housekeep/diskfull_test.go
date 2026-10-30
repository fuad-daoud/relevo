package housekeep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
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
			if got := DiskFullIn(tc.tail); got != tc.want {
				t.Errorf("DiskFullIn(%q) = %v, want %v", tc.tail, got, tc.want)
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
		if got := DiskFullLogNote(path); got != "" {
			t.Errorf("note = %q, want none for a phrase before the last %d bytes", got, diskFullScanBytes)
		}
		body = strings.Repeat("x", diskFullScanBytes+10) + "\n" + "w: " + phraseNoSpace + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := DiskFullLogNote(path); got != wantDiskNote {
			t.Errorf("note = %q, want %q", got, wantDiskNote)
		}
	})
}

// gateLogWith writes a check log carrying body and returns its path.
func gateLogWith(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate.log")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNameDiskFullSetsNoteOnFail pins that a failed check whose log reports a
// full disk carries the environment note, while a pass and a failure without
// the phrase are left alone.
func TestNameDiskFullSetsNoteOnFail(t *testing.T) {
	t.Parallel()

	logPath := gateLogWith(t, "compile\nlink: No space left on device\n")
	rec := &store.GateRecord{Command: "make check", Result: "fail", LogPath: logPath}
	NameDiskFull(rec)
	if rec.Note != wantDiskNote {
		t.Errorf("note = %q, want %q", rec.Note, wantDiskNote)
	}

	pass := &store.GateRecord{Command: "make check", Result: "pass", LogPath: logPath}
	NameDiskFull(pass)
	if pass.Note != "" {
		t.Errorf("note = %q, want none for a pass", pass.Note)
	}

	cleanLog := gateLogWith(t, "all fine\n")
	clean := &store.GateRecord{Command: "make check", Result: "fail", LogPath: cleanLog}
	NameDiskFull(clean)
	if clean.Note != "" {
		t.Errorf("note = %q, want none for a failure without the phrase", clean.Note)
	}
}

// TestNameDiskFullSetsNoteOnTimeout pins that a timed-out check whose log
// reports a full disk carries the environment note.
func TestNameDiskFullSetsNoteOnTimeout(t *testing.T) {
	t.Parallel()

	logPath := gateLogWith(t, "tar: no space left on device\n")
	rec := &store.GateRecord{Command: "make check", Result: "timeout", LogPath: logPath}
	NameDiskFull(rec)
	if rec.Note != wantDiskNote {
		t.Errorf("note = %q, want %q", rec.Note, wantDiskNote)
	}
}

// diskChainRow plants a chain with one check run carrying note, and returns
// the row and the run number.
func diskChainRow(t *testing.T, st *store.Store, note string) (db.ChainRow, int) {
	t.Helper()
	c := db.ChainRow{
		ID: db.NewID(), Name: "shop", Status: string(workflow.StatusRunning),
		Phase: "build", Step: "checking", Plan: 1, Plans: 1,
		Builder: "shop", Worktree: "/repo/shop", CreatedAt: baseTime, UpdatedAt: baseTime,
	}
	if err := st.WithLock(func(tx *store.Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}
	row := db.ChainCheckRow{
		Run: 1, Step: "check", Command: "make check",
		Result: "fail", Note: note, CreatedAt: baseTime,
	}
	if err := st.WithLock(func(tx *store.Tx) error { return tx.ChainCheckPut(c.Name, row) }); err != nil {
		t.Fatalf("ChainCheckPut: %v", err)
	}
	return c, 1
}

// TestChainHaltOnEnvironmentHalts pins that a settled check carrying an
// environment note halts the chain through the engine's transition.
func TestChainHaltOnEnvironmentHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	c, run := diskChainRow(t, st, wantDiskNote)

	var gotRow db.ChainCheckRow
	deps := Deps{
		Store: st,
		HaltChainEnv: func(_ context.Context, _ *store.Tx, _ db.ChainRow, row db.ChainCheckRow, _ workflow.Event) (bool, error) {
			gotRow = row
			return true, nil
		},
	}

	var halted bool
	err := st.WithLock(func(tx *store.Tx) error {
		var herr error
		halted, herr = ChainHaltOnEnvironment(context.Background(), deps, tx, c, run, workflow.Event{})
		return herr
	})
	if err != nil {
		t.Fatalf("ChainHaltOnEnvironment: %v", err)
	}
	if !halted {
		t.Error("halted = false, want the environment note to halt the chain")
	}
	if gotRow.Note != wantDiskNote {
		t.Errorf("halted on note %q, want %q", gotRow.Note, wantDiskNote)
	}
}

// TestChainHaltOnEnvironmentKeepsRedCheck pins that a settled check carrying
// an ordinary failure note does not halt: only an environment note stops the
// repair loop.
func TestChainHaltOnEnvironmentKeepsRedCheck(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	c, run := diskChainRow(t, st, "--- FAIL: TestX")

	called := false
	deps := Deps{
		Store: st,
		HaltChainEnv: func(_ context.Context, _ *store.Tx, _ db.ChainRow, _ db.ChainCheckRow, _ workflow.Event) (bool, error) {
			called = true
			return true, nil
		},
	}

	var halted bool
	err := st.WithLock(func(tx *store.Tx) error {
		var herr error
		halted, herr = ChainHaltOnEnvironment(context.Background(), deps, tx, c, run, workflow.Event{})
		return herr
	})
	if err != nil {
		t.Fatalf("ChainHaltOnEnvironment: %v", err)
	}
	if halted || called {
		t.Error("halted a plain red check, want the repair loop to keep it")
	}
}

// haltStub is the engine halt the disk-full paths drive: it records the
// message and answers the binding unchanged.
type haltStub struct {
	message string
	calls   int
}

func (f *haltStub) halt(_ context.Context, _ *store.Tx, b store.Binding, message string) (store.Binding, error) {
	f.message = message
	f.calls++
	return b, nil
}

func diskHaltDeps(st *store.Store, halt *haltStub) Deps {
	return Deps{
		Store:       st,
		Preflight:   func(string) error { return nil },
		Halt:        halt.halt,
		ShowCommand: func(name string, round int, section string) string { return "show " + name + " " + section },
	}
}

// TestHaltOnEnvironmentGateHalts pins that a binding whose just-closed round's
// gate carries an environment note halts before any repair round is bought.
func TestHaltOnEnvironmentGateHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}
	rec := &store.GateRecord{Command: "make check", Result: "fail", Note: wantDiskNote}

	halt := &haltStub{}
	deps := diskHaltDeps(st, halt)

	var halted bool
	err := st.WithLock(func(tx *store.Tx) error {
		_, h, herr := HaltOnEnvironmentGate(context.Background(), deps, tx, b, rec, 1)
		halted = h
		return herr
	})
	if err != nil {
		t.Fatalf("HaltOnEnvironmentGate: %v", err)
	}
	if !halted {
		t.Fatal("halted = false, want the environment note to halt the binding")
	}
	if !strings.Contains(halt.message, wantDiskNote) {
		t.Errorf("halt message = %q, want it to name the environment note", halt.message)
	}
	if !strings.Contains(halt.message, "show webshop gate") {
		t.Errorf("halt message = %q, want it to point at the gate", halt.message)
	}
}

// TestHaltOnEnvironmentGateKeepsRedGate pins that an ordinary red gate, a pass
// note, a missing record and a binding already waiting on a human do not halt.
func TestHaltOnEnvironmentGateKeepsRedGate(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}

	cases := []struct {
		name string
		b    store.Binding
		rec  *store.GateRecord
	}{
		{"ordinary red gate", b, &store.GateRecord{Command: "make check", Result: "fail", Note: "--- FAIL: TestX"}},
		{"missing record", b, nil},
		{"already needs you", store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateNeedsYou}, &store.GateRecord{Command: "make check", Result: "fail", Note: wantDiskNote}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			halt := &haltStub{}
			deps := diskHaltDeps(st, halt)
			var halted bool
			err := st.WithLock(func(tx *store.Tx) error {
				_, h, herr := HaltOnEnvironmentGate(context.Background(), deps, tx, tc.b, tc.rec, 1)
				halted = h
				return herr
			})
			if err != nil {
				t.Fatalf("HaltOnEnvironmentGate: %v", err)
			}
			if halted || halt.calls != 0 {
				t.Errorf("halted an ordinary gate, want the repair path to keep it")
			}
		})
	}
}

// errRefusedDisk is the preflight answer when the tree is short of space.
var errRefusedDisk = errors.New("disk full: /repo has 1.0 GB free (< 2 GB)")

// TestHaltOnDiskFullExitHalts pins that a builder exit whose tail reports a
// full disk halts when the free-space probe refuses too, naming the phrase and
// the halt's show command.
func TestHaltOnDiskFullExitHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}

	var probed string
	halt := &haltStub{}
	deps := diskHaltDeps(st, halt)
	deps.Preflight = func(tree string) error {
		probed = tree
		return errRefusedDisk
	}

	var halted bool
	err := st.WithLock(func(tx *store.Tx) error {
		_, h, herr := HaltOnDiskFullExit(context.Background(), deps, tx, b, "starting\ncp: No space left on device\n")
		halted = h
		return herr
	})
	if err != nil {
		t.Fatalf("HaltOnDiskFullExit: %v", err)
	}
	if !halted {
		t.Fatal("halted = false, want the refused probe to halt the round")
	}
	if probed != "/repo" {
		t.Errorf("probed = %q, want the binding's tree", probed)
	}
	for _, want := range []string{"the output also reports", "no space left on device", "show webshop log"} {
		if !strings.Contains(halt.message, want) {
			t.Errorf("halt message = %q, want it to contain %q", halt.message, want)
		}
	}
}

// TestHaltOnDiskFullExitProbesWorktree pins that a binding with a worktree
// probes the worktree, not the bare CWD.
func TestHaltOnDiskFullExitProbesWorktree(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", Worktree: "/wt/shop", Round: 1, State: store.StateActive}

	var probed string
	halt := &haltStub{}
	deps := diskHaltDeps(st, halt)
	deps.Preflight = func(tree string) error {
		probed = tree
		return nil
	}

	var halted bool
	err := st.WithLock(func(tx *store.Tx) error {
		_, h, herr := HaltOnDiskFullExit(context.Background(), deps, tx, b, "cp: No space left on device\n")
		halted = h
		return herr
	})
	if err != nil {
		t.Fatalf("HaltOnDiskFullExit: %v", err)
	}
	if halted || halt.calls != 0 {
		t.Errorf("halted on a fine disk, want the normal exit path")
	}
	if probed != "/wt/shop" {
		t.Errorf("probed = %q, want the worktree", probed)
	}
}

// TestHaltOnDiskFullExitKeepsOrdinaryExit pins that a tail without the error
// form, a quoted phrase and a cleared disk all fall through to the normal
// exit handling.
func TestHaltOnDiskFullExitKeepsOrdinaryExit(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}

	cases := []struct {
		name      string
		tail      string
		preflight error
	}{
		{"no phrase", "starting\nno space left on disk\n", errRefusedDisk},
		{"quoted phrase", "starting\nphraseNoSpace = \"no space left on device\"\n", errRefusedDisk},
		{"cleared disk", "starting\ncp: No space left on device\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			halt := &haltStub{}
			deps := diskHaltDeps(st, halt)
			deps.Preflight = func(string) error { return tc.preflight }
			var halted bool
			err := st.WithLock(func(tx *store.Tx) error {
				_, h, herr := HaltOnDiskFullExit(context.Background(), deps, tx, b, tc.tail)
				halted = h
				return herr
			})
			if err != nil {
				t.Fatalf("HaltOnDiskFullExit: %v", err)
			}
			if halted || halt.calls != 0 {
				t.Errorf("halted %q, want the normal exit path", tc.tail)
			}
		})
	}
}

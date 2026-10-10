package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainRenderBody is the sealed round file the render tests copy out.
const chainRenderBody = "DIFF-BODY"

// chainRenderFixture is a store with one binding round 1 whose diff is a sealed
// row-only key, the chain record that names it, and the key itself.
func chainRenderFixture(t *testing.T) (Runtime, db.ChainRow, string) {
	t.Helper()
	rt := newRuntime(t)
	if err := rt.Store.Save(store.Binding{Name: "shop", CWD: "/work", Round: 1}); err != nil {
		t.Fatalf("Save shop: %v", err)
	}
	key := rt.Store.DiffPath("shop", 1)
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("shop", 1, key, []byte(chainRenderBody))
	}); err != nil {
		t.Fatalf("PutRoundFile: %v", err)
	}
	c := db.ChainRow{Name: "shop", Builder: "shop", Base: "base-commit", Branch: "relevo/x"}
	return rt, c, key
}

// renderSeed runs chainRenderSeed under the store lock, as the daemon does.
func renderSeed(t *testing.T, rt Runtime, c db.ChainRow, def workflow.Definition, st workflow.State, seed string) string {
	t.Helper()
	var got string
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = chainRenderSeed(rt, tx, c, def, st, seed)
		return err
	})
	if err != nil {
		t.Fatalf("chainRenderSeed: %v", err)
	}
	return got
}

func chainRenderState(key string) workflow.State {
	return workflow.State{
		Results: map[string]workflow.Result{
			"build": {Round: 1, Status: "done", Artifacts: map[string][]string{"diff": {key}}},
		},
	}
}

// TestRenderSeedPathsGoThroughChainSeedInput pins the copy: a reference to a
// sealed round-file key renders the copy chainSeedInput made under the chain's
// directory holding the key's bytes, never the key itself.
func TestRenderSeedPathsGoThroughChainSeedInput(t *testing.T) {
	t.Parallel()
	rt, c, key := chainRenderFixture(t)
	got := renderSeed(t, rt, c, workflow.Default(), chainRenderState(key), "the round diff: {{build.diff}}.")

	copyPath, ok := rt.Store.ChainInputPath("shop", key)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", key)
	}
	if !strings.Contains(got, copyPath) {
		t.Fatalf("seed does not name the copy %s:\n%s", copyPath, got)
	}
	if strings.Contains(got, key) {
		t.Fatalf("seed names the raw key %s:\n%s", key, got)
	}
	body, err := rt.Store.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if string(body) != chainRenderBody {
		t.Fatalf("copy = %q, want %q", body, chainRenderBody)
	}
}

// A round file still on disk is copied too: a seal removes it once its round is
// sealed, which can land between the seed being written and the runner opening
// it, so the seed must never name the original.
func TestRenderSeedCopiesARoundFileStillOnDisk(t *testing.T) {
	t.Parallel()
	rt, c, _ := chainRenderFixture(t)
	prompt := rt.Store.PromptPath("shop", 1)
	if err := os.WriteFile(prompt, []byte("PROMPT-BODY"), 0o644); err != nil {
		t.Fatalf("write the prompt: %v", err)
	}
	got := renderSeed(t, rt, c, workflow.Default(), chainRenderState(prompt), "the prompt: {{build.diff}}.")

	copyPath, ok := rt.Store.ChainInputPath("shop", prompt)
	if !ok || !strings.Contains(got, copyPath) || strings.Contains(got, prompt+".") {
		t.Fatalf("seed names %q, want the copy %s and never the original", got, copyPath)
	}
	if err := os.Remove(prompt); err != nil {
		t.Fatalf("remove the original, as a seal does: %v", err)
	}
	if body, err := os.ReadFile(copyPath); err != nil || string(body) != "PROMPT-BODY" {
		t.Fatalf("copy after the original went = %q, %v; want the prompt's bytes", body, err)
	}
}

// TestRenderSeedSingleRefHandsBytes pins the single-reference seed: exactly one
// file reference hands that file's bytes over, as a plan is handed over.
func TestRenderSeedSingleRefHandsBytes(t *testing.T) {
	t.Parallel()
	rt, c, key := chainRenderFixture(t)
	got := renderSeed(t, rt, c, workflow.Default(), chainRenderState(key), "{{build.diff}}")
	if got != chainRenderBody {
		t.Fatalf("single-reference seed = %q, want the file's bytes %q", got, chainRenderBody)
	}
}

// TestRenderSeedTaskInline pins the task reference: it renders the task file's
// text inline, the one input that is never a path.
func TestRenderSeedTaskInline(t *testing.T) {
	t.Parallel()
	rt, c, _ := chainRenderFixture(t)
	taskPath := rt.Store.ChainTaskPath("shop")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatalf("make the chain dir: %v", err)
	}
	if err := os.WriteFile(taskPath, []byte("do the thing"), 0o644); err != nil {
		t.Fatalf("write the task: %v", err)
	}
	got := renderSeed(t, rt, c, workflow.Default(), workflow.State{}, "Task: {{task}}")
	if got != "Task: do the thing" {
		t.Fatalf("task seed = %q, want %q", got, "Task: do the thing")
	}
}

// TestRenderSeedAllWritesListFile pins the list reference: it writes one file
// under the chain's input directory holding every item, and the seed names that
// file.
func TestRenderSeedAllWritesListFile(t *testing.T) {
	t.Parallel()
	rt, c, _ := chainRenderFixture(t)
	st := workflow.State{Iter: map[string]workflow.Iter{"plans": {Index: 0, Items: []string{"plan-1.md", "plan-2.md"}}}}
	got := renderSeed(t, rt, c, workflow.Default(), st, "Plans: {{plans.all}}")

	path := filepath.Join(rt.Store.ChainInputDir("shop"), "plans-all.list")
	if !strings.Contains(got, path) {
		t.Fatalf("seed does not name the list file %s:\n%s", path, got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the list file: %v", err)
	}
	if want := "plan-1.md\nplan-2.md\n"; string(body) != want {
		t.Fatalf("list file = %q, want %q", body, want)
	}
}

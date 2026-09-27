package e2e

// TestHeadlessE2EReaderRound is the reader round end to end: a reviewer
// binding shares a writer's tree, runs in a throwaway scratch copy, fills its
// artifact directory, closes on its marker with a summary.md relevo took from
// its final message, and leaves the writer's tree byte-for-byte as it was.
//
// The fake `claude` on PATH branches on the reader prompt: when it names an
// artifact directory it writes index.html and style.css there, edits a file in
// its throwaway tree, creates the marker, then prints a final result ending in
// a relevo block and exits. No network, no real harness.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// The two bindings this scenario creates: a writer that owns the tree and a
// reviewer that shares it.
const (
	writerBinding = "e2e-reader-writer"
	readerBinding = "e2e-reader"
)

// fakeReaderFinal is the final message the fake harness prints for a reader
// round: the text relevo records as summary.md, ending in a relevo block.
const fakeReaderFinal = "# Reader summary\n\n" +
	"index.html and style.css are in the artifact directory.\n\n" +
	"```relevo\n" +
	"status: done\n" +
	"halted_at: \"\"\n" +
	"changed_paths: []\n" +
	"commands_run: []\n" +
	"not_done: []\n" +
	"```\n"

// fakeReaderSummary is fakeReaderFinal minus its relevo block: what summary.md
// must hold after the close, so no stray block reaches the planner.
const fakeReaderSummary = "# Reader summary\n\n" +
	"index.html and style.css are in the artifact directory.\n"

// fakeReaderStreamLine is the one claude stream-json line the fake harness
// prints for a reader round, carrying fakeReaderFinal as its result text.
func fakeReaderStreamLine(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": fakeReaderFinal,
	})
	if err != nil {
		t.Fatalf("encode the fake reader stream line: %v", err)
	}
	return string(raw)
}

func TestHeadlessE2EReaderRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("PATH", writeFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeCandidatesAndPolicy(t, configDir)
	root := filepath.Join(home, ".local", "state", "relevo")
	rt, reg := newHeadlessRuntime(t, root, configDir)

	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("write the dirty file: %v", err)
	}
	beforeStatus := runGit(t, repo, "status", "--porcelain")
	beforeReadme := readFile(t, filepath.Join(repo, "README.md"))

	rec, _, err := planner.Init(reg, planner.InitInput{
		Kind: "claude", SessionID: "e2e-reader-planner", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the planner: %v", err)
	}

	// A writer binding owns the tree; a reviewer binding shares it.
	if _, err := relevo.Bind(ctx, rt, relevo.BindOptions{
		Name: writerBinding, CWD: repo, PlannerID: rec.ID,
	}); err != nil {
		t.Fatalf("bind the writer: %v", err)
	}
	if _, err := relevo.Bind(ctx, rt, relevo.BindOptions{
		Name: readerBinding, Role: "reviewer", CWD: repo, PlannerID: rec.ID,
	}); err != nil {
		t.Fatalf("bind the reviewer: %v", err)
	}

	t.Cleanup(func() { stopRecordedBuilders(t, rt, writerBinding, readerBinding) })

	plan := writePlan(t, "reader.md", "# Review\n\nOne line of reading.\n")
	if _, err := relevo.Send(ctx, rt, readerBinding, plan, relevo.SendOptions{}); err != nil {
		t.Fatalf("send the reviewer: %v", err)
	}

	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)
	tickUntilRoundCloses(t, ctx, daemon, rt, readerBinding)

	// The round closed on the fake harness's own marker, without a note: a
	// fallback close would carry one.
	entry, ok := reportEntry(t, rt, readerBinding, 1)
	if !ok {
		t.Fatalf("binding %s round 1 has no report entry", readerBinding)
	}
	if entry.Note != "" {
		t.Fatalf("binding %s round 1 closed with note %q, want \"\":\n%s",
			readerBinding, entry.Note, builderLogTail(rt, readerBinding, 1))
	}

	artifact := rt.Store.ArtifactDir(readerBinding, 1, "reviewer")
	summary := rt.Store.SummaryPath(readerBinding, 1, "reviewer")
	for _, path := range []string{
		summary,
		filepath.Join(artifact, "index.html"),
		filepath.Join(artifact, "style.css"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s does not exist after the reader round closed: %v", path, err)
		}
	}
	if got := readFile(t, summary); got != fakeReaderSummary {
		t.Fatalf("summary.md does not equal the final message with its block stripped:\ngot:\n%q\nwant:\n%q", got, fakeReaderSummary)
	}
	if entry.Outcome != "done" {
		t.Errorf("report entry Outcome = %q, want %q", entry.Outcome, "done")
	}
	if !strings.Contains(entry.Payload, "Findings: relevo show e2e-reader --round 1 --summary") {
		t.Errorf("report payload does not name the reader summary:\n%s", entry.Payload)
	}
	if entry.Path != summary {
		t.Fatalf("report entry Path = %q, want the summary path %s", entry.Path, summary)
	}

	// The writer's tree is exactly as it was: the reader's edit lives only in
	// its throwaway copy, which is gone.
	if got := runGit(t, repo, "status", "--porcelain"); got != beforeStatus {
		t.Errorf("the binding tree's status changed:\nbefore:\n%s\nafter:\n%s", beforeStatus, got)
	}
	if got := readFile(t, filepath.Join(repo, "README.md")); got != beforeReadme {
		t.Errorf("README.md = %q, want it unchanged %q", got, beforeReadme)
	}
	if _, err := os.Stat(rt.Store.ScratchWorktreePath(readerBinding, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the reader's scratch worktree is still present: %v", err)
	}

	// A seal turns the closed round's files into rows: done, then one tick.
	if _, err := relevo.Done(ctx, rt, readerBinding); err != nil {
		t.Fatalf("done the reviewer: %v", err)
	}
	if err := daemon.Tick(ctx); err != nil {
		t.Fatalf("daemon.Tick: %v", err)
	}
	names, err := rt.Store.RoundFiles(readerBinding)
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	for _, want := range []string{
		"001-reviewer/index.html",
		"001-reviewer/style.css",
		"001-reviewer/summary.md",
	} {
		if !contains(names, want) {
			t.Errorf("sealed round files %v do not hold %s", names, want)
		}
	}
	body, err := rt.Store.ReadFile(summary)
	if err != nil {
		t.Fatalf("the sealed summary.md is not readable: %v", err)
	}
	if !strings.Contains(string(body), "index.html and style.css") {
		t.Errorf("the sealed summary.md does not carry the final message:\n%s", body)
	}
}

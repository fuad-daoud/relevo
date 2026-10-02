package e2e

// TestChainForkE2E is the fork slice's integration pin: a two-child fork run
// end to end by the fake `claude` on PATH, twice -- once where the two children
// edit different files and the merge joins, and once where they edit the same
// file and the conflict is routed to a merge round that resolves it.
//
// The scenario the plan pins:
//
//	1  the same isolation TestChainE2E runs under -- HOME and the XDG dirs under
//	   a temp root, the fork fake harness first on PATH, dbtest.InstallOwner, and
//	   a throwaway repo to cut the chain's tree from;
//	2  a mastermind is registered, and two chains run the same parent workflow
//	   file: a build step, a split fork of two children off one one-step child
//	   workflow, a check after joined, and a builder merge round on conflict;
//	3  the daemon ticks each chain until its row is no longer running, bounded,
//	   with the invariant that holds only while a fork tree runs checked on every
//	   tick: no member of it holds a pending delivery, a child included;
//	4  the clean chain ends done, both children's branch tips are reachable from
//	   the parent's, both children's files are in the parent's tree, and the
//	   MasterMind holds exactly one delivery -- the parent's;
//	5  the conflicting chain ends done as well: the fork routed conflict, the
//	   merge round's seed named the conflict report and the report named the path
//	   both children wrote, the builder's own round settled the merge in the tree
//	   MergeKeep left unfinished, and the check after it went green.
//
// Which file each child writes is stated in the workflow file, not in the test,
// so the clean and conflicting runs differ only in the two tasks the fork hands
// its children. Every wait is bounded and names what it was waiting for, so a
// stuck fork fails with its row and its members' notes rather than hanging.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// forkChildWorkflow is the one-step child both forks run. Its seed's first line
// carries the task the fork gave this child, so the fake harness can tell which
// child of which fork it is serving from the seed alone.
const forkChildWorkflow = `name: fork-child
inputs: { task: required }
start: work
steps:
  work: { run: builder, seed: "Child round: edit {{task}}.", on: { done: done } }
`

// forkParentWorkflow renders the parent workflow a fork chain runs. childPath
// is the file each child resolves, and the two tasks are what the two children
// are handed; naming the same file in both is what makes the merge conflict.
func forkParentWorkflow(childPath, taskOne, taskTwo string) string {
	return `name: fork-parent
inputs: { task: required }
start: build
steps:
  build: { run: builder, seed: "Parent build round for {{task}}.", on: { done: split } }
  split:
    fork:
      children:
        - { workflow: "` + childPath + `", task: "` + taskOne + `" }
        - { workflow: "` + childPath + `", task: "` + taskTwo + `" }
    on: { joined: verify, conflict: resolve }
  resolve: { run: builder, seed: "The merge conflicted. Resolve it:\n{{split.conflict}}", on: { done: verify } }
  verify: { check: "true", on: { green: done, red: { halt: "the check after the merge went red" } } }
`
}

func TestChainForkE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// -- 1. The same isolation the chain e2es run under ----------------------
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CLAUDECODE", "1")

	ownerCleanup, err := dbtest.InstallOwner()
	if err != nil {
		t.Fatalf("install the in-process owner: %v", err)
	}
	t.Cleanup(ownerCleanup)

	t.Setenv("PATH", writeForkFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeForkCandidatesAndPolicy(t, configDir)
	root := filepath.Join(home, ".local", "state", "relevo")
	rt, reg := newHeadlessRuntime(t, root, configDir)

	repo := newRepo(t)
	t.Chdir(repo)

	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind: "claude", SessionID: "e2e-fork-mastermind", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the mastermind: %v", err)
	}
	childPath := writePlan(t, "fork-child.yaml", forkChildWorkflow)
	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)

	// -- 2/3/4. The clean join ------------------------------------------------
	const cleanName = "forkclean"
	cleanWorkflow := writePlan(t, "fork-clean.yaml", forkParentWorkflow(childPath,
		"fork-one.txt written by child one", "fork-two.txt written by child two"))
	clean := forkStartChain(t, ctx, rt, rec.ID, cleanName, cleanWorkflow)
	t.Cleanup(func() { stopRecordedBuilders(t, rt, clean...) })
	tickUntilChainFinishes(t, ctx, daemon, rt, cleanName, func() {
		chainAssertNoMemberPending(t, rt, clean...)
	})

	if row := chainE2ERow(t, rt, cleanName); row.Status != string(chain.StatusDone) {
		t.Fatalf("the clean fork ended status %q reason %q, want done", row.Status, row.Reason)
	}
	for _, key := range []string{"1", "2"} {
		child := chainE2ERow(t, rt, cleanName+"."+key)
		if child.Parent != cleanName {
			t.Errorf("child %s records parent %q, want %q", child.Name, child.Parent, cleanName)
		}
		if child.Status != string(chain.StatusDone) {
			t.Errorf("child %s ended status %q reason %q, want done", child.Name, child.Status, child.Reason)
		}
	}

	// A clean join is exactly this: every child's branch tip is reachable from
	// the parent's, so the parent's tree carries the children's work. Checking
	// reachability rather than the files alone is what fails when the merge ran
	// before the last child ended, because a branch that was never merged has no
	// commit at all and the join could not have named it.
	cleanTree := forkWorktree(t, rt, cleanName)
	for _, key := range []string{"1", "2"} {
		ref := "relevo/" + cleanName + "." + key
		if !forkReachable(t, cleanTree, ref) {
			t.Errorf("the parent tree does not hold %s: the merge did not join every child", ref)
		}
	}
	for _, file := range []string{"fork-one.txt", "fork-two.txt"} {
		if _, err := os.Stat(filepath.Join(cleanTree, file)); err != nil {
			t.Errorf("the parent tree holds no %s (err %v): a clean merge brings both children's files", file, err)
		}
	}

	check, err := rt.Store.ChainCheck(cleanName, 1)
	if err != nil {
		t.Fatalf("ChainCheck %s run 1: %v", cleanName, err)
	}
	if check.Command != "true" || check.Result != "pass" {
		t.Errorf("the check after the join ran command %q result %q, want true/pass", check.Command, check.Result)
	}

	cleanTrace := relevo.RenderTrace(mustChainTrace(t, ctx, rt, cleanName))
	for _, want := range []string{"forked 1, 2", "merged → joined"} {
		if !strings.Contains(cleanTrace, want) {
			t.Errorf("the clean fork's trace does not carry %q:\n%s", want, cleanTrace)
		}
	}
	// A child reads its own trace, under a header naming the parent it forked
	// from; the parent's trace carries no child rows of its own.
	childDoc := mustChainTrace(t, ctx, rt, cleanName+".1")
	if childDoc.Parent != cleanName {
		t.Errorf("child %s.1's trace names parent %q, want %q", cleanName+".1", childDoc.Parent, cleanName)
	}
	if childTrace := relevo.RenderTrace(childDoc); !strings.Contains(childTrace, "child of "+cleanName) {
		t.Errorf("child %s.1's trace has no parent header:\n%s", cleanName+".1", childTrace)
	}
	if strings.Contains(cleanTrace, "child of ") {
		t.Errorf("the parent trace carries a child header:\n%s", cleanTrace)
	}

	// Exactly one delivery, and it is the parent's: a child ends into its
	// parent's fork step, not into a MasterMind mailbox.
	forkAssertOneDelivery(t, rt, cleanName, clean[1:])

	// -- 5. The conflict, routed to a merge round ----------------------------
	const clashName = "forkclash"
	clashWorkflow := writePlan(t, "fork-clash.yaml", forkParentWorkflow(childPath,
		"fork-shared.txt written by child one", "fork-shared.txt written by child two"))
	clash := forkStartChain(t, ctx, rt, rec.ID, clashName, clashWorkflow)
	t.Cleanup(func() { stopRecordedBuilders(t, rt, clash...) })
	tickUntilChainFinishes(t, ctx, daemon, rt, clashName, func() {
		chainAssertNoMemberPending(t, rt, clash...)
	})

	if row := chainE2ERow(t, rt, clashName); row.Status != string(chain.StatusDone) {
		t.Fatalf("the conflicting fork ended status %q reason %q, want done: the merge round was meant to resolve it",
			row.Status, row.Reason)
	}
	clashTrace := relevo.RenderTrace(mustChainTrace(t, ctx, rt, clashName))
	for _, want := range []string{"forked 1, 2", "merged → conflict"} {
		if !strings.Contains(clashTrace, want) {
			t.Errorf("the conflicting fork's trace does not carry %q:\n%s", want, clashTrace)
		}
	}
	if strings.Contains(clashTrace, "joined") {
		t.Errorf("the conflicting fork's trace claims a clean join:\n%s", clashTrace)
	}

	// The conflict edge hands its builder the conflict report, so the merge
	// round's seed names a copy under the chain's own input directory -- a file
	// the runner could open while the chain ran, and one the chain sweeps when
	// it ends done. The report itself is a round file, so it is read through the
	// store after the fact; it must name the path both children wrote.
	prompt := chainStaged(t, rt, rt.Store.PromptPath(clashName, 2))
	if !strings.Contains(prompt, "Resolve it:") {
		t.Fatalf("the merge round's seed is not the conflict seed:\n%s", prompt)
	}
	if !forkSeedNamesConflictCopy(prompt) {
		t.Errorf("the merge round's seed names no openable conflict report copy:\n%s", prompt)
	}
	report := chainStaged(t, rt, rt.Store.ForkConflictPath(clashName, 1))
	if !strings.Contains(report, "fork-shared.txt") {
		t.Errorf("the conflict report = %q, want it to name the collided path fork-shared.txt", report)
	}

	// The builder resolved the merge in the very tree the merge left unfinished,
	// so the parent branch now carries both children's commits and holds a
	// resolved file where the markers were.
	clashTree := forkWorktree(t, rt, clashName)
	for _, key := range []string{"1", "2"} {
		ref := "relevo/" + clashName + "." + key
		if !forkReachable(t, clashTree, ref) {
			t.Errorf("the resolved parent tree does not hold %s: the merge round did not finish the merge", ref)
		}
	}
	resolved := readFile(t, filepath.Join(clashTree, "fork-shared.txt"))
	if strings.Contains(resolved, "<<<<<<<") {
		t.Errorf("the resolved file still carries conflict markers:\n%s", resolved)
	}
	if resolved != forkResolvedBody {
		t.Errorf("the resolved file = %q, want %q", resolved, forkResolvedBody)
	}
	if check, err := rt.Store.ChainCheck(clashName, 1); err != nil || check.Result != "pass" {
		t.Errorf("the check after the merge round ran result %q (err %v), want pass", check.Result, err)
	}
	forkAssertOneDelivery(t, rt, clashName, clash[1:])
}

// forkResolvedBody is what the fake harness writes into every conflicted path:
// the merge round's own resolution, so the test can tell a resolved file from a
// marker-carrying one without parsing git's markers.
const forkResolvedBody = "resolved by the merge round\n"

// forkStartChain starts one fork chain and returns the names every member of
// its tree runs under: the parent, then each child in key order.
func forkStartChain(t *testing.T, ctx context.Context, rt relevo.Runtime, mastermindID, name, workflowPath string) []string {
	t.Helper()
	if _, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name: name, Task: "build the thing, then fork it", Workflow: workflowPath,
		Feature: "fork-e2e", MasterMindID: mastermindID,
	}); err != nil {
		t.Fatalf("ChainStart %s: %v", name, err)
	}
	return []string{name, name + ".1", name + ".2"}
}

// mustChainTrace reads a chain's trace document, failing the test when it cannot.
func mustChainTrace(t *testing.T, ctx context.Context, rt relevo.Runtime, name string) relevo.ChainTraceDoc {
	t.Helper()
	doc, err := relevo.ChainTrace(ctx, rt, name)
	if err != nil {
		t.Fatalf("ChainTrace %s: %v", name, err)
	}
	return doc
}

// forkWorktree is the tree a fork's merge runs in: the parent writer's own
// working directory, which is also where its merge round resolves.
func forkWorktree(t *testing.T, rt relevo.Runtime, chainName string) string {
	t.Helper()
	b, err := rt.Store.Load(chainName)
	if err != nil {
		t.Fatalf("load the writer binding %s: %v", chainName, err)
	}
	if b.CWD == "" {
		t.Fatalf("binding %s has no working directory: the merge has no tree", chainName)
	}
	return b.CWD
}

// forkReachable reports whether ref is an ancestor of the tree's HEAD, which is
// how a test reads "the parent branch holds that child's commits".
func forkReachable(t *testing.T, dir, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", ref, "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0")
	return cmd.Run() == nil
}

// forkAssertOneDelivery pins the delivery rule: the parent that ends a fork
// queues exactly one, and none of the children queued anything.
func forkAssertOneDelivery(t *testing.T, rt relevo.Runtime, parent string, children []string) {
	t.Helper()
	entry, found, err := rt.Store.PendingForMasterMind(parent)
	if err != nil {
		t.Fatalf("PendingForMasterMind(%s): %v", parent, err)
	}
	if !found || entry.Kind != store.KindChain {
		t.Fatalf("pending on %s = %+v (found %v), want the fork's one KindChain entry", parent, entry, found)
	}
	if !strings.Contains(entry.Payload, "chain "+parent+" finished") {
		t.Errorf("the end delivery payload = %q, want the fork's finished line", entry.Payload)
	}
	for _, child := range children {
		entry, found, err := rt.Store.PendingForMasterMind(child)
		if err != nil {
			t.Fatalf("PendingForMasterMind(%s): %v", child, err)
		}
		if found {
			t.Errorf("child %s holds a pending delivery %+v; a fork child's end is its parent's event", child, entry)
		}
	}
}

// forkSeedNamesConflictCopy reports whether a merge round's seed carries a
// conflict-report path on a line of its own. The rendered reference hands the
// builder a copy under the chain's input directory, so the line is a path the
// runner can open rather than a round-file key.
func forkSeedNamesConflictCopy(prompt string) bool {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "fork-conflict.txt") && strings.HasPrefix(line, "/") {
			return true
		}
	}
	return false
}

// writeForkCandidatesAndPolicy writes the config the fork chains resolve
// against: one claude candidate serving the builder role, and a policy that
// orders it. A fork needs a writer on the parent and on each child, so the
// builder role is all this run declares.
func writeForkCandidatesAndPolicy(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	token := "claude/anthropic/" + chainE2EModel
	candidates := `[{"harness":"claude","provider":"anthropic","model":"` + chainE2EModel + `","roles":["builder"]}]`
	pol := `{"order":{"builder":["` + token + `"]}}`
	rolesJSON := `{
  "builder": {
    "candidates": ["` + token + `"]
  }
}`
	for name, body := range map[string]string{
		"candidates.json": candidates,
		"policy.json":     pol,
		"roles.json":      rolesJSON,
	} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// writeForkFakeHarness writes an executable named `claude` -- a kind the
// candidate table knows -- into a temp dir and returns that dir, so the caller
// can put it first on PATH, the same wiring writeFakeHarness gives the other
// e2es.
func writeForkFakeHarness(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := strings.ReplaceAll(forkHarnessScript, "__REPORT__", fakeReportBody)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return dir
}

// forkHarnessScript is the fake harness the fork e2e's rounds run on. It does
// what a builder round needs -- the report, one commit in its own worktree, the
// done marker, one stream-json line -- with the round's work read from the
// seed's first line, so the workflow file alone decides which file each child
// writes and the merge they make is the test's to assert.
//
// A child round's seed names the file it writes as the first word of its task,
// and the whole task as the file's body: two children naming different files
// merge cleanly, and two naming the same file with different tasks conflict. The
// merge round's seed is the fork's conflict seed, and it resolves only a tree
// that is really holding an unfinished merge, so a merge that was undone instead
// of kept cannot be finished off-screen by this fake.
var forkHarnessScript = `#!/bin/sh
set -eu

prompt=""
for arg in "$@"; do
	case "$arg" in
	*"Your working tree is: "*) prompt="$arg" ;;
	esac
done
if [ -z "$prompt" ]; then
	echo "fork fake claude: relevo's handoff prompt is not in argv: $*" >&2
	exit 2
fi

worktree=$(printf '%s\n' "$prompt" | awk '/^Your working tree is: /{ sub(/^Your working tree is: /, ""); print; exit }')
plan=$(printf '%s\n' "$prompt" | awk '/^Read: /{ sub(/^Read: /, ""); print; exit }')
report=$(printf '%s\n' "$prompt" | awk '/^When you are done, write your report to: /{ sub(/^When you are done, write your report to: /, ""); print; exit }')
marker=$(printf '%s\n' "$prompt" | awk '/create this empty file: /{ sub(/.*create this empty file: /, ""); print; exit }')

if [ -z "$worktree" ] || [ -z "$plan" ] || [ -z "$report" ] || [ -z "$marker" ]; then
	echo "fork fake claude: the prompt is missing a path" >&2
	echo "tree=$worktree plan=$plan report=$report marker=$marker" >&2
	exit 2
fi
if [ ! -s "$plan" ]; then
	echo "fork fake claude: the staged seed $plan is missing or empty" >&2
	exit 2
fi

{
	printf 'seed read by the fork fake harness: %s\n' "$plan"
	printf 'working tree: %s\n\n' "$worktree"
	cat <<'RELEVO_FORK_REPORT'
__REPORT__
RELEVO_FORK_REPORT
} > "$report"

seed=$(head -n 1 "$plan")
case "$seed" in
"Child round: edit "*)
	task=$(printf '%s\n' "$seed" | sed -e 's/^Child round: edit //' -e 's/\.$//')
	file=${task%% *}
	printf '%s\n' "$task" > "$worktree/$file"
	;;
"The merge conflicted. Resolve it:"*)
	if git -C "$worktree" rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
		for path in $(git -C "$worktree" diff --name-only --diff-filter=U); do
			printf 'resolved by the merge round\n' > "$worktree/$path"
		done
		git -C "$worktree" add -A
		git -C "$worktree" commit -q -m "fork merge round: the conflict is resolved"
	else
		# Nothing to resolve: a merge that was undone rather than kept left no
		# conflict here. The fake resolves nothing rather than fabricating a
		# resolution, so the file stays as the children left it and the test
		# reads the difference.
		echo "fork fake claude: no merge in progress in $worktree" >&2
	fi
	;;
"Parent build round for "*)
	printf 'the parent built before it forked\n' > "$worktree/fork-parent.txt"
	;;
esac

git -C "$worktree" add -A
git -C "$worktree" commit -q -m "fork e2e: one round" || true

: > "$marker"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"fork round done"}'
`
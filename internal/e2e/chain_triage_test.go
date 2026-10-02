package e2e

// TestChainTriageE2E is the custom-workflow pin: a user workflow whose first
// step is a reader that answers yes or no, run end to end by the fake
// `claude` on PATH, with the daemon driving every round.
//
// The scenario the plan pins:
//
//	1  the same isolation TestChainE2E uses -- HOME and the XDG dirs under a
//	   temp root, the fake harness first on PATH, dbtest.InstallOwner, and a
//	   throwaway repo to cut the chain's tree from;
//	2  a mastermind is registered, and relevo.ChainStart runs a triage-first
//	   workflow with a task reading "answer yes" or "answer no";
//	3  the daemon ticks until the chain is no longer running, bounded;
//	4  the yes run goes triage -> build -> check (`true`) -> done, and the no
//	   run halts with "triage said no".
//
// Every wait is bounded and names what it was waiting for, so a stuck chain
// fails with its row and its members' notes rather than hanging the run.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db/dbtest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// triageWorkflow is the user workflow the triage e2e runs: a yes/no reader
// gates a build and a check. It is a YAML document, because the workflow a
// chain takes is a file.
const triageWorkflow = `name: triage-first
inputs: { task: required }
start: triage
steps:
  triage: { run: yes-no, seed: "Should we build this? {{task}}", on: { answer=yes: build, answer=no: { halt: "triage said no" } } }
  build: { run: builder, seed: "{{task}}", on: { done: check } }
  check: { check: "true", on: { green: done, red: { halt: "check red" } } }
`

func TestChainTriageE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// -- 1. The same isolation TestChainE2E runs under ----------------------
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

	t.Setenv("PATH", writeFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeTriageCandidatesAndPolicy(t, configDir)
	root := filepath.Join(home, ".local", "state", "relevo")
	rt, reg := newHeadlessRuntime(t, root, configDir)

	repo := newRepo(t)
	t.Chdir(repo)

	rec, _, err := mastermind.Init(reg, mastermind.InitInput{
		Kind: "claude", SessionID: "e2e-triage-mastermind", CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the mastermind: %v", err)
	}
	workflowPath := writePlan(t, "triage.yaml", triageWorkflow)

	// -- 2. The yes edge: triage -> build -> check -> done ------------------
	const yesName = "triageyes"
	if _, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name: yesName, Task: "answer yes", Workflow: workflowPath,
		Feature: "triage-e2e", MasterMindID: rec.ID,
	}); err != nil {
		t.Fatalf("ChainStart %s: %v", yesName, err)
	}
	t.Cleanup(func() { stopRecordedBuilders(t, rt, yesName, yesName+"-yes-no") })

	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)
	tickUntilChainFinishes(t, ctx, daemon, rt, yesName, nil)

	yesRow := chainE2ERow(t, rt, yesName)
	if yesRow.Status != string(chain.StatusDone) {
		t.Fatalf("yes chain ended status %q reason %q, want done", yesRow.Status, yesRow.Reason)
	}
	check, err := rt.Store.ChainCheck(yesName, 1)
	if err != nil {
		t.Fatalf("ChainCheck %s run 1: %v", yesName, err)
	}
	if check.Command != "true" || check.Result != "pass" {
		t.Errorf("check run = command %q result %q, want true/pass", check.Command, check.Result)
	}
	doc, err := relevo.ChainTrace(ctx, rt, yesName)
	if err != nil {
		t.Fatalf("ChainTrace %s: %v", yesName, err)
	}
	text := relevo.RenderTrace(doc)
	for _, want := range []string{"triage r1", "answer=yes", "build r1", "check run 1", "→ done"} {
		if !strings.Contains(text, want) {
			t.Errorf("the yes run's trace does not carry %q:\n%s", want, text)
		}
	}

	// -- 3. The no edge: triage halts the chain -----------------------------
	const noName = "triageno"
	if _, err := relevo.ChainStart(ctx, rt, relevo.ChainOptions{
		Name: noName, Task: "answer no", Workflow: workflowPath,
		Feature: "triage-e2e", MasterMindID: rec.ID,
	}); err != nil {
		t.Fatalf("ChainStart %s: %v", noName, err)
	}
	t.Cleanup(func() { stopRecordedBuilders(t, rt, noName, noName+"-yes-no") })

	tickUntilChainFinishes(t, ctx, daemon, rt, noName, nil)

	noRow := chainE2ERow(t, rt, noName)
	if noRow.Status != string(chain.StatusHalted) {
		t.Fatalf("no chain ended status %q reason %q, want halted", noRow.Status, noRow.Reason)
	}
	if !strings.Contains(noRow.Reason, "triage said no") {
		t.Errorf("no chain reason = %q, want it to carry %q", noRow.Reason, "triage said no")
	}
}

// writeTriageCandidatesAndPolicy writes the config the triage chain resolves
// against: the fake claude candidate and the yes-no reader that declares the
// answer outcome its workflow routes on.
func writeTriageCandidatesAndPolicy(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	token := "claude/anthropic/" + chainE2EModel
	candidates := `[{"harness":"claude","provider":"anthropic","model":"` + chainE2EModel + `","roles":["builder","researcher"]}]`
	pol := `{"order":{"builder":["` + token + `"],"researcher":["` + token + `"]}}`
	rolesJSON := `{
  "builder": {
    "candidates": ["` + token + `"]
  },
  "yes-no": {
    "shape": "reader",
    "definitions": {
      "claude": {"agent": "researcher"}
    },
    "candidates": ["` + token + `"],
    "outputs": {
      "answer": {"one-of": ["yes", "no"]}
    }
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

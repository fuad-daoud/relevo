package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainPlanArg is a --plan value for the refusals that fire before any plan is
// read: the path need not exist, because nothing opens it.
func chainPlanArg(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "plan.md")
}

// TestChainRequiresExactlyOneFeatureFlag pins the label rule at the CLI edge:
// neither flag and both flags are refused, in one line and with exit 2,
// before any runtime is built. CI has no harness, so a test that got past the
// refusal would fail on the environment rather than on the rule.
func TestChainRequiresExactlyOneFeatureFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"neither", []string{"chain", "--name", "shop", "--plan", chainPlanArg(t)}},
		{"both", []string{"chain", "--name", "shop", "--plan", chainPlanArg(t), "--feature", "auth", "--no-feature"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeRefused, "")
			if !strings.Contains(ce.message, "--feature") {
				t.Errorf("message = %q, want it to name --feature", ce.message)
			}
		})
	}
}

// TestChainRejectsAnEmptyPlanAtParseTime pins the plan shape at the CLI edge:
// no --plan, and a --plan with no path, are refused before any runtime is
// built.
func TestChainRejectsAnEmptyPlanAtParseTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"none", []string{"chain", "--name", "shop", "--feature", "auth"}},
		{"empty value", []string{"chain", "--name", "shop", "--feature", "auth", "--plan", "  "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeUsage, "")
			if !strings.Contains(ce.message, "--plan") {
				t.Errorf("message = %q, want it to name --plan", ce.message)
			}
		})
	}
}

// TestChainRefusesAnUnknownFlag pins that an unknown flag is a usage refusal,
// before any runtime is built.
func TestChainRefusesAnUnknownFlag(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--feature", "auth", "--plan", chainPlanArg(t), "--nope"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "nope") {
		t.Errorf("message = %q, want it to name the unknown flag", ce.message)
	}
}

// TestChainStartBadInputIsRefused pins the class at the CLI edge: the start's
// own input refusals (an empty plan file here) are refused with exit 2, never
// internal -- a user's bad file is not a relevo bug and gets no bugreport hint.
func TestChainStartBadInputIsRefused(t *testing.T) {
	plan := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(plan, nil, 0o644); err != nil {
		t.Fatalf("write empty plan: %v", err)
	}
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "cliemptyplan", "--plan", plan, "--feature", "auth"})
	})
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "is empty") {
		t.Errorf("message = %q, want the empty-plan text", ce.message)
	}
}

// TestChainResumeFlagRules pins the resume arm's own refusals, all of which
// fire before newRuntime: --resume is useless without a name, the feature flags
// are still exclusive (a resume may name neither, because the chain keeps its
// label), and the two security flags are still exclusive.
func TestChainResumeFlagRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code errorCode
		want string
	}{
		{
			"no name",
			[]string{"chain", "--resume"},
			codeUsage, "--name",
		},
		{
			"both feature flags",
			[]string{"chain", "--resume", "--name", "shop", "--feature", "auth", "--no-feature"},
			codeRefused, "--feature",
		},
		{
			"both security flags",
			[]string{"chain", "--resume", "--name", "shop", "--security", "--no-security"},
			codeUsage, "--security",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, tc.code, "")
			if !strings.Contains(ce.message, tc.want) {
				t.Errorf("message = %q, want it to name %q", ce.message, tc.want)
			}
		})
	}

	// A resume with no feature flag at all is not a feature refusal: it
	// reaches the store and reports the chain it cannot find.
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", "resumeflags"})
	})
	ce := requireCLIError(t, err, codeBindingNotFound, "")
	if strings.Contains(ce.message, "--feature") {
		t.Errorf("message = %q, want the missing chain, not the feature rule", ce.message)
	}
}

// TestChainResumeRefusesStartOnlyFlags pins the round-6 review's refusal: a
// resume takes the chain's name and the settings, never a start's own flags.
// Each is a usage error that names the flag, made before any runtime is built,
// so a caller who passes one cannot believe it took effect. Parse-only: no
// state is opened and no harness is needed.
func TestChainResumeRefusesStartOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"plan", []string{"chain", "--resume", "--name", "shop", "--plan", chainPlanArg(t)}},
		{"base", []string{"chain", "--resume", "--name", "shop", "--base", "HEAD"}},
		{"ticket", []string{"chain", "--resume", "--name", "shop", "--ticket", "#1"}},
		{"feature", []string{"chain", "--resume", "--name", "shop", "--feature", "auth"}},
		{"no-feature", []string{"chain", "--resume", "--name", "shop", "--no-feature"}},
		{"mastermind", []string{"chain", "--resume", "--name", "shop", "--mastermind", "someone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeUsage, "")
			if !strings.Contains(ce.message, "--"+tc.name) {
				t.Errorf("message = %q, want it to name --%s", ce.message, tc.name)
			}
		})
	}

	// The settings flags stay accepted: a resume with a settings flag and no
	// start-only flag reaches the store and reports the chain it cannot find.
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", "resumesettings", "--max-corrections", "1", "--reviewer-actor", "reviewer"})
	})
	ce := requireCLIError(t, err, codeBindingNotFound, "")
	if strings.Contains(ce.message, "--max-corrections") || strings.Contains(ce.message, "--reviewer-actor") {
		t.Errorf("message = %q, want the missing chain, not a settings refusal", ce.message)
	}
}

// seedCLIChain writes one chain row and its member bindings into the default
// state root: store-only, so a test drives the verbs with no harness, no git
// and no network.
func seedCLIChain(t *testing.T, name, status string) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	now := time.Now().UTC()

	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: status, Phase: "build", Step: "building",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		AwaitingMember: "builder", AwaitingRound: 1,
		Builder: name, Reviewer: name + "-rev", Planner: name + "-plan",
		CreatedAt: now, UpdatedAt: now,
	}
	err = s.WithLock(func(tx *store.Tx) error {
		for _, m := range []store.Binding{
			{Name: name, CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive},
			{Name: name + "-rev", CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive, Shape: store.ShapeReader},
			{Name: name + "-plan", CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive, Shape: store.ShapeReader},
		} {
			if err := tx.Save(m); err != nil {
				return err
			}
		}
		return tx.ChainPut(c)
	})
	if err != nil {
		t.Fatalf("seed chain %s: %v", name, err)
	}
}

// TestChainStopAndDoneThroughTheCLI pins the two chain arms end to end on a
// store-only fixture: a name that is a chain is stopped (nothing on the member
// is open, so the chain is stopped directly), and a stopped chain is released by
// done, whose document carries chain: true.
func TestChainStopAndDoneThroughTheCLI(t *testing.T) {
	const name = "clichain"
	seedCLIChain(t, name, "running")

	// stop --json: the document is the stop shape with chain carried, and the
	// action names the direct stop.
	stdout, stderr, err := captureOutput(t, func() error { return run([]string{"stop", name, "--json"}) })
	if err != nil {
		t.Fatalf("stop %s --json: %v (stderr: %s)", name, err, stderr)
	}
	var stopDoc StopDoc
	if err := json.Unmarshal(stdout, &stopDoc); err != nil {
		t.Fatalf("decode StopDoc: %v\n%s", err, stdout)
	}
	if !stopDoc.Chain || stopDoc.Name != name {
		t.Errorf("stop document = %+v, want the chain's name with chain carried", stopDoc)
	}
	if stopDoc.Action != relevo.ChainStopActionStopped {
		t.Errorf("stop action = %q, want %q", stopDoc.Action, relevo.ChainStopActionStopped)
	}

	// A second stop has nothing to stop: the chain is not running any more.
	stdout, _, err = captureOutput(t, func() error { return run([]string{"stop", name}) })
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if !strings.Contains(string(stdout), "nothing to stop") {
		t.Errorf("second stop stdout = %q, want the nothing-to-stop answer", stdout)
	}

	// done on the (now stopped) chain releases every member.
	stdout, stderr, err = captureOutput(t, func() error { return run([]string{"done", name, "--json"}) })
	if err != nil {
		t.Fatalf("done %s --json: %v (stderr: %s)", name, err, stderr)
	}
	var doneDoc DoneDoc
	if err := json.Unmarshal(stdout, &doneDoc); err != nil {
		t.Fatalf("decode DoneDoc: %v\n%s", err, stdout)
	}
	if !doneDoc.Chain || doneDoc.Name != name {
		t.Errorf("done document = %+v, want the chain's name with chain carried", doneDoc)
	}

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	c, err := s.Chain(name)
	if err != nil {
		t.Fatalf("Chain %s: %v", name, err)
	}
	if c.Status != "done" {
		t.Errorf("chain status = %q, want done", c.Status)
	}
	for _, member := range []string{name, name + "-rev", name + "-plan"} {
		b, err := s.Load(member)
		if err != nil {
			t.Fatalf("Load %s: %v", member, err)
		}
		if b.State != store.StateDone {
			t.Errorf("member %s state = %q, want done", member, b.State)
		}
	}
}

// TestChainMemberDoneAndUnbindRefusedWhileRunning pins the guards at the CLI
// edge: on a running chain's member, done and unbind are both conflicts naming
// the chain and `relevo stop <n> first`, and unbind on the chain's own name is
// refused through the member that name is. Store-only: seedCLIChain needs no
// harness, no git and no network.
func TestChainMemberDoneAndUnbindRefusedWhileRunning(t *testing.T) {
	const name = "climembers"
	seedCLIChain(t, name, "running")

	for _, tc := range []struct {
		what string
		args []string
	}{
		{"done on a member", []string{"done", name + "-rev"}},
		{"unbind on a member", []string{"unbind", name + "-rev"}},
		{"unbind on the chain", []string{"unbind", name}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeConflict, "")
			if !strings.Contains(ce.message, name) || !strings.Contains(ce.message, "relevo stop "+name+" first") {
				t.Errorf("message = %q, want it to name the chain and `relevo stop %s first`", ce.message, name)
			}
		})
	}
}

// TestChainMemberDoneAndUnbindAllowedAfterTheChainStops pins the other half at
// the CLI edge: a chain that is not running has handed its members back, so done
// and unbind both succeed on one. The seeded member has no worktree and no PID,
// so neither verb runs git or a harness.
func TestChainMemberDoneAndUnbindAllowedAfterTheChainStops(t *testing.T) {
	const name = "clistop"
	seedCLIChain(t, name, "stopped")

	if _, stderr, err := captureOutput(t, func() error { return run([]string{"done", name + "-rev"}) }); err != nil {
		t.Fatalf("done %s-rev = %v (stderr: %s), want it allowed", name, err, stderr)
	}
	if _, stderr, err := captureOutput(t, func() error { return run([]string{"unbind", name + "-rev"}) }); err != nil {
		t.Fatalf("unbind %s-rev = %v (stderr: %s), want it allowed", name, err, stderr)
	}
}

// chainOptionsFrom parses args through chain's own flag set and maps them with
// chainOptions, the way cmdChain's start arm does -- parse and mapping only, no
// runtime.
func chainOptionsFrom(t *testing.T, args ...string) (relevo.ChainOptions, error) {
	t.Helper()
	fs := flag.NewFlagSet("relevo chain", flag.ContinueOnError)
	v := chainFlagSet(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return chainOptions(fs, v)
}

// chainResumeOptionsFrom is chainOptionsFrom's resume twin.
func chainResumeOptionsFrom(t *testing.T, args ...string) (relevo.ResumeOptions, error) {
	t.Helper()
	fs := flag.NewFlagSet("relevo chain", flag.ContinueOnError)
	v := chainFlagSet(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return chainResumeOptions(fs, v)
}

// TestChainFlagSetDefinesGateFlags pins that the three gate flags exist on the
// chain verb's set, so the registry's parity test and the mapping below see
// them.
func TestChainFlagSetDefinesGateFlags(t *testing.T) {
	fs := flag.NewFlagSet("relevo chain", flag.ContinueOnError)
	chainFlagSet(fs)
	for _, name := range []string{"gate", "no-gate", "regate"} {
		if fs.Lookup(name) == nil {
			t.Errorf("chain flag set has no --%s", name)
		}
	}
}

// TestChainGateFlagsParseIntoOptions pins the start arm's mapping: --gate,
// --no-gate and --regate land in ChainOptions.
func TestChainGateFlagsParseIntoOptions(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--feature", "auth",
		"--gate", "make check", "--regate", "2")
	if err != nil {
		t.Fatalf("chainOptions: %v", err)
	}
	if opts.Gate != "make check" {
		t.Errorf("Gate = %q, want make check", opts.Gate)
	}
	if opts.Regate == nil || *opts.Regate != 2 {
		t.Errorf("Regate = %v, want 2", opts.Regate)
	}
	if opts.NoGate {
		t.Error("NoGate = true, want false")
	}

	noGate, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--feature", "auth", "--no-gate")
	if err != nil {
		t.Fatalf("chainOptions --no-gate: %v", err)
	}
	if !noGate.NoGate {
		t.Error("NoGate = false, want true")
	}
}

// TestChainResumeGateFlagsParseIntoOptions pins the resume arm's mapping: the
// same three flags land in ResumeOptions.
func TestChainResumeGateFlagsParseIntoOptions(t *testing.T) {
	opts, err := chainResumeOptionsFrom(t, "--resume", "--name", "shop", "--gate", "make check", "--regate", "3")
	if err != nil {
		t.Fatalf("chainResumeOptions: %v", err)
	}
	if opts.Gate != "make check" {
		t.Errorf("Gate = %q, want make check", opts.Gate)
	}
	if opts.Regate == nil || *opts.Regate != 3 {
		t.Errorf("Regate = %v, want 3", opts.Regate)
	}
	if opts.NoGate {
		t.Error("NoGate = true, want false")
	}

	noGate, err := chainResumeOptionsFrom(t, "--resume", "--name", "shop", "--no-gate")
	if err != nil {
		t.Fatalf("chainResumeOptions --no-gate: %v", err)
	}
	if !noGate.NoGate {
		t.Error("NoGate = false, want true")
	}
}

// TestChainRegateRefusesANegativeValue pins the one refusal --regate makes:
// both arms refuse a value below zero, before any runtime is built.
func TestChainRegateRefusesANegativeValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"start", []string{"chain", "--name", "shop", "--feature", "auth", "--plan", chainPlanArg(t), "--regate", "-1"}},
		{"resume", []string{"chain", "--resume", "--name", "shop", "--regate", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(tc.args) })
			ce := requireCLIError(t, err, codeUsage, "")
			if !strings.Contains(ce.message, "--regate") {
				t.Errorf("message = %q, want it to name --regate", ce.message)
			}
		})
	}
}

// TestChainResumeGateFlagsAreSettings pins that the gate flags are settings on
// the resume arm, not start-only flags: a resume naming --gate reaches the
// store and reports the chain it cannot find.
func TestChainResumeGateFlagsAreSettings(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", "missing", "--gate", "make check"})
	})
	ce := requireCLIError(t, err, codeBindingNotFound, "")
	if strings.Contains(ce.message, "--gate") {
		t.Errorf("message = %q, want the missing chain, not a settings refusal", ce.message)
	}
}

// TestChainDocCarriesTheCheck pins the document's check field: the resolved
// command, or "none" when the builder ran no check.
func TestChainDocCarriesTheCheck(t *testing.T) {
	if got := chainDocOf(relevo.ChainResult{Check: "make check"}).Check; got != "make check" {
		t.Errorf("Check = %q, want make check", got)
	}
	if got := chainDocOf(relevo.ChainResult{}).Check; got != "none" {
		t.Errorf("Check = %q, want none", got)
	}
}

// TestChainStartedTextPrintsTheCheck pins the human line: the resolved command,
// or "none".
func TestChainStartedTextPrintsTheCheck(t *testing.T) {
	for _, tc := range []struct {
		check string
		want  string
	}{
		{"make check", "  check: make check\n"},
		{"", "  check: none\n"},
	} {
		stdout, _, err := captureOutput(t, func() error {
			chainStartedText(relevo.Runtime{}, relevo.ChainResult{Check: tc.check})
			return nil
		})
		if err != nil {
			t.Fatalf("chainStartedText: %v", err)
		}
		if !strings.Contains(string(stdout), tc.want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, tc.want)
		}
	}
}

// TestChainDocCarriesPlacement pins the document's placement field: a remote
// member names its server, every other member runs here.
func TestChainDocCarriesPlacement(t *testing.T) {
	res := relevo.ChainResult{
		Chain: db.ChainRow{Builder: "shop", Reviewer: "review", Planner: "plan"},
		Members: []store.Binding{
			{Name: "shop", Builder: store.Endpoint{Server: "zen"}},
			{Name: "review", Role: "reviewer"},
			{Name: "plan", Role: "planner"},
		},
	}
	doc := chainDocOf(res)
	if len(doc.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(doc.Members))
	}
	for i, want := range []string{"zen", "local", "local"} {
		if got := doc.Members[i].Placement; got != want {
			t.Errorf("members[%d].Placement = %q, want %q", i, got, want)
		}
	}
}

// TestChainStartedTextNamesTheRemoteBuilder pins the remote human text: the
// builder names its server, branch and base with no worktree line, and a local
// builder keeps the worktree line.
func TestChainStartedTextNamesTheRemoteBuilder(t *testing.T) {
	remote := relevo.ChainResult{
		Chain: db.ChainRow{Builder: "shop", Reviewer: "review", Branch: "relevo/shop", Base: "abc123"},
		Members: []store.Binding{
			{Name: "shop", Builder: store.Endpoint{Server: "zen"}},
			{Name: "review", Role: "reviewer"},
		},
	}
	stdout, _, err := captureOutput(t, func() error {
		chainStartedText(relevo.Runtime{}, remote)
		return nil
	})
	if err != nil {
		t.Fatalf("chainStartedText: %v", err)
	}
	out := string(stdout)
	for _, want := range []string{"on zen", "branch relevo/shop", "from abc123"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "worktree") {
		t.Errorf("stdout = %q, want no worktree line for a remote builder", out)
	}

	local := relevo.ChainResult{
		Chain:   db.ChainRow{Builder: "shop", Worktree: "/work/shop", Branch: "relevo/shop", Base: "abc123"},
		Members: []store.Binding{{Name: "shop"}},
	}
	stdout, _, err = captureOutput(t, func() error {
		chainStartedText(relevo.Runtime{}, local)
		return nil
	})
	if err != nil {
		t.Fatalf("chainStartedText: %v", err)
	}
	if want := "  worktree /work/shop on relevo/shop (from abc123)\n"; !strings.Contains(string(stdout), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}

// TestChainResumedTextNamesPlacement pins the resume human text: each member
// names where it runs, so the remote builder names its server.
func TestChainResumedTextNamesPlacement(t *testing.T) {
	res := relevo.ChainResult{
		Chain: db.ChainRow{Name: "shop", Builder: "shop", Reviewer: "review"},
		Members: []store.Binding{
			{Name: "shop", Builder: store.Endpoint{Server: "zen"}},
			{Name: "review", Role: "reviewer"},
		},
	}
	stdout, _, err := captureOutput(t, func() error {
		chainResumedText(res)
		return nil
	})
	if err != nil {
		t.Fatalf("chainResumedText: %v", err)
	}
	out := string(stdout)
	if !strings.Contains(out, "zen") {
		t.Errorf("stdout = %q, want it to name the remote builder's server zen", out)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("stdout = %q, want the local reader's placement", out)
	}
}

// TestChainServerFlag pins --server at the CLI edge: a start carries it into
// ChainOptions, and a resume refuses it because the chain already names its
// server. Parse-only: no state is opened and no harness needs to run.
func TestChainServerFlag(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--feature", "auth", "--server", "zen")
	if err != nil {
		t.Fatalf("chainOptions --server: %v", err)
	}
	if opts.Server != "zen" {
		t.Errorf("Server = %q, want zen", opts.Server)
	}

	_, _, err = captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", "shop", "--server", "zen"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "--server") {
		t.Errorf("message = %q, want it to name --server", ce.message)
	}
}

// TestChainDoneOnARunningChainIsAConflict pins the refusal's class: a script
// must be able to tell "stop it first" from an internal failure.
func TestChainDoneOnARunningChainIsAConflict(t *testing.T) {
	const name = "clirunning"
	seedCLIChain(t, name, "running")

	_, _, err := captureOutput(t, func() error { return run([]string{"done", name}) })
	ce := requireCLIError(t, err, codeConflict, "")
	if !strings.Contains(ce.message, "relevo stop "+name+" first") {
		t.Errorf("message = %q, want it to name `relevo stop %s first`", ce.message, name)
	}
}

// TestChainResumeOnASettledChainIsAConflict pins both settle refusals at the
// CLI edge: a chain still in flight and a finished chain are conflicts, not
// internal failures.
func TestChainResumeOnASettledChainIsAConflict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   string
	}{
		{"running", "running", "is running"},
		{"done", "done", "is done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "cli" + tc.name + "resume"
			seedCLIChain(t, name, tc.status)

			_, _, err := captureOutput(t, func() error {
				return run([]string{"chain", "--resume", "--name", name})
			})
			ce := requireCLIError(t, err, codeConflict, "")
			if !strings.Contains(ce.message, tc.want) {
				t.Errorf("message = %q, want it to say the chain %s", ce.message, tc.want)
			}
		})
	}
}

// TestSeedOverCapIsAUsageRefusal pins the class of the planner seed cap: the
// refusal is a usage error naming the escape, not an internal failure. Pure: it
// classifies an error and touches no state.
func TestSeedOverCapIsAUsageRefusal(t *testing.T) {
	err := writeError(fmt.Errorf("binding \"planner\": the seed is 4097 bytes: %w", relevo.ErrSeedOverCap))
	ce := requireCLIError(t, err, codeUsage, "trim the seed or pass --force")
	if !strings.Contains(ce.message, "4097") {
		t.Errorf("message = %q, want the wrapping error's text", ce.message)
	}
}

// seedCLIOpenMemberChain writes a halted chain whose builder member's round is
// still open: a prompt log entry with no report. Store-only, so a resume of it
// reaches the round-open refusal with no daemon, no harness and no network; the
// refusal precedes startRound, so nothing spawns.
func seedCLIOpenMemberChain(t *testing.T, name string) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	now := time.Now().UTC()
	plan := filepath.Join(t.TempDir(), "plan-1.md")
	if err := os.WriteFile(plan, []byte("build it"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	c := db.ChainRow{
		ID: db.NewID(), Name: name, Status: "halted", Phase: "build", Step: "building",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`[` + strconv.Quote(plan) + `]`), SettingsJSON: []byte(`{}`),
		AwaitingMember: "builder", AwaitingRound: 1,
		Builder: name, Reviewer: name + "-rev", Planner: name + "-plan",
		CreatedAt: now, UpdatedAt: now,
	}
	err = s.WithLock(func(tx *store.Tx) error {
		for _, m := range []store.Binding{
			{Name: name, CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive},
			{Name: name + "-rev", CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive, Shape: store.ShapeReader},
			{Name: name + "-plan", CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive, Shape: store.ShapeReader},
		} {
			if err := tx.Save(m); err != nil {
				return err
			}
		}
		if err := tx.ChainPut(c); err != nil {
			return err
		}
		// The builder's round 1 is open: its prompt entry exists, no report.
		return tx.AppendLog(name, store.LogEntry{
			TS: now, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
			Path: plan, Confirmed: true,
		})
	})
	if err != nil {
		t.Fatalf("seed open chain %s: %v", name, err)
	}
}

// TestChainResumeOpenMemberRoundIsAConflict pins the class of a resume that
// finds its member's round still open: a conflict naming `relevo stop <member>`
// as the next command, not an internal failure.
func TestChainResumeOpenMemberRoundIsAConflict(t *testing.T) {
	const name = "cliresumeopen"
	seedCLIOpenMemberChain(t, name)

	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", name})
	})
	ce := requireCLIError(t, err, codeConflict, "relevo stop "+name)
	if !strings.Contains(ce.message, "still open") {
		t.Errorf("message = %q, want the round-open refusal", ce.message)
	}
}

// TestChainResumeOpenRoundIsAConflict pins item 1 at the CLI edge: a resume
// whose target member's round is open is a conflict whose next command is the
// stop that ends it, not an internal failure. Store-only: the refusal precedes
// the send, so no daemon starts, no harness is spawned and no network is
// reached.
func TestChainResumeOpenRoundIsAConflict(t *testing.T) {
	const name = "cliresumeopen"
	seedCLIChain(t, name, "stopped")

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.AppendLog(name, store.LogEntry{
		TS: time.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
	}); err != nil {
		t.Fatalf("open the builder's round: %v", err)
	}

	_, _, err = captureOutput(t, func() error { return run([]string{"chain", "--resume", "--name", name}) })
	ce := requireCLIError(t, err, codeConflict, "relevo stop "+name)
	if !strings.Contains(ce.message, "round 1 is still open") {
		t.Errorf("message = %q, want it to say the round is still open", ce.message)
	}
}

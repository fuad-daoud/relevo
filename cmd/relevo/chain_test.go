package main

import (
	"encoding/json"
	"flag"
	"path/filepath"
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

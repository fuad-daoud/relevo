package main

import (
	"flag"
	"strings"
	"testing"
)

// The tests here exercise flag parsing only, as CLAUDE.md's CLI rule demands:
// every refusal fires in chainOptions or chainResumeOptions, before a runtime
// is built, so nothing touches a state directory and no harness runs.

// TestChainFlagsServerRefusesWorkflow pins that --server refuses the flags the
// server cannot carry yet, naming its workflow feature in the refusal.
func TestChainFlagsServerRefusesWorkflow(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--server", "zen", "--workflow", "custom"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "workflow") {
		t.Errorf("message = %q, want the server's workflow feature", ce.message)
	}
}

// TestChainFlagsTaskExclusive pins that --task and --task-file are refused
// together, before either is read.
func TestChainFlagsTaskExclusive(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--task", "build it", "--task-file", "task.md"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "--task") {
		t.Errorf("message = %q, want it to name --task", ce.message)
	}
}

// TestChainFlagsFromNeedsResume pins that --from is refused on a start, naming
// the --resume it belongs to.
func TestChainFlagsFromNeedsResume(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--from", "review"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "--resume") {
		t.Errorf("message = %q, want it to name --resume", ce.message)
	}
}

// TestChainFlagsCustomWorkflowStarts pins that a workflow other than the
// shipped default now parses into a start: the engine can drive it, so no flag
// combination refuses it before a runtime is built.
func TestChainFlagsCustomWorkflowStarts(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--plan", "plan.md", "--no-feature", "--workflow", "custom")
	if err != nil {
		t.Fatalf("chainOptions(--workflow custom): %v", err)
	}
	if opts.Workflow != "custom" {
		t.Errorf("Workflow = %q, want custom", opts.Workflow)
	}
}

// TestChainFlagsParamsParseIntoOptions pins that a dry run carries --param and
// --task into the start options without starting anything.
func TestChainFlagsParamsParseIntoOptions(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--dry-run", "--param", "reviewer=assistant", "--task", "build it")
	if err != nil {
		t.Fatalf("chainOptions: %v", err)
	}
	if !opts.DryRun {
		t.Error("DryRun = false, want true")
	}
	if opts.Params["reviewer"] != "assistant" {
		t.Errorf("Params[reviewer] = %q, want assistant", opts.Params["reviewer"])
	}
	if opts.Task != "build it" {
		t.Errorf("Task = %q, want build it", opts.Task)
	}

	if _, err := chainOptionsFrom(t, "--name", "shop", "--param", "reviewer"); err == nil {
		t.Error("chainOptions(--param reviewer) = nil error, want a usage refusal")
	}
}

// TestChainFlagsBuilderActorParsesIntoOptions pins the start arm's mapping:
// --builder-actor lands in ChainOptions.BuilderActor, an unnamed builder stays
// empty for the repository entry, and the flag's help names its default the
// way the reviewer and planner actors' do.
func TestChainFlagsBuilderActorParsesIntoOptions(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--no-feature", "--builder-actor", "custom-builder")
	if err != nil {
		t.Fatalf("chainOptions --builder-actor: %v", err)
	}
	if opts.BuilderActor != "custom-builder" {
		t.Errorf("BuilderActor = %q, want custom-builder", opts.BuilderActor)
	}

	unnamed, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--no-feature")
	if err != nil {
		t.Fatalf("chainOptions without --builder-actor: %v", err)
	}
	if unnamed.BuilderActor != "" {
		t.Errorf("BuilderActor = %q, want empty so the repository entry stands", unnamed.BuilderActor)
	}

	fs := flag.NewFlagSet("relevo chain", flag.ContinueOnError)
	chainFlagSet(fs)
	if got := fs.Lookup("builder-actor").Usage; !strings.Contains(got, "(default:") {
		t.Errorf("usage = %q, want it to name a default like the other actor flags", got)
	}
}

// TestChainFlagsBuilderActorRefusedOnResume pins that --builder-actor is a
// start-only flag: a resume naming it is refused before any runtime is built,
// so a resumed chain keeps the builder it started with.
func TestChainFlagsBuilderActorRefusedOnResume(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--resume", "--name", "shop", "--builder-actor", "custom-builder"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "--builder-actor") {
		t.Errorf("message = %q, want it to name --builder-actor", ce.message)
	}
}

// TestChainFlagsCandidateParsesIntoOptions pins the start arm's mapping: each
// --candidate part=value lands in ChainOptions.Candidates keyed by part, and
// a bad entry is a usage error before any runtime is built.
func TestChainFlagsCandidateParsesIntoOptions(t *testing.T) {
	opts, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--no-feature",
		"--candidate", "builder=opencode/test/m", "--candidate", "reviewer=claude/test/m")
	if err != nil {
		t.Fatalf("chainOptions --candidate: %v", err)
	}
	if opts.Candidates["builder"] != "opencode/test/m" || opts.Candidates["reviewer"] != "claude/test/m" {
		t.Errorf("Candidates = %v, want builder and reviewer pins", opts.Candidates)
	}

	unnamed, err := chainOptionsFrom(t, "--name", "shop", "--plan", chainPlanArg(t), "--no-feature")
	if err != nil {
		t.Fatalf("chainOptions without --candidate: %v", err)
	}
	if len(unnamed.Candidates) != 0 {
		t.Errorf("Candidates = %v, want none so every member takes the policy pick", unnamed.Candidates)
	}

	for _, args := range [][]string{
		{"--candidate", "writer=opencode/test/m"},
		{"--candidate", "builder=opencode/test/m", "--candidate", "builder=claude/test/m"},
		{"--candidate", "builder="},
	} {
		full := append([]string{"--name", "shop", "--plan", chainPlanArg(t), "--no-feature"}, args...)
		_, err := chainOptionsFrom(t, full...)
		if err == nil {
			t.Errorf("chainOptions(%v) = nil, want a usage refusal", args)
			continue
		}
		_ = requireCLIError(t, err, codeUsage, "")
	}
}

// TestChainFlagsCandidateAcceptedOnResume pins the resume arm's mapping: each
// --candidate part=value lands in ResumeOptions.Candidates keyed by part, so a
// resume re-pins that member, and a resume without the flag keeps the pins its
// start stored.
func TestChainFlagsCandidateAcceptedOnResume(t *testing.T) {
	opts, err := chainResumeOptionsFrom(t, "--resume", "--name", "shop",
		"--candidate", "reviewer=claude/test/m")
	if err != nil {
		t.Fatalf("chainResumeOptions --candidate: %v", err)
	}
	if opts.Candidates["reviewer"] != "claude/test/m" {
		t.Errorf("Candidates = %v, want the reviewer pin", opts.Candidates)
	}

	unnamed, err := chainResumeOptionsFrom(t, "--resume", "--name", "shop")
	if err != nil {
		t.Fatalf("chainResumeOptions without --candidate: %v", err)
	}
	if len(unnamed.Candidates) != 0 {
		t.Errorf("Candidates = %v, want none so the resume keeps the stored pins", unnamed.Candidates)
	}
}

// TestChainFlagsCandidateRefusedOnResume pins the resume arm's refusal: an
// unknown part is refused before any runtime is built, naming the parts a pin
// can take.
func TestChainFlagsCandidateRefusedOnResume(t *testing.T) {
	_, err := chainResumeOptionsFrom(t, "--resume", "--name", "shop", "--candidate", "writer=opencode/test/m")
	if err == nil {
		t.Fatal("chainResumeOptions(--candidate writer=...) = nil, want a usage refusal")
	}
	ce := requireCLIError(t, err, codeUsage, "")
	for _, want := range []string{`"writer"`, "builder", "reviewer", "planner", "security"} {
		if !strings.Contains(ce.message, want) {
			t.Errorf("message = %q, want it to name %q", ce.message, want)
		}
	}
}

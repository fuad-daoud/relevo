package main

import (
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

// TestChainFlagsCustomWorkflowNeedsDryRun pins that a workflow other than the
// shipped default is refused without --dry-run, so a start never runs a
// workflow the engine cannot yet drive.
func TestChainFlagsCustomWorkflowNeedsDryRun(t *testing.T) {
	_, _, err := captureOutput(t, func() error {
		return run([]string{"chain", "--name", "shop", "--workflow", "custom"})
	})
	ce := requireCLIError(t, err, codeUsage, "")
	if !strings.Contains(ce.message, "--dry-run") {
		t.Errorf("message = %q, want it to name --dry-run", ce.message)
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

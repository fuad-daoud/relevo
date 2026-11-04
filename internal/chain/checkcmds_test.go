package chain

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestCheckCommandsListsDistinctRenderedCommands pins the collection: every
// non-empty check step renders once, in sorted step order, with params
// expanded and duplicates dropped.
func TestCheckCommandsListsDistinctRenderedCommands(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{
		Params: map[string]workflow.Param{
			"gate": {Kind: workflow.ParamString, Str: "make check"},
		},
		Steps: map[string]workflow.Step{
			"b-check": {Check: "go test ./..."},
			"a-check": {Check: "{{params.gate}}"},
			"dup":     {Check: "go test ./..."},
			"empty":   {Check: ""},
			"run":     {Run: "echo hi"},
		},
	}
	got := CheckCommands(def)
	want := []string{"make check", "go test ./..."}
	if len(got) != len(want) {
		t.Fatalf("CheckCommands = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CheckCommands = %q, want %q", got, want)
		}
	}
}

// TestCheckCommandsEmptyWhenNoCheckSteps pins the empty workflow: no check
// steps means no commands.
func TestCheckCommandsEmptyWhenNoCheckSteps(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{
		Steps: map[string]workflow.Step{
			"run": {Run: "echo hi"},
		},
	}
	if got := CheckCommands(def); len(got) != 0 {
		t.Errorf("CheckCommands = %q, want empty", got)
	}
	if got := SingleCheckCommand(def); got != "" {
		t.Errorf("SingleCheckCommand = %q, want empty", got)
	}
	if cmd, source := CheckReport(def, resolve.GateResolution{}, nil); cmd != "" || source != "" {
		t.Errorf("CheckReport = %q (%q), want empty", cmd, source)
	}
}

// TestSingleCheckCommandNeedsExactlyOne pins the gate: one distinct command
// passes through, zero or two yield none.
func TestSingleCheckCommandNeedsExactlyOne(t *testing.T) {
	t.Parallel()

	single := workflow.Definition{
		Steps: map[string]workflow.Step{"a": {Check: "make check"}},
	}
	if got := SingleCheckCommand(single); got != "make check" {
		t.Errorf("SingleCheckCommand = %q, want %q", got, "make check")
	}
	multi := workflow.Definition{
		Steps: map[string]workflow.Step{
			"a": {Check: "make check"},
			"b": {Check: "go test ./..."},
		},
	}
	if got := SingleCheckCommand(multi); got != "" {
		t.Errorf("SingleCheckCommand with two commands = %q, want empty", got)
	}
}

// TestCheckReportNamesTheCheckSource pins the start line's check: a gate
// given through --param is named as such, a resolved gate keeps its source,
// several commands join under the workflow, and an unmatched single command
// falls back to the workflow.
func TestCheckReportNamesTheCheckSource(t *testing.T) {
	t.Parallel()

	withGate := func(gate string) workflow.Definition {
		return workflow.Definition{
			Params: map[string]workflow.Param{
				"gate": {Kind: workflow.ParamString, Str: gate},
			},
			Steps: map[string]workflow.Step{
				"check": {Check: "{{params.gate}}"},
			},
		}
	}
	multi := workflow.Definition{
		Steps: map[string]workflow.Step{
			"a": {Check: "make check"},
			"b": {Check: "go test ./..."},
		},
	}
	cases := []struct {
		name        string
		def         workflow.Definition
		res         resolve.GateResolution
		params      map[string]string
		cmd, source string
	}{
		{"--param gate", withGate("go test ./..."), resolve.GateResolution{}, map[string]string{"gate": "go test ./..."}, "go test ./...", "--param gate"},
		{"policy default", withGate("make check"), resolve.GateResolution{Command: "make check", Source: resolve.GateSourcePolicy}, nil, "make check", resolve.GateSourcePolicy},
		{"several commands join", multi, resolve.GateResolution{}, nil, "make check; go test ./...", "workflow"},
		{"unmatched single falls back", withGate("make check"), resolve.GateResolution{Command: "other", Source: resolve.GateSourcePolicy}, nil, "make check", "workflow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cmd, source := CheckReport(c.def, c.res, c.params)
			if cmd != c.cmd || source != c.source {
				t.Errorf("CheckReport = %q (%q), want %q (%q)", cmd, source, c.cmd, c.source)
			}
		})
	}
}

// TestCleanupCommandsListsDistinctRenderedCommands pins the collection: every
// non-empty cleanup step renders once, in sorted step order, with params
// expanded and duplicates dropped.
func TestCleanupCommandsListsDistinctRenderedCommands(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{
		Params: map[string]workflow.Param{
			"cleanup_round": {Kind: workflow.ParamString, Str: "make clean"},
		},
		Steps: map[string]workflow.Step{
			"b-cleanup": {Cleanup: "go clean ./..."},
			"a-cleanup": {Cleanup: "{{params.cleanup_round}}"},
			"dup":       {Cleanup: "go clean ./..."},
			"empty":     {Cleanup: ""},
			"run":       {Run: "echo hi"},
		},
	}
	got := CleanupCommands(def)
	want := []string{"make clean", "go clean ./..."}
	if len(got) != len(want) {
		t.Fatalf("CleanupCommands = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CleanupCommands = %q, want %q", got, want)
		}
	}
}

// TestCleanupReportNamesTheCleanupSource pins the start line's cleanup: a
// cleanup given through --param is named as such, a resolved cleanup keeps
// its source and when word, several commands join under the workflow, and an
// unmatched single command falls back to the workflow.
func TestCleanupReportNamesTheCleanupSource(t *testing.T) {
	t.Parallel()

	withCleanup := func(round, chain string) workflow.Definition {
		return workflow.Definition{
			Params: map[string]workflow.Param{
				"cleanup_round": {Kind: workflow.ParamString, Str: round},
				"cleanup_chain": {Kind: workflow.ParamString, Str: chain},
			},
			Steps: map[string]workflow.Step{
				"cleanup-round": {Cleanup: "{{params.cleanup_round}}"},
				"cleanup-chain": {Cleanup: "{{params.cleanup_chain}}"},
			},
		}
	}
	cases := []struct {
		name                          string
		def                           workflow.Definition
		cmd, source, after            string
		params                        map[string]string
		wantCmd, wantSource, wantWhen string
	}{
		{"--param cleanup_round", withCleanup("go clean ./...", ""), "", "", "",
			map[string]string{"cleanup_round": "go clean ./..."}, "go clean ./...", "--param cleanup_round", "round"},
		{"flag round", withCleanup("make clean", ""), "make clean", "--cleanup", "round",
			nil, "make clean", "--cleanup", "round"},
		{"repo chain", withCleanup("", "make clean"), "make clean", "repo setting", "chain",
			nil, "make clean", "repo setting", "chain"},
		{"both", withCleanup("make clean", "make clean"), "make clean", "--cleanup", "both",
			nil, "make clean", "--cleanup", "both"},
		{"several commands join", withCleanup("make clean", "go clean ./..."), "", "", "both",
			nil, "go clean ./...; make clean", "workflow", "both"},
		{"unmatched single falls back", withCleanup("make clean", ""), "other", "--cleanup", "round",
			nil, "make clean", "workflow", "round"},
		{"literal keeps the after word", workflow.Definition{
			Steps: map[string]workflow.Step{
				"sweep": {Cleanup: "make clean", On: map[string]workflow.Target{"done": workflow.DoneTarget()}},
			},
		}, "", "", "chain", nil, "make clean", "workflow", "chain"},
		{"no cleanup", withCleanup("", ""), "", "", "chain",
			nil, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cmd, source, when := CleanupReport(c.def, c.cmd, c.source, c.after, c.params)
			if cmd != c.wantCmd || source != c.wantSource || when != c.wantWhen {
				t.Errorf("CleanupReport = %q (%q) [%q], want %q (%q) [%q]",
					cmd, source, when, c.wantCmd, c.wantSource, c.wantWhen)
			}
		})
	}
}

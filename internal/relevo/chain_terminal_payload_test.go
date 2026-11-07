package relevo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// wfPayloadOfRow renders the end delivery of a stored row and state exactly
// as the terminal caller does, so direct payload tests pin the delivered
// text: a row without a stored workflow reads the shipped one, since only the
// pinned substrings matter there.
func wfPayloadOfRow(t *testing.T, c db.ChainRow, st workflow.State) string {
	t.Helper()
	def := workflow.Default()
	if len(c.WorkflowJSON) > 0 {
		var err error
		def, err = chain.WorkflowDef(c)
		if err != nil {
			t.Fatalf("chain.WorkflowDef: %v", err)
		}
	}
	return chainTerminalWFPayload(c, def, st)
}

// TestHaltPayloadNamesBothWaysOut pins the halt payload's two hints: a resume
// once the cause is fixed, and `relevo done` for a reader who has given up on
// the work. The mutation it kills is dropping the done clause, which leaves a
// payload naming only the resume.
func TestHaltPayloadNamesBothWaysOut(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		build func() string
	}{
		{"workflow", func() string {
			c := db.ChainRow{Name: "shop", Branch: "relevo/shop"}
			st := workflow.State{Status: workflow.StatusHalted, At: "repair"}
			return wfPayloadOfRow(t, c, st)
		}},
		{"legacy", func() string {
			return chainTerminalPayload(
				db.ChainRow{Name: "shop", Branch: "relevo/shop", Status: string(chain.StatusHalted)}, 0)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.build()
			for _, want := range []string{
				"relevo chain --resume --name shop",
				"`relevo done shop` closes it",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("halt payload %q does not carry %q", got, want)
				}
			}
		})
	}
}

// TestEndPayloadWithoutAHaltNamesNeither pins the other side of the same rule:
// a chain that finished names neither command, because there is nothing to
// resume and nothing to close.
func TestEndPayloadWithoutAHaltNamesNeither(t *testing.T) {
	t.Parallel()

	c := db.ChainRow{Name: "shop", Branch: "relevo/shop"}
	st := workflow.State{Status: workflow.StatusDone, At: "review"}
	got := wfPayloadOfRow(t, c, st)
	for _, unwanted := range []string{"--resume", "relevo done"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("a finished chain's payload %q carries %q", got, unwanted)
		}
	}
}

// TestStoppedPayloadNamesNoResume pins the stopped fallback: a chain stopped
// on a repair arm still names no --from pair, since stopped keeps the
// trace-only ending.
func TestStoppedPayloadNamesNoResume(t *testing.T) {
	t.Parallel()

	c := db.ChainRow{Name: "shop", Branch: "relevo/shop"}
	st := workflow.State{Status: workflow.StatusStopped, At: "repair"}
	got := wfPayloadOfRow(t, c, st)
	for _, unwanted := range []string{"--resume", "--from", "relevo done"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("a stopped chain's payload %q carries %q", got, unwanted)
		}
	}
}

// TestLegacyPayloadRepairArmNamesBothFroms pins the legacy site: a halted
// mirror row whose stored state stands on a repair arm one check feeds names
// that check's --from and the arm's, with the same wording as the workflow
// site.
func TestLegacyPayloadRepairArmNamesBothFroms(t *testing.T) {
	t.Parallel()

	def, err := json.Marshal(workflow.Default())
	if err != nil {
		t.Fatal(err)
	}
	st, err := json.Marshal(workflow.State{Status: workflow.StatusHalted, At: "repair"})
	if err != nil {
		t.Fatal(err)
	}
	c := db.ChainRow{Name: "shop", Branch: "relevo/shop",
		Status: string(chain.StatusHalted), WorkflowJSON: def, StateJSON: st}
	got := chainTerminalPayload(c, 0)
	for _, want := range []string{
		"relevo chain --resume --name shop --from check",
		"--from repair",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("legacy payload %q does not carry %q", got, want)
		}
	}
}

// TestLegacyPayloadOffRepairArmKeepsTodayText pins the legacy fallbacks: a
// halted row without a stored workflow, one standing where no single check
// feeds, and a stopped row keep today's ending, never a --from pair.
func TestLegacyPayloadOffRepairArmKeepsTodayText(t *testing.T) {
	t.Parallel()

	mustRaw := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode test workflow: %v", err)
		}
		return raw
	}
	def := mustRaw(workflow.Default())
	two, err := workflow.Parse([]byte(`name: twogates
inputs: { plans: optional }
params: { gate: make check, budget: 0 }
start: build
steps:
  build: { run: builder, seed: "build it", on: { done: gate-a } }
  gate-a: { check: "{{params.gate}}", on: { green: gate-b, red: arm } }
  gate-b: { check: "{{params.gate}}", on: { green: done, red: arm } }
  arm: { run: builder, seed: "fix it",
         budget: { max: "{{params.budget}}", per: build, then: gate-a },
         on: { done: gate-a } }
`))
	if err != nil {
		t.Fatalf("parse two-check workflow: %v", err)
	}
	storedRow := func(rawDef json.RawMessage, rowStatus chain.Status, stateStatus workflow.Status, at string) db.ChainRow {
		return db.ChainRow{Name: "shop", Branch: "relevo/shop",
			Status: string(rowStatus), WorkflowJSON: rawDef,
			StateJSON: mustRaw(workflow.State{Status: stateStatus, At: at})}
	}

	for _, tt := range []struct {
		name string
		row  db.ChainRow
		// resumeToday and fromPair name which halted ending the row keeps.
		resumeToday bool
		fromPair    bool
	}{
		{"no stored workflow",
			db.ChainRow{Name: "shop", Branch: "relevo/shop", Status: string(chain.StatusHalted)},
			true, false},
		{"stored workflow without state",
			db.ChainRow{Name: "shop", Branch: "relevo/shop", Status: string(chain.StatusHalted), WorkflowJSON: def},
			true, false},
		{"standing no check feeds",
			storedRow(def, chain.StatusHalted, workflow.StatusHalted, "review"),
			true, false},
		{"two feeding checks",
			storedRow(mustRaw(two), chain.StatusHalted, workflow.StatusHalted, "arm"),
			true, false},
		{"stopped on the arm",
			storedRow(def, chain.StatusStopped, workflow.StatusStopped, "repair"),
			false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := chainTerminalPayload(tt.row, 0)
			if tt.resumeToday && !strings.Contains(got, "relevo chain --resume --name shop resumes it") {
				t.Errorf("legacy payload %q does not carry today's resume", got)
			}
			if !tt.resumeToday && strings.Contains(got, "--resume") {
				t.Errorf("legacy payload %q names a resume", got)
			}
			if from := strings.Contains(got, "--from"); from != tt.fromPair {
				t.Errorf("legacy payload %q names --from = %v, want %v", got, from, tt.fromPair)
			}
		})
	}
}

// TestHaltPayloadOnRedGateNamesCheckAndOverride pins the red-gate ending: a
// halt on a pass a red check gated names --from the check with its real step
// name and the override the halt reason asks for, never the arm pair.
func TestHaltPayloadOnRedGateNamesCheckAndOverride(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtPassWhileRedReview(t, rt)
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := wfPayloadOfRow(t, c, st)
	for _, want := range []string{
		"relevo chain --resume --name shop --from check",
		"override",
		"`relevo done shop` closes it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("payload %q does not carry %q", got, want)
		}
	}
	if strings.Contains(got, "--from review") {
		t.Errorf("payload %q names the halted step as a repair arm", got)
	}
}

// TestHaltPayloadOnChangesRequestedKeepsToday pins the halt-kind rule in the
// delivery: a correction-budget halt with a red check upstream names today's
// plain resume, never a --from.
func TestHaltPayloadOnChangesRequestedKeepsToday(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	haltAtChangesRequestedWithRedCheck(t, rt)
	c := flowChainRow(t, rt)
	st, err := chain.WorkflowState(c)
	if err != nil {
		t.Fatalf("chain.WorkflowState: %v", err)
	}
	got := wfPayloadOfRow(t, c, st)
	if !strings.Contains(got, "relevo chain --resume --name shop resumes it") {
		t.Errorf("payload %q does not carry today's resume", got)
	}
	if strings.Contains(got, "--from") {
		t.Errorf("payload %q names --from on a correction-budget halt", got)
	}
}

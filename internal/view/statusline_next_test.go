package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

func nextOfFixture(b BindingStatus) (StatusLineRow, string) {
	row := StatusLineRows(Report{Bindings: []BindingStatus{b}}, baseTime)[0]
	data, _ := json.Marshal(row)
	return row, string(data)
}

func TestStatusLineNextByTone(t *testing.T) {
	t.Parallel()
	report := &LastEvent{Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport}
	question := &LastEvent{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindQuestion}
	for _, tt := range []struct {
		name      string
		b         BindingStatus
		wantLabel string
		wantText  string
	}{
		{"question", BindingStatus{Name: "x", Round: 2, Display: "ACTIVE", LastPayload: question, Question: "Proceed?"},
			"answer", `Answer x r2's question "Proceed?": `},
		{"report", BindingStatus{Name: "x", Round: 4, Display: "ACTIVE", LastPayload: report, PromptPath: "/s/003-prompt.md"},
			"make check, compare the diff",
			"Verify x r3: run make check, then compare relevo show x --round 3 --diff against /s/003-prompt.md"},
		{"reader report", BindingStatus{Name: "p", Round: 2, Display: "ACTIVE", Shape: store.ShapeReader, LastPayload: &LastEvent{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport}},
			"review the output", "Review p r2's output: relevo show p --round 2 --output"},
		{"halt", BindingStatus{Name: "x", Round: 2, Display: "NEEDS YOU",
			Waiting: &Waiting{Cause: "halted", Line: "boom", Hint: "relevo status --name x"}},
			"resolve the halt", "Resolve x r2 (halted: boom): run relevo status --name x"},
		{"chain halt", BindingStatus{Name: "c", Display: "NEEDS YOU", Detail: "step failed", Chain: &ChainFacts{}},
			"resolve the halt", "Resolve c: step failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row, doc := nextOfFixture(tt.b)
			if row.Next == nil || row.Next.Label != tt.wantLabel || row.Next.Text != tt.wantText {
				t.Fatalf("Next = %+v, want %q / %q", row.Next, tt.wantLabel, tt.wantText)
			}
			if !strings.Contains(doc, `"next":{"label":`) {
				t.Errorf("json lacks next: %s", doc)
			}
		})
	}
}

func TestStatusLineNextAbsentOffTheMasterMind(t *testing.T) {
	t.Parallel()
	open := baseTime.Add(-time.Minute)
	for _, tt := range []struct {
		name string
		b    BindingStatus
	}{
		{"running", BindingStatus{Name: "x", Round: 1, Display: "ACTIVE", BuilderStatus: "working", RoundStart: open}},
		{"paused", BindingStatus{Name: "x", Round: 1, Display: "PAUSED"}},
		{"done", BindingStatus{Name: "x", Round: 1, Display: "DONE"}},
		{"chain active", BindingStatus{Name: "c", Display: "ACTIVE", Chain: &ChainFacts{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row, doc := nextOfFixture(tt.b)
			if row.Next != nil || strings.Contains(doc, `"next"`) {
				t.Fatalf("Next = %+v in %s", row.Next, doc)
			}
		})
	}
}

func TestStatusLineGatesProjection(t *testing.T) {
	t.Parallel()
	until := baseTime.Add(time.Hour)
	got := StatusLineGates([]availability.Gate{
		{Token: "claude/test/m", Kind: availability.RateLimited, Note: "limit", Until: until},
		{Token: "opencode/test/m", Kind: availability.SpawnFailed},
	})
	if len(got) != 2 || got[0].Provider != "test" || got[0].Reason != "limit" || got[0].Until != until.Format(time.RFC3339) {
		t.Fatalf("gates[0] = %+v", got)
	}
	if got[1].Until != "" || got[1].Reason != availability.GateKindText(availability.SpawnFailed) {
		t.Errorf("gates[1] = %+v", got[1])
	}
	data, _ := json.Marshal(StatusLineDoc{})
	if strings.Contains(string(data), `"gates"`) {
		t.Errorf("empty doc carries gates: %s", data)
	}
}

func TestStatusLineHaltCarriesTheWaitingLine(t *testing.T) {
	t.Parallel()
	halted := BindingStatus{Name: "x", Round: 2, Display: "NEEDS YOU",
		Waiting: &Waiting{Cause: "halted", Line: "builder exited without a report", Hint: "relevo status --name x"}}
	row, _ := nextOfFixture(halted)
	if row.Halt != "builder exited without a report" {
		t.Fatalf("Halt = %q, want the waiting line", row.Halt)
	}
	running := BindingStatus{Name: "x", Round: 1, Display: "ACTIVE", BuilderStatus: "working", RoundStart: baseTime.Add(-time.Minute)}
	if row, doc := nextOfFixture(running); row.Halt != "" || strings.Contains(doc, `"halt"`) {
		t.Fatalf("Halt = %q on a running row: %s", row.Halt, doc)
	}
}

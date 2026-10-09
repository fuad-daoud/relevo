package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStatusLineRowActivityIsActivityWord(t *testing.T) {
	t.Parallel()
	open := baseTime.Add(-4 * time.Minute)
	for _, tt := range []struct {
		name, status, quiet, want string
	}{
		{"working", "working", "", "working"},
		{"quiet", "working", "2m", "quiet 2m"},
		{"stalled", "stalled 17m", "", "stalled 17m"},
		{"idle", "idle", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := BindingStatus{Name: "x", Round: 1, Display: "ACTIVE", BuilderStatus: tt.status, QuietFor: tt.quiet, RoundStart: open}
			rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, baseTime)
			if got := rows[0].Activity; got != tt.want {
				t.Fatalf("Activity = %q, want %q", got, tt.want)
			}
			data, _ := json.Marshal(rows[0])
			if has := strings.Contains(string(data), `"activity"`); has != (tt.want != "") {
				t.Errorf("activity key present = %v in %s", has, data)
			}
			if strings.Contains(string(data), `"chain_progress"`) {
				t.Errorf("ordinary row carries chain_progress: %s", data)
			}
		})
	}
}

func TestStatusLineRowChainProgress(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		f    ChainFacts
		want ChainProgress
	}{
		{"fixed", ChainFacts{Status: "running", Plan: 3, Plans: 4, Step: "reviewing"}, ChainProgress{2, 4, "reviewing"}},
		{"flow", ChainFacts{Status: "running", StepAt: "build", Round: 1, PlanPos: 1, PlanTotal: 3}, ChainProgress{0, 3, "build"}},
		{"done", ChainFacts{Status: "done", PlanPos: 4, PlanTotal: 4, StepAt: "done"}, ChainProgress{4, 4, "done"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := BindingStatus{Name: "c", Display: "ACTIVE", Chain: &tt.f}
			rows := StatusLineRows(Report{Bindings: []BindingStatus{b}}, baseTime)
			if rows[0].ChainProgress == nil || *rows[0].ChainProgress != tt.want {
				t.Fatalf("ChainProgress = %+v, want %+v", rows[0].ChainProgress, tt.want)
			}
			data, _ := json.Marshal(rows[0])
			if !strings.Contains(string(data), `"chain_progress":{"done":`) {
				t.Errorf("json = %s", data)
			}
		})
	}
}

func TestStatusLineDocPushLiveAlwaysPresent(t *testing.T) {
	t.Parallel()
	data, _ := json.Marshal(StatusLineDoc{})
	if !strings.Contains(string(data), `"push_live":false`) {
		t.Errorf("doc = %s, want push_live:false", data)
	}
}

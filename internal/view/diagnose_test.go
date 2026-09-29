package view

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestDiagnoseBuilderDerivesBothFacts(t *testing.T) {
	t.Parallel()

	sent := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		binding  store.Binding
		wantOpen bool
	}{
		{
			name: "round in flight and session recorded",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: true,
		},
		{
			name: "round closed and session recorded",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: false,
		},
		{
			name: "named, session-less endpoint",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", AgentName: "webshop-builder"},
			},
			wantOpen: false,
		},
		{
			name: "round in flight and no session",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: true,
		},
		{
			name: "round closed and no session",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: false,
		},
		{
			name:     "zero binding does not panic",
			binding:  store.Binding{},
			wantOpen: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnoseBuilder(tc.binding)
			if got.RoundOpen != tc.wantOpen {
				t.Errorf("RoundOpen = %v, want %v", got.RoundOpen, tc.wantOpen)
			}
		})
	}
}

// TestDiagnoseBuilderReadsTheShape pins that reader-ness comes from the
// stored shape alone: an empty shape is a writer, exactly as it always was.
func TestDiagnoseBuilderReadsTheShape(t *testing.T) {
	t.Parallel()

	if got := DiagnoseBuilder(store.Binding{Shape: store.ShapeReader}); !got.Reader {
		t.Error("a reader binding must diagnose as a reader")
	}
	if got := DiagnoseBuilder(store.Binding{Shape: store.ShapeWriter}); got.Reader {
		t.Error("a writer binding must not diagnose as a reader")
	}
	if got := DiagnoseBuilder(store.Binding{}); got.Reader {
		t.Error("an empty shape is a writer and must not diagnose as a reader")
	}
}

func TestBuilderDiagnosisDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		d     BuilderDiagnosis
		round int
		want  string
	}{
		{
			name:  "round open",
			d:     BuilderDiagnosis{RoundOpen: true},
			round: 3,
			want:  "round 3 was open -- that work is unaccounted for; rebind and resend the round",
		},
		{
			name:  "report delivered",
			d:     BuilderDiagnosis{},
			round: 3,
			want:  "round 2 report delivered; nothing outstanding -- unless you want another round",
		},
		{
			name:  "reader output delivered",
			d:     BuilderDiagnosis{Reader: true},
			round: 3,
			want:  "round 2 output delivered; nothing outstanding -- unless you want another round",
		},
		{
			name:  "never sent",
			d:     BuilderDiagnosis{},
			round: 1,
			want:  "no round has been sent yet; nothing outstanding -- unless you want to send one",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.Detail(tc.round)
			if got != tc.want {
				t.Errorf("Detail(%d) =\n  %q\nwant\n  %q", tc.round, got, tc.want)
			}
		})
	}
}

// A binding bound but never sent has Round 1, and queueReport's increment
// means the "delivered report" wording would name round 0 -- a report that
// does not exist. That binding is also the one whose builder has taken no
// turn, so it is exactly the case this detail exists to warn about; it must
// not be papered over with a nonsense round number.
func TestDetailNeverNamesRoundZero(t *testing.T) {
	t.Parallel()

	for _, d := range []BuilderDiagnosis{{}, {RoundOpen: true}} {
		got := d.Detail(1)
		if strings.Contains(got, "round 0") {
			t.Errorf("Detail(1) = %q, must not name round 0", got)
		}
		if got == "" {
			t.Error("Detail must be total, got empty string")
		}
	}
}

func TestDetailIsTotal(t *testing.T) {
	t.Parallel()

	for _, round := range []int{0, 1, 2, 7} {
		for _, open := range []bool{true, false} {
			d := BuilderDiagnosis{RoundOpen: open}
			if d.Detail(round) == "" {
				t.Errorf("Detail(%d) empty for %+v", round, d)
			}
		}
	}
}

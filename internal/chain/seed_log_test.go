package chain

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// logStore is a store whose member logs hold the given entries, in order.
func logStore(t *testing.T, logs map[string][]store.LogEntry) *store.Store {
	t.Helper()
	s := store.New(t.TempDir())
	for name, entries := range logs {
		if err := s.WithLock(func(tx *store.Tx) error {
			return tx.Save(store.Binding{Name: name, CWD: s.Dir(name)})
		}); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
		for _, e := range entries {
			e.TS = time.Now().UTC()
			if err := s.AppendLog(name, e); err != nil {
				t.Fatalf("append %s: %v", name, err)
			}
		}
	}
	return s
}

func inTx(t *testing.T, s *store.Store, fn func(tx *store.Tx)) {
	t.Helper()
	if err := s.WithLock(func(tx *store.Tx) error { fn(tx); return nil }); err != nil {
		t.Fatal(err)
	}
}

func report(round int, path string) store.LogEntry {
	return store.LogEntry{Direction: store.DirToMasterMind, Kind: store.KindReport, Round: round, Path: path}
}

// TestMemberReportPathFindsThePlannersRoundReport pins that the planner's
// plan for a round is the path on that round's report entry, and that an
// entry with no path, a prompt entry, or another round never stands in for it.
func TestMemberReportPathFindsThePlannersRoundReport(t *testing.T) {
	t.Parallel()

	s := logStore(t, map[string][]store.LogEntry{
		"c-plan": {
			{Direction: store.DirToBuilder, Kind: store.KindPrompt, Round: 2, Path: "/prompt-2.md"},
			report(1, "/plan-1.md"),
			report(2, ""),
			report(2, "/plan-2.md"),
		},
	})
	inTx(t, s, func(tx *store.Tx) {
		got, err := MemberReportPath(tx, "c-plan", 2)
		if err != nil || got != "/plan-2.md" {
			t.Errorf("MemberReportPath(round 2) = %q, %v; want /plan-2.md", got, err)
		}
		_, err = MemberReportPath(tx, "c-plan", 3)
		if err == nil || !strings.Contains(err.Error(), "chain planner c-plan has no plan for round 3") {
			t.Errorf("MemberReportPath(round 3) err = %v, want the no-plan refusal", err)
		}
	})
}

// TestRoundGateReadsTheRoundsReportGate pins that the gate is the one on the
// builder's report entry for that round, the newest such entry winning, and
// that a round with no report entry has no gate.
func TestRoundGateReadsTheRoundsReportGate(t *testing.T) {
	t.Parallel()

	first := report(2, "/r.md")
	first.Gate = &store.GateRecord{Result: "fail"}
	second := report(2, "/r.md")
	second.Gate = &store.GateRecord{Result: "pass"}
	other := report(1, "/r.md")
	other.Gate = &store.GateRecord{Result: "fail"}
	diff := store.LogEntry{Direction: store.DirToMasterMind, Kind: store.KindDiff, Round: 2, Gate: &store.GateRecord{Result: "fail"}}
	s := logStore(t, map[string][]store.LogEntry{"c": {other, first, second, diff}})

	inTx(t, s, func(tx *store.Tx) {
		got, err := RoundGate(tx, "c", 2)
		if err != nil || got == nil || got.Result != "pass" {
			t.Errorf("RoundGate(round 2) = %+v, %v; want the newest entry's pass", got, err)
		}
		got, err = RoundGate(tx, "c", 3)
		if err != nil || got != nil {
			t.Errorf("RoundGate(round 3) = %+v, %v; want nil", got, err)
		}
	})
}

// TestNewestReportAtIsTheLatestReportTime pins that only a report's time counts,
// and that the latest one wins wherever it sits in the log.
func TestNewestReportAtIsTheLatestReportTime(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.Save(store.Binding{Name: "c", CWD: s.Dir("c")})
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	base := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	for _, e := range []store.LogEntry{
		{Direction: store.DirToMasterMind, Kind: store.KindReport, Round: 1, TS: base.Add(time.Minute)},
		{Direction: store.DirToMasterMind, Kind: store.KindReport, Round: 2, TS: base.Add(3 * time.Minute)},
		{Direction: store.DirToMasterMind, Kind: store.KindReport, Round: 3, TS: base.Add(2 * time.Minute)},
		{Direction: store.DirToMasterMind, Kind: store.KindDiff, Round: 4, TS: base.Add(9 * time.Minute)},
		{Direction: store.DirToBuilder, Kind: store.KindPrompt, Round: 5, TS: base.Add(9 * time.Minute)},
	} {
		if err := s.AppendLog("c", e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	inTx(t, s, func(tx *store.Tx) {
		if got, want := NewestReportAt(tx, "c"), base.Add(3*time.Minute); !got.Equal(want) {
			t.Errorf("NewestReportAt(c) = %v, want %v", got, want)
		}
		for _, name := range []string{"", "gone"} {
			if got := NewestReportAt(tx, name); !got.IsZero() {
				t.Errorf("NewestReportAt(%q) = %v, want zero", name, got)
			}
		}
	})
}

// TestNewestClosedRoundIsTheHighestReportedRound pins that only report
// entries count, that the highest round wins regardless of order, and that
// an empty name or a member with no log reads as round 0.
func TestNewestClosedRoundIsTheHighestReportedRound(t *testing.T) {
	t.Parallel()

	s := logStore(t, map[string][]store.LogEntry{
		"c": {
			report(3, "/r3.md"),
			report(1, "/r1.md"),
			{Direction: store.DirToBuilder, Kind: store.KindPrompt, Round: 5, Path: "/p5.md"},
			{Direction: store.DirToMasterMind, Kind: store.KindDiff, Round: 4, Path: "/d4.patch"},
		},
	})
	inTx(t, s, func(tx *store.Tx) {
		for _, tc := range []struct {
			name string
			want int
		}{{"c", 3}, {"", 0}, {"gone", 0}} {
			if got := NewestClosedRound(tx, tc.name); got != tc.want {
				t.Errorf("NewestClosedRound(%q) = %d, want %d", tc.name, got, tc.want)
			}
		}
	})
}

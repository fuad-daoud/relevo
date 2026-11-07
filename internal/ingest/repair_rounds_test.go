package ingest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRepairPassCorrectsStaleRoundRowsOnce pins the repair a re-ingest
// performs: rows an earlier derivation left behind are re-derived from the
// stored log, only the rows that moved reach the outbox, and a second pass
// exports nothing with the rows byte-identical. Round 2's switch takes two
// spellings: the poll note a server-only history leaves, beside the shipped
// entry carrying its account, and the switch entry naming what it replaced.
func TestRepairPassCorrectsStaleRoundRowsOnce(t *testing.T) {
	const (
		candA = "opencode/openrouter/z-ai/glm-5.3-flash"
		candB = "agy/google/gemini-3.8-flash-high"
	)
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	pickA := store.LogEntry{
		TS: at, Round: 1, Direction: store.DirToMasterMind,
		Kind: store.KindPick, Confirmed: true,
		Note: "picked " + candA + " for builder: order #1; account cp1",
	}
	switchCases := []struct {
		name    string
		entries []store.LogEntry
	}{
		{
			"poll note",
			[]store.LogEntry{
				{
					TS: at.Add(30 * time.Minute), Round: 2, Direction: store.DirToMasterMind,
					Kind: store.KindSwitch, Confirmed: true,
					Note: store.PollSwitchNotePrefix + "zen: " + candA + " -> " + candB,
				},
				{
					TS: at.Add(time.Hour), Round: 2, Direction: store.DirToMasterMind,
					Kind: store.KindSwitch, Confirmed: true,
					Note: "switched builder (rate-limited): picked " + candB + " for builder: order #2; account cp2",
				},
			},
		},
		{
			"replaced clause",
			[]store.LogEntry{
				{
					TS: at.Add(time.Hour), Round: 2, Direction: store.DirToMasterMind,
					Kind: store.KindSwitch, Confirmed: true,
					Note: "switched builder (rate-limited): picked " + candB + " for builder: order #2; account cp2" + store.SwitchReplacedClause + candA,
				},
			},
		},
	}

	for _, tc := range switchCases {
		t.Run(tc.name, func(t *testing.T) {
			b := store.Binding{
				Name:             "fixture",
				CWD:              "/work/fixture",
				Builder:          store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless},
				Role:             "builder",
				BuilderCandidate: candB,
				BuilderAccount:   "cp2",
				CreatedAt:        at,
				Round:            2,
			}
			entries := append([]store.LogEntry{pickA}, tc.entries...)
			dir := writeLogFixture(t, b, entries)
			path, d := openRepoDB(t)
			if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
				t.Fatalf("first Ingest: %v", err)
			}

			// Stand in for rows the earlier derivation wrote: round 1
			// carries the current candidate and account through.
			bindingID := mustBinding(t, d, "fixture").ID
			rounds := mustRounds(t, d, bindingID)
			if len(rounds) != 2 {
				t.Fatalf("len(rounds) = %d, want 2", len(rounds))
			}
			stale := rounds[0]
			stale.Candidate = strPtr(candB)
			stale.Harness = strPtr("agy")
			stale.Provider = strPtr("google")
			stale.Model = strPtr("gemini-3.8-flash-high")
			stale.Account = strPtr("cp2")
			if _, err := d.UpsertRound(stale); err != nil {
				t.Fatalf("UpsertRound stale: %v", err)
			}

			if err := d.TruncateOutbox(); err != nil {
				t.Fatalf("TruncateOutbox: %v", err)
			}
			if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
				t.Fatalf("repair Ingest: %v", err)
			}

			rounds = mustRounds(t, d, bindingID)
			if !strEq(rounds[0].Candidate, candA) || !strEq(rounds[0].Harness, "opencode") ||
				!strEq(rounds[0].Provider, "openrouter") || !strEq(rounds[0].Model, "z-ai/glm-5.3-flash") ||
				!strEq(rounds[0].Account, "cp1") {
				t.Errorf("round 1 = %v/%v/%v/%v/%v, want %s with opencode/openrouter/z-ai/glm-5.3-flash/cp1",
					rounds[0].Candidate, rounds[0].Harness, rounds[0].Provider, rounds[0].Model, rounds[0].Account, candA)
			}
			if !strEq(rounds[1].Candidate, candB) || !strEq(rounds[1].Account, "cp2") {
				t.Errorf("round 2 = %v/%v, want %s/cp2", rounds[1].Candidate, rounds[1].Account, candB)
			}
			if n := countWhere(t, path, `SELECT count(*) FROM sync_outbox WHERE tbl = 'round'`); n != 1 {
				t.Errorf("round outbox entries = %d, want 1: only the corrected row", n)
			}
			if n := countWhere(t, path, `SELECT count(*) FROM sync_outbox`); n != 1 {
				t.Errorf("outbox entries = %d, want 1: no binding, event or artifact row", n)
			}

			if err := d.TruncateOutbox(); err != nil {
				t.Fatalf("TruncateOutbox: %v", err)
			}
			before := mustRounds(t, d, bindingID)
			if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
				t.Fatalf("second repair Ingest: %v", err)
			}
			if n := countWhere(t, path, `SELECT count(*) FROM sync_outbox`); n != 0 {
				t.Errorf("outbox entries = %d, want 0: the second pass exports nothing", n)
			}
			if after := mustRounds(t, d, bindingID); !reflect.DeepEqual(before, after) {
				t.Errorf("round rows moved on the second pass: before %+v, after %+v", before, after)
			}
		})
	}
}

// TestRepairPassKeepsRoundsWithoutEvidence pins what the repair deliberately
// leaves: a round whose stored events name no replaced candidate and no
// account keeps the values it already holds.
func TestRepairPassKeepsRoundsWithoutEvidence(t *testing.T) {
	const candB = "agy/google/gemini-3.8-flash-high"
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	b := store.Binding{
		Name:             "fixture",
		CWD:              "/work/fixture",
		Builder:          store.Endpoint{Kind: "opencode", Mode: store.ModeHeadless},
		Role:             "builder",
		BuilderCandidate: candB,
		BuilderAccount:   "cp2",
		CreatedAt:        at,
		Round:            2,
	}
	dir := writeLogFixture(t, b, []store.LogEntry{
		{
			TS: at, Round: 2, Direction: store.DirToMasterMind,
			Kind: store.KindSwitch, Confirmed: true,
			Note: store.PollSwitchNotePrefix + "zen:  -> " + candB,
		},
	})
	path, d := openRepoDB(t)
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	bindingID := mustBinding(t, d, "fixture").ID
	rounds := mustRounds(t, d, bindingID)
	if len(rounds) != 1 {
		t.Fatalf("len(rounds) = %d, want 1", len(rounds))
	}
	held := rounds[0]
	held.Account = strPtr("cp7")
	if _, err := d.UpsertRound(held); err != nil {
		t.Fatalf("UpsertRound held: %v", err)
	}

	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}
	if _, err := Ingest(context.Background(), DirSource(dir), d, Deps{}); err != nil {
		t.Fatalf("repair Ingest: %v", err)
	}

	rounds = mustRounds(t, d, bindingID)
	if !strEq(rounds[0].Candidate, candB) || !strEq(rounds[0].Account, "cp7") {
		t.Errorf("round 2 = %v/%v, want the held %s/cp7", rounds[0].Candidate, rounds[0].Account, candB)
	}
	if n := countWhere(t, path, `SELECT count(*) FROM sync_outbox`); n != 0 {
		t.Errorf("outbox entries = %d, want 0: the held row does not move", n)
	}
}

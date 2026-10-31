package ui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// haltStore plants a chain the cockpit will show as a row of its own: a halted
// chain named shop whose builder member carries the chain's own name, which is
// the shape every chain really has.
// nowUTC is the clock the cockpit's runtime runs on, so a stop decision is
// read against a fixed time rather than the wall clock.
var nowUTC = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func haltStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.New(filepath.Join(t.TempDir(), "state"))
	now := nowUTC
	b := store.Binding{
		Name: "shop", CWD: "/w/shop", MasterMindID: "mm",
		State: store.StateActive, Shape: store.ShapeWriter, Round: 2,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save shop: %v", err)
	}
	// A second binding with no round started at all: the binding-shaped
	// nothing-to-stop, beside the chain-shaped answer.
	lone := b
	lone.Name, lone.Round, lone.CWD = "lone", 0, "/w/lone"
	if err := st.Save(lone); err != nil {
		t.Fatalf("Save lone: %v", err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		c := db.ChainRow{
			ID: db.NewID(), Name: "shop", Status: string(chain.StatusHalted),
			Reason: "reviewer wants changes", Branch: "relevo/shop", Phase: "reviewing", Step: "review", Plan: 1, Plans: 1,
			Worktree: "/w/shop", Builder: "shop", MasterMindID: "mm",
			SettingsJSON: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.ChainPut(c); err != nil {
			return err
		}
		return tx.ChainMembersPut("shop", []db.ChainMemberRow{
			{Binding: "shop", Actor: "builder", Seq: 0},
		})
	}); err != nil {
		t.Fatalf("plant the halted chain: %v", err)
	}
	return st
}

// TestCockpitStopOnAHaltedChainNamesDone pins the cockpit's stop text: the same
// sentence the CLI prints and the JSON document carries, so a reader who meets
// the halt in either place is told the same command. It is an answer, not a
// failure -- Err stays nil, exactly as the binding case's is.
func TestCockpitStopOnAHaltedChainNamesDone(t *testing.T) {
	t.Parallel()

	st := haltStore(t)
	rt := relevo.Runtime{Store: st, Now: func() time.Time { return nowUTC }}
	a := &mastermindActions{live: newLiveRuntime(rt)}

	res := a.Stop(context.Background(), "shop")
	if res.Err != nil {
		t.Fatalf("Stop = %v, want an answer, not a failure", res.Err)
	}
	if want := chain.ChainHaltedStopText("shop"); res.Text != want {
		t.Errorf("Stop text = %q, want %q", res.Text, want)
	}
	if !res.Refresh {
		t.Error("Stop did not ask for a refresh: the row's own state is what changed the answer")
	}
}

// TestCockpitStopOnAPlainBindingKeepsItsOwnText pins that the halt's wording
// is the chain row's alone: a binding with no open round still reads as a
// binding with no open round.
func TestCockpitStopOnAPlainBindingKeepsItsOwnText(t *testing.T) {
	t.Parallel()

	st := haltStore(t)
	rt := relevo.Runtime{Store: st, Now: func() time.Time { return nowUTC }}
	a := &mastermindActions{live: newLiveRuntime(rt)}

	res := a.Stop(context.Background(), "lone")
	if res.Err != nil {
		t.Fatalf("Stop = %v, want an answer", res.Err)
	}
	if want := "nothing to stop: lone has no open round"; res.Text != want {
		t.Errorf("Stop text = %q, want %q", res.Text, want)
	}
}

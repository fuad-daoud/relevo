package chain

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestChainReadDir pins the one place the chain-membership decision is made
// for the claude chain-input read grant: a member gets the absolute chain
// directory, everyone else gets "" so PrintArgs renders nothing.
func TestChainReadDir(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	member := store.Binding{Name: "shop", CWD: t.TempDir(), Round: 1, State: store.StateActive}
	if err := s.Save(member); err != nil {
		t.Fatalf("Save(member): %v", err)
	}
	now := time.Now().UTC()
	row := db.ChainRow{
		ID: db.NewID(), Name: "shop", Status: string(StatusHalted),
		Phase: "build", Step: "building", Plan: 1, Plans: 1, Builder: "shop",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(row, []store.Binding{member})
	}); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	want := s.ChainDir("shop")

	// Outside the lock: the store path.
	if got := Dir(s, nil, "shop"); got != want {
		t.Errorf("Dir(s, nil, member) = %q, want %q", got, want)
	}
	// Inside the lock: the tx path must agree with the store path.
	var gotTx string
	if err := s.WithLock(func(tx *store.Tx) error {
		gotTx = Dir(s, tx, "shop")
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if gotTx != want {
		t.Errorf("Dir(s, tx, member) = %q, want %q", gotTx, want)
	}
	// A non-member is "" on both paths, and "" is what PrintArgs drops.
	if got := Dir(s, nil, "not-a-member"); got != "" {
		t.Errorf("Dir(s, nil, non-member) = %q, want empty", got)
	}
	if err := s.WithLock(func(tx *store.Tx) error {
		if got := Dir(s, tx, "not-a-member"); got != "" {
			t.Errorf("Dir(s, tx, non-member) = %q, want empty", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	// No store at all is "", not a panic.
	if got := Dir(nil, nil, "shop"); got != "" {
		t.Errorf("Dir with no store = %q, want empty", got)
	}
}

// TestChainReadDirNoChains covers the store that holds no chain at all: the
// membership lookup finds nothing and the grant is not rendered.
func TestChainReadDirNoChains(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{Name: "solo", CWD: t.TempDir(), Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Dir(s, nil, "solo"); got != "" {
		t.Errorf("Dir with no chains = %q, want empty", got)
	}
}

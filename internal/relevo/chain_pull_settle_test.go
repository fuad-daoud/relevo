package relevo

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The tests here drive a chain pull twice over an unchanged server view. The
// property they pin is the round's cost: a pull that changes nothing writes
// nothing -- no binding_record, chains or mastermind outbox entries -- and a
// pull that does change something still writes exactly what it did before.

// outboxRaw keeps a driver connection on the store's machine database so a
// test reads the entries the shared-table triggers collected.
func outboxRaw(t *testing.T, rt Runtime) *sql.DB {
	t.Helper()
	raw, err := db.OpenRaw(rt.Store.DBPath())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

// outboxPerTable counts the outbox entries each shared table left behind.
func outboxPerTable(t *testing.T, raw *sql.DB) map[string]int {
	t.Helper()
	rows, err := raw.Query(`SELECT tbl, COUNT(*) FROM sync_outbox GROUP BY tbl`)
	if err != nil {
		t.Fatalf("count sync_outbox: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var tbl string
		var n int
		if err := rows.Scan(&tbl, &n); err != nil {
			t.Fatalf("scan sync_outbox: %v", err)
		}
		out[tbl] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("walk sync_outbox: %v", err)
	}
	return out
}

// chainPullRoundFilesFail serves every member through base except one, whose
// round files can never be fetched: the fetch at the loop's head breaks the
// member's install there and its mirror round never advances.
func chainPullRoundFilesFail(fail string, base func(context.Context, string, string, int, string) (io.ReadCloser, error)) func(context.Context, string, string, int, string) (io.ReadCloser, error) {
	return func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
		if name == fail {
			return nil, errors.New("the round files for " + fail + " cannot be fetched")
		}
		return base(ctx, server, name, round, kind)
	}
}

// TestChainPullStoppedViewSettlesBehindMembers pins a server-stopped chain one
// member of which can never catch up: the mirror settles to stopped with the
// one end delivery, and a repeated pass over the same view writes nothing --
// no binding_record, chains or mastermind outbox entries, and no updated_at
// column moves.
//
// The member that can never catch up is the builder: it sits at the seeded
// round 1 while the server closed round 1, and its round-file fetch fails
// deterministically, so the strict caught-up guard would hold the mirror open
// forever. The reviewer is caught up and installs its round.
func TestChainPullStoppedViewSettlesBehindMembers(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusStopped), 1, 1, 0))
	fr.roundFileFunc = chainPullRoundFilesFail("shop", chainPullFiles())
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	raw := outboxRaw(t, rt)

	pullRounds(t, rt, "shop")

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusStopped) {
		t.Errorf("mirror status = %q, want stopped: a server-stopped chain settles even with a member behind", row.Status)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 1 {
		t.Fatalf("pending deliveries = %d, want the one end delivery", len(pending))
	}
	if got := chainBinding(t, rt, "shop"); got.Round != 1 {
		t.Fatalf("builder round = %d, want 1: its closed round cannot be fetched", got.Round)
	}
	if got := chainBinding(t, rt, "shop-rev"); got.Round != 2 {
		t.Errorf("reviewer round = %d, want 2: its closed round keeps installing", got.Round)
	}

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	before := outboxPerTable(t, raw)
	pullRounds(t, rt, "shop")
	after := outboxPerTable(t, raw)

	for _, tbl := range []string{"binding_record", "chains", "mastermind"} {
		if after[tbl] != before[tbl] {
			t.Errorf("outbox entries in %s = %d after, %d before: a repeated pull of an unchanged view writes nothing", tbl, after[tbl], before[tbl])
		}
	}
	if row2 := chainStoredRow(t, rt, "shop"); !row2.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("chain row updated_at = %v, want it stable at %v: an unchanged row keeps its stamp", row2.UpdatedAt, row.UpdatedAt)
	}
	if rev2 := chainBinding(t, rt, "shop-rev"); !rev2.UpdatedAt.Equal(rev.UpdatedAt) {
		t.Errorf("reviewer updated_at = %v, want it stable at %v: an unchanged member keeps its stamp", rev2.UpdatedAt, rev.UpdatedAt)
	}
	if !chainStatusTerminal(chainStoredRow(t, rt, "shop").Status) {
		t.Errorf("mirror status after a second pass = %q, want still stopped", chainStoredRow(t, rt, "shop").Status)
	}
}

// TestChainPullSaveIfChangedDropsNothingButTheStamp pins the comparison the
// pull's saves run under: a record equal to the stored one but for the stamp
// saves nothing, and a moved field saves. The stamp observation is the
// outbox, because Save restamps on every call. The field case includes a
// poll cache the pass nulls out of a record that holds it: the empty pointer
// costs exactly one save (the null-out the pass performs is a real change),
// and the record the write leaves behind -- nulls, as the pass recomputes
// them -- is untouched by every pass after it, which is what the no-op
// two-pass tests rest on.
func TestChainPullSaveIfChangedDropsNothingButTheStamp(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	repo := rt.Store.Dir("webshop")
	b := store.Binding{
		Name: "webshop", CWD: repo, Round: 1, State: store.StateActive,
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw := outboxRaw(t, rt)
	count := func() int {
		return outboxPerTable(t, raw)["binding_record"]
	}

	// Equal but for the stamp: nothing is written.
	before := count()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		return chainPullSaveIfChanged(tx, cur, cur)
	}); err != nil {
		t.Fatalf("pass an unchanged record: %v", err)
	}
	if count() != before {
		t.Errorf("binding_record outbox entries = %d, want %d: an unchanged record saves nothing", count(), before)
	}

	// A moved field saves exactly once.
	before = count()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		snap := cur
		cur.Round = 2
		return chainPullSaveIfChanged(tx, snap, cur)
	}); err != nil {
		t.Fatalf("pass a round advance: %v", err)
	}
	if count() != before+1 {
		t.Errorf("binding_record outbox entries = %d, want %d: a moved field saves once", count(), before+1)
	}
	if cur := chainBinding(t, rt, "webshop"); cur.Round != 2 {
		t.Errorf("round = %d, want 2: the moved field persisted", cur.Round)
	}

	// A poll cache that passes off the record saves once -- the null-out is
	// a real change -- and the record the save leaves holds the cache nil,
	// which the next pass over the same view must read as unchanged.
	b2 := chainBinding(t, rt, "webshop")
	b2.Builder.RemoteQueue = &store.QueueFacts{Position: 1}
	if err := rt.Store.Save(b2); err != nil {
		t.Fatalf("seed a held poll cache: %v", err)
	}
	before = count()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		snap := cur
		cur.Builder.RemoteQueue = nil
		cur.Builder.RemoteLive = nil
		return chainPullSaveIfChanged(tx, snap, cur)
	}); err != nil {
		t.Fatalf("null the poll cache out: %v", err)
	}
	if count() != before+1 {
		t.Errorf("binding_record outbox entries = %d, want %d: nulling a held poll cache saves once", count(), before+1)
	}
	before = count()
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load("webshop")
		if err != nil {
			return err
		}
		cur.Builder.RemoteQueue = nil
		cur.Builder.RemoteLive = nil
		return chainPullSaveIfChanged(tx, cur, cur)
	}); err != nil {
		t.Fatalf("pass the settled record again: %v", err)
	}
	if count() != before {
		t.Errorf("binding_record outbox entries = %d, want %d: a settled null cache saves nothing", count(), before)
	}
}

// TestChainPullHaltedStaysRunningBehindMembers pins the kept guard: a halted
// view holds the caught-up rule, so a mirror whose builder cannot fetch stays
// running with nothing queued -- and a repeated pass over the same view
// writes nothing at all.
func TestChainPullHaltedStaysRunningBehindMembers(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 1, 1, 0))
	fr.roundFileFunc = chainPullRoundFilesFail("shop", chainPullFiles())
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	raw := outboxRaw(t, rt)

	pullRounds(t, rt, "shop")

	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("mirror status = %q, want running: a halted view no longer settles a behind member", row.Status)
	}
	if pending := chainPendingChain(t, rt, "shop"); len(pending) != 0 {
		t.Fatalf("pending deliveries = %d, want none while the member is behind", len(pending))
	}

	row := chainStoredRow(t, rt, "shop")
	rev := chainBinding(t, rt, "shop-rev")
	before := outboxPerTable(t, raw)
	pullRounds(t, rt, "shop")
	after := outboxPerTable(t, raw)

	for _, tbl := range []string{"binding_record", "chains", "mastermind"} {
		if after[tbl] != before[tbl] {
			t.Errorf("outbox entries in %s = %d after, %d before: a repeated pass over a halted view writes nothing", tbl, after[tbl], before[tbl])
		}
	}
	if row := chainStoredRow(t, rt, "shop"); row.Status != string(chain.StatusRunning) {
		t.Errorf("mirror status after a second pass = %q, want still running", row.Status)
	}
	if row2 := chainStoredRow(t, rt, "shop"); !row2.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("chain row updated_at = %v, want it stable at %v", row2.UpdatedAt, row.UpdatedAt)
	}
	if rev2 := chainBinding(t, rt, "shop-rev"); !rev2.UpdatedAt.Equal(rev.UpdatedAt) {
		t.Errorf("reviewer updated_at = %v, want it stable at %v", rev2.UpdatedAt, rev.UpdatedAt)
	}
}

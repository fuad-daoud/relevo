package relevo

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/store"
)

// countingClaims is the ClaimStore the scope tests count rows with: statusRow
// asks the live claim store once per row it builds, so the set of ids asked
// about is exactly the set of rows built.
type countingClaims struct {
	mu   sync.Mutex
	seen []string
}

func (c *countingClaims) Live(mastermind string, now time.Time) (*delivery.Claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, mastermind)
	return nil, nil
}

func (c *countingClaims) Write(delivery.Claim, time.Time) error { return nil }

func (c *countingClaims) Remove(string, int) error { return nil }

// askedAbout reports how many rows asked about id.
func (c *countingClaims) askedAbout(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, a := range c.seen {
		if a == id {
			n++
		}
	}
	return n
}

func (c *countingClaims) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// TestScopedStatusReachesTheSameRowsAsScopeReport pins that narrowing before
// the build is invisible: for every scope, the scoped builder's rows and the
// full report's rows put through the post-filter name the same bindings and
// report the same DoneHidden.
func TestScopedStatusReachesTheSameRowsAsScopeReport(t *testing.T) {
	rt := scopeFixture(t)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		sc   Scope
	}{
		{"plain", Scope{MasterMindID: testClaimMasterMind}},
		{"all keeps done", Scope{MasterMindID: testClaimMasterMind, All: true}},
		{"named bypasses narrowing", Scope{Named: true}},
		{"every mastermind", Scope{All: true}},
		{"another mastermind", Scope{MasterMindID: otherClaimMasterMind}},
		{"no mastermind", Scope{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			full, err := Status(ctx, rt)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			want := ScopeReport(full, c.sc)

			got, err := ScopedStatus(ctx, rt, c.sc)
			if err != nil {
				t.Fatalf("ScopedStatus: %v", err)
			}

			if !sameNames(rowNames(got), rowNames(want)) {
				t.Errorf("scoped rows = %v, want %v", rowNames(got), rowNames(want))
			}
			if got.DoneHidden != want.DoneHidden {
				t.Errorf("DoneHidden = %d, want %d", got.DoneHidden, want.DoneHidden)
			}
		})
	}
}

// TestScopedStatusBuildsNoRowOutsideTheScope is the pin the name-set assertion
// cannot make: after the post-filter the rows are right either way, so what is
// pinned here is that the rows outside the scope were never built at all.
func TestScopedStatusBuildsNoRowOutsideTheScope(t *testing.T) {
	rt := scopeFixture(t)
	claims := &countingClaims{}
	rt.Channels = claims

	if _, err := ScopedStatus(context.Background(), rt, Scope{MasterMindID: testClaimMasterMind}); err != nil {
		t.Fatalf("ScopedStatus: %v", err)
	}

	// Four rows: a1, a2 and chain cha's two members. b1 and chb's members belong
	// to the other mastermind, and a-done is this one's DONE row -- the DONE
	// rule is scopeBinding's, asked before the row exists, so neither is built.
	if claims.total() != 4 {
		t.Errorf("built %d rows under a narrow scope, want 4", claims.total())
	}
	if n := claims.askedAbout(otherClaimMasterMind); n != 0 {
		t.Errorf("built %d rows of %s under another mastermind's scope, want 0", n, otherClaimMasterMind)
	}
	if claims.askedAbout(testClaimMasterMind) != claims.total() {
		t.Errorf("built rows for %d ids, want only %s", claims.total(), testClaimMasterMind)
	}

	// Asking for DONE brings the fifth row back: the count moves with the scope,
	// so the four above is the narrowing and not a fixture that only has four.
	withDone := &countingClaims{}
	rt.Channels = withDone
	if _, err := ScopedStatus(context.Background(), rt, Scope{MasterMindID: testClaimMasterMind, All: true}); err != nil {
		t.Fatalf("ScopedStatus with All: %v", err)
	}
	if withDone.total() != 5 {
		t.Errorf("built %d rows with All, want 5 (the DONE row too)", withDone.total())
	}
}

// TestScopedStatusKeepsEveryScopedChainMemberRow pins that a chain in scope
// still shows the members it has: a chain row stands in for them on the binding
// listing, so a member the scope hides must not leave the chain nameless.
func TestScopedStatusKeepsEveryScopedChainMemberRow(t *testing.T) {
	rt := scopeFixture(t)

	rep, err := ScopedStatus(context.Background(), rt, Scope{MasterMindID: testClaimMasterMind})
	if err != nil {
		t.Fatalf("ScopedStatus: %v", err)
	}
	names := rowNames(rep)
	for _, want := range []string{"cha"} {
		if !contains(names, want) {
			t.Errorf("rows = %v, want the chain %q", names, want)
		}
	}
	for _, unwanted := range []string{"chb", "b1", "cb1", "cb2"} {
		if contains(names, unwanted) {
			t.Errorf("rows = %v, want no %q from another mastermind", names, unwanted)
		}
	}
}

// TestReadChainsScopeKeepsEveryScopedMemberRow is the same rule on the chains
// reader: a chain listing must show each scoped chain's members whether or not
// the binding scope would keep those members on their own.
func TestReadChainsScopeKeepsEveryScopedMemberRow(t *testing.T) {
	rt := scopeFixture(t)

	for _, sc := range []Scope{{All: true}, {}, {MasterMindID: testClaimMasterMind}} {
		doc, err := ReadChainsScope(context.Background(), rt, sc)
		if err != nil {
			t.Fatalf("ReadChainsScope(%+v): %v", sc, err)
		}
		for _, c := range doc.Chains {
			if len(c.Members) == 0 {
				t.Errorf("chain %q under %+v has no member rows", c.Name, sc)
			}
			for _, m := range c.Members {
				if m.Name == "" {
					t.Errorf("chain %q under %+v has an unnamed member row", c.Name, sc)
				}
			}
		}
	}

	// A member that is gone from the store is a member the report never had, and
	// the listing shows the members it has rather than a blank row for one it
	// does not.
	archiveBinding(t, rt, "cb1")
	doc, err := ReadChainsScope(context.Background(), rt, Scope{All: true})
	if err != nil {
		t.Fatalf("ReadChainsScope after archiving a member: %v", err)
	}
	found := false
	for _, c := range doc.Chains {
		for _, m := range c.Members {
			if m.Name == "" {
				t.Errorf("chain %q lists a member row with no name", c.Name)
			}
			if m.Name == "cb2" {
				found = true
			}
		}
	}
	if !found {
		t.Error("chain chb lost its remaining member row cb2")
	}
}

// archiveBinding takes name out of the listing without touching its chain rows,
// which is how an unbound chain member looks.
func archiveBinding(t *testing.T, rt Runtime, name string) {
	t.Helper()
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store DB: %v", err)
	}
	if err := d.RecordArchive("", name, baseTime); err != nil {
		t.Fatalf("RecordArchive(%q): %v", name, err)
	}
}

// TestReadChainsScopeNarrowsBeforeBuilding is the chains reader's build counter:
// a chain in scope answers for its own members, so the other mastermind's
// bindings are never built to answer for it.
func TestReadChainsScopeNarrowsBeforeBuilding(t *testing.T) {
	rt := scopeFixture(t)
	claims := &countingClaims{}
	rt.Channels = claims

	if _, err := ReadChainsScope(context.Background(), rt, Scope{MasterMindID: testClaimMasterMind}); err != nil {
		t.Fatalf("ReadChainsScope: %v", err)
	}

	if claims.total() == 0 {
		t.Fatal("no row was built, so the counter is not wired")
	}
	if n := claims.askedAbout(otherClaimMasterMind); n != 0 {
		t.Errorf("built %d rows of %s for another mastermind's chain, want 0", n, otherClaimMasterMind)
	}
}

// TestReadChainsScopeFailsOnAStoreError pins that the chain listing reports a
// store failure instead of printing chains with missing member rows: the
// builder is abort-on-first-error, and its error must not be dropped here.
func TestReadChainsScopeFailsOnAStoreError(t *testing.T) {
	rt := scopeFixture(t)
	corruptOneLog(t, rt, "b1")

	_, err := ReadChainsScope(context.Background(), rt, Scope{})
	if err == nil {
		t.Fatal("ReadChainsScope on an unreadable log returned nil error")
	}
	if !strings.Contains(err.Error(), "decode log entry") {
		t.Errorf("error = %v, want the store's own read error", err)
	}
}

// corruptOneLog makes name's log undecodable without making the binding itself
// unlistable: the row is written through the database directly, so List still
// returns the binding and only the log read inside the row build fails.
func corruptOneLog(t *testing.T, rt Runtime, name string) {
	t.Helper()
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("store DB: %v", err)
	}
	rec, ok, err := d.RecordGet("", name)
	if err != nil || !ok {
		t.Fatalf("RecordGet(%q) = (%v, %v), want the seeded row", name, ok, err)
	}
	bad := db.RecordEvent{
		Seq: 900, TS: baseTime, Round: 1,
		Direction: string(store.DirToMasterMind), Kind: string(store.KindReport), JSON: "not json",
	}
	if err := d.EventAppend(rec.ID, bad); err != nil {
		t.Fatalf("EventAppend: %v", err)
	}
}

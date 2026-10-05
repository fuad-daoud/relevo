package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// unusedProvider is a provider name the test candidate set does not hold, so a
// rate limit on it shows up in the report's unused-provider gates.
const unusedProvider = "absent-provider"

// badLedgerKV is a kv handle whose ledger row will not decode.
type badLedgerKV struct{ inner db.KV }

func (b *badLedgerKV) KVGet(key string) ([]byte, bool, error) {
	if key == "ledger" {
		return []byte("not json"), true, nil
	}
	return b.inner.KVGet(key)
}

func (b *badLedgerKV) KVPut(key string, value []byte) error { return b.inner.KVPut(key, value) }

func (b *badLedgerKV) KVDelete(key string) error { return b.inner.KVDelete(key) }

// countingKV is a db.KV that records every key read, so a test can pin how many
// times one report touches a kv row. Writes pass straight through.
type countingKV struct {
	inner *db.DB
	reads map[string]int
}

func (c *countingKV) KVGet(key string) ([]byte, bool, error) {
	c.reads[key]++
	return c.inner.KVGet(key)
}

func (c *countingKV) KVPut(key string, value []byte) error { return c.inner.KVPut(key, value) }

func (c *countingKV) KVDelete(key string) error { return c.inner.KVDelete(key) }

// bulkRuntime is the Runtime the bulk-load tests build on: a real kv database
// behind a countingKV, and a claim store and a wait store that both implement
// the bulk surface Phase 4 added. Each name is saved as an active binding with
// one prompt entry in its log, so no row reads an empty log.
func bulkRuntime(t *testing.T, names ...string) (Runtime, *countingKV) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	kv := &countingKV{inner: d, reads: map[string]int{}}
	tx := db.TxKV{DB: d}
	rt := Runtime{
		Store:      store.New(t.TempDir()),
		Candidates: candidateSet(t, testCandidatesJSON),
		Gates:      kv,
		Latency:    kv,
		Channels:   &delivery.KVClaims{KV: tx, Alive: alwaysAlive},
		Waits:      &delivery.KVWaitClaims{KV: tx, Alive: alwaysAlive},
		Now:        func() time.Time { return baseTime },
	}
	for _, name := range names {
		b := store.Binding{
			Name: name, CWD: "/repo/" + name, Round: 1, State: store.StateActive,
			MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess-" + name},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
		appendEntries(t, rt, name, store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		})
	}
	return rt, kv
}

// rowNamed finds one row by name, so an assertion about a bulk load's failure
// direction reads as that row's fields rather than the whole report's order.
func rowNamed(t *testing.T, rows []view.BindingStatus, name string) view.BindingStatus {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q in %+v", name, rows)
	return view.BindingStatus{}
}

// claimIDFor is a valid claim key for name: pl_ plus twelve characters of
// [a-z2-7], the shape the claim store parses out of its kv key.
func claimIDFor(name string) string {
	s := "pl_" + name + "aaaaaaaa"
	return s[:15]
}

// TestStatusReadsTheLedgerOnce pins step 1: one report projects a single kv row
// twice -- the candidate gates and the unused-provider gates -- and reads it
// once. Before, each projection ran its own LoadLedger, so the row was read and
// decoded twice per report.
func TestStatusReadsTheLedgerOnce(t *testing.T) {
	rt, kv := bulkRuntime(t, "webshop")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := kv.reads["ledger"]; got != 1 {
		t.Errorf("ledger key: %d reads, want exactly 1 per Status", got)
	}
	// Both projections still ran over that one load. An empty ledger leaves
	// Unused empty and not nil-slice-of-err, so assert the report is whole
	// rather than the gates' contents.
	if len(rep.Bindings) != 1 {
		t.Errorf("rows = %d, want 1", len(rep.Bindings))
	}
	if len(rep.Unused) != 0 {
		t.Errorf("Unused = %+v, want none from an empty ledger", rep.Unused)
	}
}

// TestStatusLedgerProjectionsMatchTheSeparateLoads is the byte-identical case
// for step 1 on a ledger that actually holds entries: the report's Gated and
// Unused must equal what the two separate LoadLedger calls produced. It is the
// check an empty ledger cannot make -- with nothing in the ledger, both paths
// are empty and the test would pass even if one projection were dropped.
func TestStatusLedgerProjectionsMatchTheSeparateLoads(t *testing.T) {
	rt, kv := bulkRuntime(t, "webshop")

	// One rate limit against a configured candidate but on a subject the
	// candidate set does not hold, so the entry lands in Unused and not in
	// Gated: unusedProvider is not one of the three providers
	// testCandidatesJSON names.
	if _, err := availability.Unavailable(
		AvailabilityDeps(rt), testOpencodeRef, time.Time{}, "rate-limited", unusedProvider,
	); err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}
	kv.reads = map[string]int{}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := kv.reads["ledger"]; got != 1 {
		t.Errorf("ledger key: %d reads, want exactly 1 per Status", got)
	}

	// The two projections the report shares one load for.
	wantGated := availability.Gates(AvailabilityDeps(rt))
	wantUnused := UnusedProviderGates(rt)
	if !reflect.DeepEqual(rep.Gated, wantGated) {
		t.Errorf("Gated = %+v, want the separate Gates projection %+v", rep.Gated, wantGated)
	}
	if !reflect.DeepEqual(rep.Unused, wantUnused) {
		t.Errorf("Unused = %+v, want the separate UnusedProviderGates projection %+v", rep.Unused, wantUnused)
	}
	// A ledger with an entry in it must actually reach the report, or the two
	// comparisons above would both be comparing empties.
	if len(rep.Unused) == 0 {
		t.Errorf("Unused is empty: the seeded ledger entry did not reach the report")
	}
}

// TestStatusSurvivesALedgerLoadError pins the ledger's error case: a kv row
// that will not decode leaves both projections empty and does not fail the
// report. A load failure is a bookkeeping file's problem, not status's.
func TestStatusSurvivesALedgerLoadError(t *testing.T) {
	rt, _ := bulkRuntime(t, "webshop")
	// The db validates JSON on the way in, so the undecodable row is handed to
	// the reader by a handle that returns bad bytes for the ledger key. A real
	// store can hold such a row -- a hand-edited or older one.
	rt.Gates = &badLedgerKV{inner: rt.Gates}

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status with an unreadable ledger: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Errorf("rows = %d, want 1", len(rep.Bindings))
	}
	if len(rep.Gated) != 0 {
		t.Errorf("Gated = %+v, want none from an unreadable ledger", rep.Gated)
	}
	if len(rep.Unused) != 0 {
		t.Errorf("Unused = %+v, want none from an unreadable ledger", rep.Unused)
	}
}

// TestMasterMindNamesComeFromOneBulkLoad pins step 2's shape: every distinct
// mastermind id in the report is resolved once, before the row loop, and the
// row reads the map. A binding whose id is in the map names its record; one
// whose id is not has an empty name and no error, which is the direction the
// field's comment has always required.
func TestMasterMindNamesComeFromOneBulkLoad(t *testing.T) {
	rt, _ := bulkRuntime(t, "webshop")
	reg, created := testMasterMindRegistry(t, mastermind.Record{
		ID: testMasterMindID, Name: "architect-1",
		HarnessKind: "claude", SessionID: "sess-architect", CWD: "/repo",
	})
	rt.MasterMinds = reg

	// The binding names a mastermind id the registry does not hold: the bulk map
	// has no record for it, so the field is empty and no read is wasted.
	b := openActiveBinding("webshop")
	b.MasterMindID = "pl_absentmasterm"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := statusOrFail(t, rt)[0].MasterMindName; got != "" {
		t.Errorf("MasterMindName = %q, want empty for an id no record holds", got)
	}

	// The same binding bound to the seeded id: the name comes from the one bulk
	// load.
	b.MasterMindID = testMasterMindID
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save bound: %v", err)
	}
	if got := statusOrFail(t, rt)[0].MasterMindName; got != created.Name {
		t.Errorf("MasterMindName = %q, want %q from the bulk map", got, created.Name)
	}
}

// TestRouteAndWaitLiveSurviveAMissingBulkEntry pins step 3's failure direction:
// a claim and a wait the bulk load does not return read as not live, and only
// the affected row changes. A bulk load that fell back to a per-row read would
// find nothing either -- so the case that matters is that the *other* row's
// liveness survives, which the paired claim below pins.
func TestRouteAndWaitLiveSurviveAMissingBulkEntry(t *testing.T) {
	rt, _ := bulkRuntime(t, "held", "orphan")

	// Both rows carry a configured deliverer, so a missing claim row drops the
	// route from "channel" to "deliverer" and the liveness with it.
	rt.Deliverers = map[string]delivery.MasterMindDeliverer{"claude": &haltDeliverer{}}
	for _, name := range []string{"held", "orphan"} {
		b := openActiveBinding(name)
		b.MasterMind = store.Endpoint{Kind: "claude", SessionID: "sess-" + name}
		b.MasterMindID = claimIDFor(name)
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save(%s): %v", name, err)
		}
	}
	if err := rt.Channels.Write(delivery.Claim{
		MasterMind: claimIDFor("held"), PID: 4242,
		StartedAt: baseTime, SeenAt: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("claim write: %v", err)
	}
	if err := rt.Waits.Write(delivery.WaitClaim{
		Name: "held", PID: 4343, StartedAt: baseTime, SeenAt: baseTime,
	}, baseTime); err != nil {
		t.Fatalf("wait write: %v", err)
	}

	rows := statusOrFail(t, rt)
	held := rowNamed(t, rows, "held")
	if held.MasterMindRoute != "channel" || !held.MasterMindRouteLive {
		t.Errorf("held: route = %q live = %v, want channel/live", held.MasterMindRoute, held.MasterMindRouteLive)
	}
	if !held.WaitLive {
		t.Errorf("held: WaitLive = false, want true from the bulk wait map")
	}

	// The claim and wait the bulk load did not return read as not live, and the
	// route falls through to the configured deliverer.
	orphan := rowNamed(t, rows, "orphan")
	if orphan.MasterMindRoute != "deliverer" || !orphan.MasterMindRouteLive {
		t.Errorf("orphan: route = %q live = %v, want deliverer/live with no claim row",
			orphan.MasterMindRoute, orphan.MasterMindRouteLive)
	}
	if orphan.WaitLive {
		t.Errorf("orphan: WaitLive = true, want false with no wait row")
	}
}

// TestViewedAtComesFromTheBindingRow pins step 4, which Phase 3 already met:
// the unread marker's stamp rides the binding the List read, so a stamped row
// reads read and an unstamped one reads unread, and marking one does not touch
// the other. A per-row RecordGet that ignored the binding would flip both.
func TestViewedAtComesFromTheBindingRow(t *testing.T) {
	rt, _ := bulkRuntime(t, "webshop", "other")
	for _, name := range []string{"webshop", "other"} {
		appendEntries(t, rt, name, store.LogEntry{
			TS: baseTime.Add(time.Minute), Round: 1,
			Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "report",
		})
	}

	// No stamp at all: both rows are unread, the no-stamp case.
	rows := statusOrFail(t, rt)
	for _, name := range []string{"webshop", "other"} {
		if got := rowNamed(t, rows, name).Unread; !got {
			t.Errorf("%s: Unread = false, want true with no viewed stamp", name)
		}
	}

	// One stamp, read after the report entry: only that row goes read.
	if err := rt.Store.MarkViewed("other", baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("MarkViewed: %v", err)
	}
	rows = statusOrFail(t, rt)
	if got := rowNamed(t, rows, "webshop").Unread; !got {
		t.Errorf("webshop: Unread = false, want the unstamped row still unread")
	}
	if got := rowNamed(t, rows, "other").Unread; got {
		t.Errorf("other: Unread = true, want false after MarkViewed")
	}
}

// TestBuildReportFailsFastWithNoPartialReport pins the abort-on-first-error
// contract the shared bulk load must not weaken: a store whose third binding's
// log will not decode returns an empty report, not the two rows that built.
func TestBuildReportFailsFastWithNoPartialReport(t *testing.T) {
	rt, _ := bulkRuntime(t, "a", "b", "c")
	corruptOneLog(t, rt, "c")

	rep, err := buildReport(context.Background(), rt, []store.Binding{
		openActiveBinding("a"), openActiveBinding("b"), openActiveBinding("c"),
	})
	if err == nil {
		t.Fatal("buildReport on an unreadable log returned no error")
	}
	if !strings.Contains(err.Error(), "decode log entry") {
		t.Errorf("error = %v, want the store's own read error", err)
	}
	if len(rep.Bindings) != 0 {
		t.Errorf("rows = %+v, want no rows on a failed report", rep.Bindings)
	}
}

// TestStatusSurvivesABulkLoadError pins that a claims or waits read error does
// not take status down: the whole report reads as not live, which is the
// direction every failure has always had. buildReport is abort-on-first-error
// for *store* failures, and a claims read is not one.
func TestStatusSurvivesABulkLoadError(t *testing.T) {
	rt, kv := bulkRuntime(t, "webshop")
	b := openActiveBinding("webshop")
	b.MasterMindID = claimIDFor("webshop")
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt.Channels = &erroringClaimStore{}
	rt.Waits = &erroringWaitStore{}

	rows := statusOrFail(t, rt)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: a bulk-load error must not fail the report", len(rows))
	}
	if rows[0].MasterMindRoute == "channel" {
		t.Errorf("route = channel with a failed bulk load, want not live")
	}
	if rows[0].WaitLive {
		t.Errorf("WaitLive = true with a failed bulk load, want false")
	}
	_ = kv
}

// erroringClaimStore is a ClaimStore whose bulk read fails, and whose single
// read fails too: the report must treat both as not live rather than failing.
type erroringClaimStore struct{}

func (erroringClaimStore) Live(string, time.Time) (*delivery.Claim, error) {
	return nil, errBulkLoad
}

func (erroringClaimStore) Write(delivery.Claim, time.Time) error { return nil }

func (erroringClaimStore) Remove(string, int) error { return nil }

func (erroringClaimStore) LiveAll(time.Time) (map[string]*delivery.Claim, error) {
	return nil, errBulkLoad
}

// erroringWaitStore is erroringClaimStore's wait-side twin.
type erroringWaitStore struct{}

func (erroringWaitStore) Live(string, time.Time) (*delivery.WaitClaim, error) {
	return nil, errBulkLoad
}

func (erroringWaitStore) Write(delivery.WaitClaim, time.Time) error { return nil }

func (erroringWaitStore) Remove(string, int) error { return nil }

func (erroringWaitStore) LiveAll(time.Time) (map[string]*delivery.WaitClaim, error) {
	return nil, errBulkLoad
}

// errBulkLoad is the failure both erroring stores report.
var errBulkLoad = errors.New("kv read failed")

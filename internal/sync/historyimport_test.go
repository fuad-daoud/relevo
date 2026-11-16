package sync

// The none-history pull's own cases: what a steady attempt mirrors, what it
// leaves alone, and that a repeat tick writes nothing. Every case runs against
// the in-memory log, so no case reaches a network or spawns a worker.
//
// These cases are sequential on purpose: the mirrored documents arrive through
// the process-wide builder, and parallel cases would see each other's
// documents.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// mirrorDoc is the TxDoc a none-history case binds: an in-memory document
// writing through the config and secret stores the way the mirrored documents
// do, so a re-apply that wrote again would move updated_at and re-export.
type mirrorDoc struct {
	name    string
	secret  bool
	items   map[string]json.RawMessage
	applies int
}

func newMirrorDoc(name string, secret bool) *mirrorDoc {
	return &mirrorDoc{name: name, secret: secret, items: map[string]json.RawMessage{}}
}

func (d *mirrorDoc) Name() string { return d.name }

func (d *mirrorDoc) Items() (map[string]json.RawMessage, error) { return d.items, nil }

func (d *mirrorDoc) Apply(key string, value json.RawMessage) error {
	if value == nil {
		delete(d.items, key)
	} else {
		d.items[key] = value
	}
	return nil
}

// ApplyIn writes one arriving key through the same store a local edit would
// use, unstamped and unsignalled: a peer's value is not this machine's edit,
// and the importer's applied state already stops it echoing straight back.
func (d *mirrorDoc) ApplyIn(t *db.Tx, key string, value json.RawMessage, _ string) (error, error) {
	d.applies++
	now := time.Now().UTC()
	if value == nil {
		delete(d.items, key)
		if d.secret {
			return nil, t.SecretDelete(key)
		}
		return nil, t.ConfigDelete(key)
	}
	d.items[key] = value
	if d.secret {
		return nil, t.SecretPut(key, []byte(value), now)
	}
	return nil, t.ConfigPut(key, []byte(value), now)
}

func (d *mirrorDoc) SupersedeIn(_ *db.Tx, _ string, _ json.RawMessage, _, _ string) error {
	return nil
}

// countingTransport is the in-memory log with a count per read call, so a case
// pins which half of the pull ran: the ranged pre-pass, the whole-log import,
// or neither.
type countingTransport struct {
	*synclog.MemTransport
	pulls   atomic.Int64
	ranges  atomic.Int64
	appends atomic.Int64
}

func (c *countingTransport) Pull(marks map[string]int) ([]synclog.Entry, error) {
	c.pulls.Add(1)
	return c.MemTransport.Pull(marks)
}

func (c *countingTransport) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	c.ranges.Add(1)
	return c.MemTransport.PullRange(origin, after, upto)
}

func (c *countingTransport) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	c.appends.Add(1)
	return c.MemTransport.Append(entries)
}

// noneFixture is one machine with a key, the mirrored documents bound, and a
// log holding another origin's signed config entry. History selects the mode
// under test.
func noneFixture(t *testing.T, history string) (shared *db.DB, local Local, config, secrets *mirrorDoc, log *countingTransport) {
	t.Helper()
	shared, local = joinPair(t, "m1")
	key := []byte("0123456789abcdef0123456789abcdef")
	if err := SetKey(local, key, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	config, secrets = newMirrorDoc(ConfigDocName, false), newMirrorDoc(SecretsDocName, true)
	docs := []synclog.SharedDoc{config, secrets}
	SetDocsBuilder(func(*db.DB) []synclog.SharedDoc { return docs })
	t.Cleanup(func() { SetDocsBuilder(nil) })
	if err := PutSettings(local, Settings{History: history}, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	inner := synclog.NewMemTransport("m1")
	inner.SetLabel("m2", "peer")
	seedMirrorEntry(t, shared, inner, key, config, "m2", "policy", `{"a":1}`)
	log = &countingTransport{MemTransport: inner}
	return shared, local, config, secrets, log
}

// seedMirrorEntry appends one signed doc entry of origin to the log, the way a
// peer's export wrote it.
func seedMirrorEntry(t *testing.T, shared *db.DB, log *synclog.MemTransport, key []byte, doc synclog.SharedDoc, origin, item, value string) {
	t.Helper()
	_, known := shared.SchemaVersions()
	entry, err := synclog.NewDocUpsert(origin, doc, item, known, synclog.Stamp(1000), json.RawMessage(value), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	entry.MAC = synclog.EntryMAC(key, entry)
	// A signed entry predicts the origin's signed-from boundary, from the
	// head the log holds; the log records it once and later entries ride it.
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	seq := state.Heads[origin] + 1
	if _, err := log.OnLog(origin).AppendSigned([]synclog.Entry{entry},
		synclog.Boundary{Seq: seq, Tag: synclog.BoundaryTag(key, origin, seq)}); err != nil {
		t.Fatalf("append the doc entry: %v", err)
	}
}

// TestSteadyNoneMirrorsDocsWithoutPulling pins the none pull: the ranged
// pre-pass runs, the whole-log import never does, a peer's config entry lands,
// and no import mark moves. The reported applied count stays zero: the
// pre-pass counts handled entries and a redelivery counts too, so the number
// cannot tell a real change from one.
func TestSteadyNoneMirrorsDocsWithoutPulling(t *testing.T) {
	shared, local, config, _, log := noneFixture(t, HistoryNone)

	runner := &Runner{Client: log, Local: local}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if got := log.pulls.Load(); got != 0 {
		t.Errorf("Pull ran %d times, want none: a none machine never imports", got)
	}
	if got := log.ranges.Load(); got == 0 {
		t.Error("PullRange never ran, want the pre-pass's ranged reads")
	}
	if got := string(config.items["policy"]); got != `{"a":1}` {
		t.Errorf("policy = %s, want the peer's entry applied", got)
	}
	if marks, err := shared.ImportMarks(); err != nil {
		t.Fatalf("ImportMarks: %v", err)
	} else if len(marks) != 0 {
		t.Errorf("ImportMarks = %v, want none moved", marks)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want zero: the pre-pass count includes redeliveries", res.Applied)
	}
}

// TestSteadyNoneRecordsPeerLabels pins that the same attempt writes the peers'
// labels into the directory, and nothing for this machine itself.
func TestSteadyNoneRecordsPeerLabels(t *testing.T) {
	shared, local, _, _, log := noneFixture(t, HistoryNone)
	log.SetLabel("m3", "field")

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	for _, want := range []struct{ id, label string }{{"m2", "peer"}, {"m3", "field"}} {
		row, ok := installationByID(t, shared, want.id)
		if !ok {
			t.Errorf("no directory row for the peer %s", want.id)
			continue
		}
		if row.Label != want.label {
			t.Errorf("label of %s = %q, want %q", want.id, row.Label, want.label)
		}
	}
}

// TestSteadyFullStillPulls pins the other mode through the same attempt: a
// full-history machine runs the whole-log import.
func TestSteadyFullStillPulls(t *testing.T) {
	shared, local, _, _, log := noneFixture(t, "")

	runner := &Runner{Client: log, Local: local}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if got := log.pulls.Load(); got == 0 {
		t.Error("Pull never ran, want the full import on a full-history machine")
	}
}

// stampedRows snapshots the updated_at of every row of table on path, so a
// case pins that a second attempt wrote nothing there.
func stampedRows(t *testing.T, path, table string) map[string]string {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	rows, err := raw.Query(`SELECT name, updated_at FROM ` + table)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, at string
		if err := rows.Scan(&name, &at); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out[name] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return out
}

func equalRows(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestSteadyNoneSecondAttemptWritesNothing pins the no-churn contract: with
// unchanged mirrored documents a second attempt leaves the outbox empty,
// exports nothing, applies nothing again, and moves no stamp. A server that
// re-applied its documents as local writes every tick would re-export them
// every tick, the churn the traffic study fixed.
func TestSteadyNoneSecondAttemptWritesNothing(t *testing.T) {
	shared, local, config, secrets, log := noneFixture(t, HistoryNone)
	seedMirrorEntry(t, shared, log.MemTransport, []byte("0123456789abcdef0123456789abcdef"), secrets, "m2", "r2-endpoint", `"https://r2.invalid"`)

	runner := &Runner{Client: log, Local: local}
	first := runner.SyncOnce(context.Background(), shared)
	if first.Err != nil {
		t.Fatalf("first SyncOnce: %v", first.Err)
	}
	if len(config.items) != 1 || len(secrets.items) != 1 {
		t.Fatalf("mirrored items = %d config, %d secrets, want one of each", len(config.items), len(secrets.items))
	}
	applies := config.applies + secrets.applies
	configRows := stampedRows(t, local.Path(), "config_doc")
	secretRows := stampedRows(t, local.Path(), "secret")
	peer, ok := installationByID(t, shared, "m2")
	if !ok {
		t.Fatal("no directory row for the peer after the first attempt")
	}

	second := runner.SyncOnce(context.Background(), shared)
	if second.Err != nil {
		t.Fatalf("second SyncOnce: %v", second.Err)
	}
	if second.Exported != 0 {
		t.Errorf("second Exported = %d, want nothing re-exported", second.Exported)
	}
	if got := config.applies + secrets.applies; got != applies {
		t.Errorf("applies moved %d -> %d, want no second application", applies, got)
	}
	if got := stampedRows(t, local.Path(), "config_doc"); !equalRows(got, configRows) {
		t.Errorf("config_doc stamps moved: before %v, after %v", configRows, got)
	}
	if got := stampedRows(t, local.Path(), "secret"); !equalRows(got, secretRows) {
		t.Errorf("secret stamps moved: before %v, after %v", secretRows, got)
	}
	if again, ok := installationByID(t, shared, "m2"); !ok {
		t.Error("the peer directory row is gone after the second attempt")
	} else if !again.LastSeen.Equal(peer.LastSeen) {
		t.Errorf("peer last_seen moved %v -> %v, want the first attempt's stamp", peer.LastSeen, again.LastSeen)
	}
	drained, err := shared.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if len(drained) != 0 {
		t.Errorf("outbox holds %d rows, want empty", len(drained))
	}
}

// onePerPageLog is the log with PullRange cut to one entry per page, which is
// what a large fleet looks like to a ranged read: many small pages.
type onePerPageLog struct {
	*synclog.MemTransport
	ranges atomic.Int64
}

func (l *onePerPageLog) PullRange(origin string, after, upto int) ([]synclog.Entry, error) {
	l.ranges.Add(1)
	page, err := l.MemTransport.PullRange(origin, after, upto)
	if err != nil || len(page) <= 1 {
		return page, err
	}
	return page[:1], nil
}

// TestPrepassMirroredPagesToTheHead pins the ranging loop: five entries one
// page at a time all apply, and the loop ends because the cursor advanced
// rather than because an answer came back short. A repeat pass counts the
// same five redeliveries, which is why the callers report no applied count.
func TestPrepassMirroredPagesToTheHead(t *testing.T) {
	shared, local, config, secrets, log := noneFixture(t, HistoryNone)
	key := []byte("0123456789abcdef0123456789abcdef")
	seedMirrorEntry(t, shared, log.MemTransport, key, config, "m2", "b", `{"b":1}`)
	seedMirrorEntry(t, shared, log.MemTransport, key, config, "m2", "c", `{"c":1}`)
	seedMirrorEntry(t, shared, log.MemTransport, key, secrets, "m2", "s1", `"one"`)
	seedMirrorEntry(t, shared, log.MemTransport, key, secrets, "m2", "s2", `"two"`)

	paged := &onePerPageLog{MemTransport: log.MemTransport}
	first, err := PrepassMirrored(shared, local, paged)
	if err != nil {
		t.Fatalf("PrepassMirrored: %v", err)
	}
	if first != 5 {
		t.Errorf("first pass applied %d, want the five entries", first)
	}
	if len(config.items) != 3 || len(secrets.items) != 2 {
		t.Errorf("mirrored items = %d config, %d secrets, want 3 and 2", len(config.items), len(secrets.items))
	}
	if got := paged.ranges.Load(); got != 5 {
		t.Errorf("PullRange ran %d times, want one page per entry", got)
	}
	again, err := PrepassMirrored(shared, local, paged)
	if err != nil {
		t.Fatalf("second PrepassMirrored: %v", err)
	}
	if again != 5 {
		t.Errorf("second pass counted %d, want the same five redeliveries", again)
	}
}

package store

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// liveBinding returns a binding whose every overlay field carries a distinct
// non-zero value, so a strip that missed one would show.
func liveBinding() Binding {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	b := newBinding("api", "/repo/a")
	b.Builder.StreamRound = 7
	b.Builder.StreamOffset = 9000
	b.Builder.StreamStart = 800
	b.Builder.StreamSegments = []StreamSegment{{Start: 800, Kind: "opencode"}}
	b.Builder.RemoteLive = &LiveFacts{At: now, PID: 4711, Tail: []string{"one", "two"}}
	b.Progress = &Progress{
		SampledAt:  now,
		Tree:       "treefp",
		TreeAt:     now.Add(-time.Minute),
		OutputAt:   now.Add(-2 * time.Second),
		ResumedAt:  now.Add(-3 * time.Minute),
		CPUTicks:   123,
		CPUTicksAt: now.Add(-time.Second),
	}
	return b
}

// strippedKeys are the record_json paths that must never survive a save.
var strippedKeys = []string{"stream_offset", "remote_live", "sampled_at", "output_at", "cpu_ticks", "cpu_ticks_at"}

func assertNoStrippedKeys(t *testing.T, raw []byte, where string) {
	t.Helper()
	doc := string(raw)
	for _, key := range strippedKeys {
		if strings.Contains(doc, `"`+key+`"`) {
			t.Errorf("%s holds stripped key %q: %s", where, key, doc)
		}
	}
}

// TestLiveOverlayStripMergeRoundTrip is the step-1 unit: the stripped binding
// serialises without any overlay key, the merge restores the binding exactly,
// and neither helper touches the caller's binding.
func TestLiveOverlayStripMergeRoundTrip(t *testing.T) {
	b := liveBinding()
	stripped, ov := stripLive(b)

	raw, err := strippedJSON(b)
	if err != nil {
		t.Fatalf("strippedJSON: %v", err)
	}
	assertNoStrippedKeys(t, raw, "stripped JSON")

	// The synced companions of the stripped fields stay: the round the cursor
	// keys on, the segments that render it, and the labels read from the row.
	doc := string(raw)
	for _, keep := range []string{`"stream_round"`, `"stream_start"`, `"stream_segments"`, `"tree"`, `"tree_at"`, `"resumed_at"`} {
		if !strings.Contains(doc, keep) {
			t.Errorf("stripped JSON lost synced key %s: %s", keep, doc)
		}
	}

	if merged := mergeLive(stripped, ov); !SameBinding(b, merged) {
		t.Errorf("merge did not restore the binding exactly")
	}

	// stripLive must not reach through the pointers it copies.
	if b.Builder.StreamOffset != 9000 || b.Builder.RemoteLive == nil ||
		b.Progress == nil || b.Progress.SampledAt.IsZero() || b.Progress.CPUTicks != 123 {
		t.Errorf("stripLive mutated the caller's binding")
	}

	// The round-trip over the whole store: a Save and a Load cross the
	// strip/merge seam, so a read path that skips the overlay loses the
	// cursor no matter how the pure helpers behave.
	s := New(t.TempDir())
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.StreamOffset != 9000 || !reflect.DeepEqual(got.Builder.RemoteLive, b.Builder.RemoteLive) {
		t.Errorf("store round-trip lost live fields: %+v", got.Builder)
	}
	if got.Progress == nil || !got.Progress.SampledAt.Equal(b.Progress.SampledAt) ||
		got.Progress.CPUTicks != b.Progress.CPUTicks || !got.Progress.CPUTicksAt.Equal(b.Progress.CPUTicksAt) ||
		!got.Progress.OutputAt.Equal(b.Progress.OutputAt) {
		t.Errorf("store round-trip lost progress clocks: %+v", got.Progress)
	}
}

// TestSaveStripsLiveFieldsAndLoadMergesThem is the step-2 seam: Save then
// Load returns the full binding while the stored record_json holds none of
// the stripped keys (asserted on the raw JSON).
func TestSaveStripsLiveFieldsAndLoadMergesThem(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	assertNoStrippedKeys(t, bindingRecordJSON(t, s, "api"), "record_json")

	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := liveBinding()
	if got.Builder.StreamOffset != want.Builder.StreamOffset {
		t.Errorf("stream offset: got %d want %d", got.Builder.StreamOffset, want.Builder.StreamOffset)
	}
	if !reflect.DeepEqual(got.Builder.RemoteLive, want.Builder.RemoteLive) {
		t.Errorf("remote live: got %+v want %+v", got.Builder.RemoteLive, want.Builder.RemoteLive)
	}
	if got.Progress == nil || want.Progress == nil {
		t.Fatalf("progress object: got %v want %v", got.Progress, want.Progress)
	}
	if !got.Progress.SampledAt.Equal(want.Progress.SampledAt) ||
		!got.Progress.OutputAt.Equal(want.Progress.OutputAt) ||
		got.Progress.CPUTicks != want.Progress.CPUTicks ||
		!got.Progress.CPUTicksAt.Equal(want.Progress.CPUTicksAt) {
		t.Errorf("progress clocks: got %+v want %+v", got.Progress, want.Progress)
	}
	if got.Progress.Tree != want.Progress.Tree || !got.Progress.TreeAt.Equal(want.Progress.TreeAt) {
		t.Errorf("synced progress labels lost: got %+v", got.Progress)
	}

	// List merges too (decodeRecords is the other choke point).
	listed, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Builder.StreamOffset != 9000 || listed[0].Builder.RemoteLive == nil {
		t.Errorf("List did not merge the overlay: %+v", listed)
	}
}

// cursorTick advances only overlay fields and saves through the public API.
func cursorTick(t *testing.T, s *Store, offset int64) {
	t.Helper()
	b, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamOffset = offset
	b.Progress.SampledAt = b.Progress.SampledAt.Add(30 * time.Second)
	b.Progress.OutputAt = b.Progress.OutputAt.Add(30 * time.Second)
	if err := s.Save(b); err != nil {
		t.Fatalf("Save cursor %d: %v", offset, err)
	}
}

// outboxRecordRows counts the binding_record entries waiting in the shared
// outbox. Reading the log does not drain it.
func outboxRecordRows(t *testing.T, d *db.DB) []db.DrainedEntry {
	t.Helper()
	entries, err := d.DrainOutbox(1000)
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	var rows []db.DrainedEntry
	for _, e := range entries {
		if e.Table == "binding_record" {
			rows = append(rows, e)
		}
	}
	return rows
}

// TestOverlayTickQueuesNoOutboxRows pins the per-round cost this whole change
// exists for: N saves that move only overlay fields queue zero
// binding_record outbox rows and rewrite no shared row; one save that moves a
// synced field queues exactly one.
func TestOverlayTickQueuesNoOutboxRows(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	for i, off := range []int64{1000, 2000, 3000, 4000, 5000} {
		cursorTick(t, s, off)
		if rows := outboxRecordRows(t, d); len(rows) != 0 {
			t.Fatalf("cursor-only save %d queued %d binding_record rows", i+1, len(rows))
		}
	}

	// The overlay holds the latest cursor and the next load resumes from it.
	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.StreamOffset != 5000 {
		t.Errorf("overlay cursor: got %d want 5000", got.Builder.StreamOffset)
	}

	// A real transition still writes exactly one shared row.
	before, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	before.Halt = "needs a human"
	before.State = StateNeedsYou
	if err := s.Save(before); err != nil {
		t.Fatalf("Save transition: %v", err)
	}
	if rows := outboxRecordRows(t, d); len(rows) != 1 {
		t.Fatalf("real transition queued %d binding_record rows, want exactly 1", len(rows))
	}
	// ...and the overlay stayed current through it (case 2).
	got, err = s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.StreamOffset != 5000 {
		t.Errorf("overlay after transition: got %d want 5000", got.Builder.StreamOffset)
	}
}

// TestSaveWithOverlayVerdict is the write-through gate flag: an overlay-only
// change persists locally and reports the shared row unchanged; a synced
// change reports it changed.
func TestSaveWithOverlayVerdict(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	changed := func(mutate func(*Binding)) bool {
		t.Helper()
		b, err := s.Load("api")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		mutate(&b)
		var out bool
		if err := s.WithLock(func(tx *Tx) error {
			var err error
			out, err = tx.SaveWithOverlay(b)
			return err
		}); err != nil {
			t.Fatalf("SaveWithOverlay: %v", err)
		}
		return out
	}

	if changed(func(b *Binding) { b.Builder.StreamOffset = 6000 }) {
		t.Errorf("cursor-only save reported the shared row changed")
	}
	if got, err := s.Load("api"); err != nil || got.Builder.StreamOffset != 6000 {
		t.Errorf("cursor-only save did not persist the overlay: got %d err %v", got.Builder.StreamOffset, err)
	}
	if !changed(func(b *Binding) { b.Halt = "halted" }) {
		t.Errorf("synced-field save reported the shared row unchanged")
	}
}

// TestOverlayDeletedOnRemoveAndRebind pins case 5 across incarnations: a name
// deleted and re-added reads no stale cursor, and the dead row's key is gone.
func TestOverlayDeletedOnRemoveAndRebind(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	oldKey := liveKey(rec.ID)
	if _, ok, _ := d.LocalOrSelf().KVGet(oldKey); !ok {
		t.Fatalf("overlay key missing after the initial save")
	}

	if err := s.Delete("api"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := d.LocalOrSelf().KVGet(oldKey); ok {
		t.Errorf("delete left the overlay key behind")
	}

	fresh := newBinding("api", "/repo/b")
	fresh.Builder.StreamRound = 1
	if err := s.Save(fresh); err != nil {
		t.Fatalf("rebind Save: %v", err)
	}
	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load after rebind: %v", err)
	}
	if got.Builder.StreamOffset != 0 || got.Builder.RemoteLive != nil {
		t.Errorf("stale cursor leaked across incarnations: offset %d live %+v", got.Builder.StreamOffset, got.Builder.RemoteLive)
	}
}

// TestOverlayDeletedOnArchive drops the key on the archive path too.
func TestOverlayDeletedOnArchive(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	if _, err := s.Archive("api"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, ok, _ := d.LocalOrSelf().KVGet(liveKey(rec.ID)); ok {
		t.Errorf("archive left the overlay key behind")
	}
}

// seedPreOverlayRow rewrites the stored row the way a pre-overlay binary
// would -- full JSON, live fields included -- and drops the overlay, so the
// next reads see exactly an old row on an old machine.
func seedPreOverlayRow(t *testing.T, s *Store, b Binding) db.Record {
	t.Helper()
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	rec, ok, err := d.RecordGet(s.owner, "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	full, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	old := rec
	old.ID = ""
	old.JSON = string(full)
	if _, err := d.RecordPut(old); err != nil {
		t.Fatalf("RecordPut legacy row: %v", err)
	}
	if err := d.LocalOrSelf().KVDelete(liveKey(rec.ID)); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}
	return rec
}

// TestOverlaySeedsFromSyncedRow covers old rows and the downgrade window: a
// record written with the live fields in its JSON (by a binary before this
// change) reads whole from its own synced values, migrates its keys into the
// overlay on the first save, and stays quiet after that.
func TestOverlaySeedsFromSyncedRow(t *testing.T) {
	s := New(t.TempDir())
	b := liveBinding()
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rec := seedPreOverlayRow(t, s, b)

	seeded, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if seeded.Builder.StreamOffset != 9000 || seeded.Builder.RemoteLive == nil {
		t.Fatalf("old row did not seed from its synced values: %+v", seeded.Builder)
	}

	// A cursor tick on the seeded binding migrates the row once -- the keys
	// leave the JSON for the overlay -- and queues nothing after that. The
	// overlay carries the latest cursor from the first save.
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	cursorTick(t, s, 9500)
	if rows := outboxRecordRows(t, d); len(rows) != 1 {
		t.Errorf("seeding migration queued %d rows, want exactly 1", len(rows))
	}
	assertNoStrippedKeys(t, bindingRecordJSON(t, s, "api"), "record_json after seeded tick")
	ov, ok, err := s.readLiveOverlay(rec.ID)
	if err != nil || !ok {
		t.Fatalf("overlay after seeded tick: ok=%v err=%v", ok, err)
	}
	if ov.StreamOffset != 9500 {
		t.Errorf("overlay cursor after seeded tick: got %d want 9500", ov.StreamOffset)
	}
	cursorTick(t, s, 9800)
	if rows := outboxRecordRows(t, d); len(rows) != 1 {
		t.Errorf("steady cursor ticks after migration queued %d rows, want still 1", len(rows))
	}

	// A real transition rewrites the row and keeps the overlay current.
	next, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	next.Halt = "halted"
	if err := s.Save(next); err != nil {
		t.Fatalf("Save transition: %v", err)
	}
	assertNoStrippedKeys(t, bindingRecordJSON(t, s, "api"), "record_json after transition")
	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.StreamOffset != 9800 {
		t.Errorf("cursor lost across the rewriting transition: got %d", got.Builder.StreamOffset)
	}
}

// TestSaveWithLogKeepsOverlayCurrent covers the other RecordPut writer: a
// save-with-log with advanced live fields leaves a stripped row and a current
// overlay.
func TestSaveWithLogKeepsOverlayCurrent(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Builder.StreamOffset = 11000
	err = s.WithLock(func(tx *Tx) error {
		return tx.SaveWithLog(b, LogEntry{TS: time.Now().UTC(), Round: 7, Direction: DirToBuilder, Kind: KindPrompt, Note: "go"})
	})
	if err != nil {
		t.Fatalf("SaveWithLog: %v", err)
	}
	assertNoStrippedKeys(t, bindingRecordJSON(t, s, "api"), "record_json after SaveWithLog")
	got, err := s.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Builder.StreamOffset != 11000 {
		t.Errorf("SaveWithLog lost the cursor: got %d", got.Builder.StreamOffset)
	}
}

// TestLiveKeyScopedPerStoreRoot documents the key's home: a machine-local kv
// row under a namespace the export never reads.
func TestLiveKeyScopedPerStoreRoot(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(liveBinding()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	keys, err := d.LocalOrSelf().KVKeys("live/")
	if err != nil {
		t.Fatalf("KVKeys: %v", err)
	}
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "live/") {
		t.Fatalf("overlay keys on the local surface: %v", keys)
	}
	// The shared file carries none of it: an export reads only the shared
	// handle's outbox, and the shared file's kv table holds no live key.
	sharedKeys, err := d.KVKeys("live/")
	if err != nil {
		t.Fatalf("shared KVKeys: %v", err)
	}
	if len(sharedKeys) != 0 {
		t.Errorf("live overlay row found on the shared handle: %v", sharedKeys)
	}
}

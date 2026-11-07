package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// liveOverlay holds the binding fields that move every tick while a round
// runs: the stream cursor, the progress sampling clocks, the client's copy of
// a served round's live view, and this machine's last word on reaching the
// server. They live in a machine-local kv blob
// keyed by binding_record id instead of the synced record_json, because every
// rewrite of the synced row is an outbox entry every machine pulls, and a
// tick that moves only these fields changes no synced fact. On save they are
// stripped from record_json; on load they are merged back, so callers keep
// reading store.Binding whole.
//
// The overlay wins when it is present; a binding row with no overlay (an old
// record, a fresh machine) seeds from the synced row itself, which still
// carries the values until its next real save strips them.
type liveOverlay struct {
	// StreamOffset is the round file's render cursor. It pairs with the
	// synced runner.stream_round: that moves only at a round start or
	// switch, which are synced writes, so a save that moves the cursor
	// cannot leave the synced round behind it.
	StreamOffset int64 `json:"stream_offset,omitempty"`
	// RemoteLive is the whole server-view copy of a running served round,
	// rewritten on every poll with the server's current numbers.
	RemoteLive *LiveFacts `json:"remote_live,omitempty"`
	// RemoteStatus and RemoteUnreachableSince say how this machine's last poll
	// went. One blip on the link flips every served binding at once, and a peer
	// reaches the server itself when it takes a binding over.
	RemoteStatus           string    `json:"remote_status,omitempty"`
	RemoteUnreachableSince time.Time `json:"remote_unreachable_since,omitzero"`
	// ViewedAt is when this machine last displayed the binding's newest report.
	// A read stamps only this machine's view, and every rewrite of the synced
	// row is an outbox entry every machine pulls, so the stamp stays local.
	ViewedAt time.Time `json:"viewed_at,omitzero"`
	// Clocks are the per-sample progress clocks; nil when the binding has
	// no Progress object at all.
	Clocks *overlayClocks `json:"clocks,omitempty"`
}

// overlayClocks mirrors Progress's per-sample fields. Tree, TreeAt and
// ResumedAt stay synced: the fingerprint moves only on a real edit, and a
// resume is a transition.
type overlayClocks struct {
	SampledAt  time.Time `json:"sampled_at,omitzero"`
	OutputAt   time.Time `json:"output_at,omitzero"`
	CPUTicks   uint64    `json:"cpu_ticks,omitempty"`
	CPUTicksAt time.Time `json:"cpu_ticks_at,omitzero"`
}

func (ov liveOverlay) isZero() bool {
	return ov.StreamOffset == 0 && ov.RemoteLive == nil && ov.Clocks == nil &&
		ov.RemoteStatus == "" && ov.RemoteUnreachableSince.IsZero() && ov.ViewedAt.IsZero()
}

// liveKey is the machine-local kv key of one binding's overlay. The key is
// the binding_record id, not the name: a name deleted and re-added takes a
// fresh id, so a new incarnation can never read the old incarnation's cursor
// even if a delete crashed between the two removals.
func liveKey(recordID string) string { return "live/" + recordID }

// stripLive returns b with the overlay fields removed and the overlay those
// fields carried. The caller's binding is not changed: Progress is a pointer,
// so it is cloned before its clocks are zeroed.
func stripLive(b Binding) (Binding, liveOverlay) {
	ov := liveOverlay{
		StreamOffset: b.Builder.StreamOffset,
		RemoteLive:   b.Builder.RemoteLive,

		RemoteStatus:           b.Builder.RemoteStatus,
		RemoteUnreachableSince: b.RemoteUnreachableSince,
	}
	if b.ViewedAt != nil {
		ov.ViewedAt = *b.ViewedAt
	}
	if p := b.Progress; p != nil {
		ov.Clocks = &overlayClocks{
			SampledAt:  p.SampledAt,
			OutputAt:   p.OutputAt,
			CPUTicks:   p.CPUTicks,
			CPUTicksAt: p.CPUTicksAt,
		}
		c := *p
		c.SampledAt, c.OutputAt, c.CPUTicks, c.CPUTicksAt = time.Time{}, time.Time{}, 0, time.Time{}
		b.Progress = &c
	}
	b.Builder.StreamOffset = 0
	b.Builder.RemoteLive = nil
	b.Builder.RemoteStatus = ""
	b.RemoteUnreachableSince = time.Time{}
	b.ViewedAt = nil
	return b, ov
}

// mergeLive puts a present overlay back onto a synced binding. Overlay values
// win unconditionally: a written overlay always describes the last save on
// this machine, and a synced row never carries these values after that.
func mergeLive(b Binding, ov liveOverlay) Binding {
	b.Builder.StreamOffset = ov.StreamOffset
	if ov.RemoteLive != nil {
		b.Builder.RemoteLive = ov.RemoteLive
	}
	// An overlay written before these fields moved is silent about them while its
	// row still holds them, and a row that has moved them holds nothing to protect.
	if ov.RemoteStatus != "" {
		b.Builder.RemoteStatus = ov.RemoteStatus
	}
	if !ov.RemoteUnreachableSince.IsZero() {
		b.RemoteUnreachableSince = ov.RemoteUnreachableSince
	}
	if !ov.ViewedAt.IsZero() {
		at := ov.ViewedAt
		b.ViewedAt = &at
	}
	if c := ov.Clocks; c != nil {
		if b.Progress == nil {
			b.Progress = &Progress{}
		}
		p := *b.Progress
		p.SampledAt, p.OutputAt, p.CPUTicks, p.CPUTicksAt = c.SampledAt, c.OutputAt, c.CPUTicks, c.CPUTicksAt
		b.Progress = &p
	}
	return b
}

// SameBindingSynced reports whether two bindings hold the same synced state:
// SameBinding narrowed to the fields the shared row carries. A tick that
// moved only overlay fields is "the same" here, which is what lets a gate
// skip a save, an emit and an outbox row for it.
func SameBindingSynced(a, b Binding) bool {
	sa, _ := stripLive(a)
	sb, _ := stripLive(b)
	return SameBinding(sa, sb)
}

// syncedUnchanged reports whether a save may skip the shared row, comparing the
// caller's binding as SameBindingSynced does: every shared-row write from this
// binary's save paths is produced by strippedJSON, so a row they wrote compares
// against itself exactly. A row that predates the overlay (a legacy record
// carrying live keys) compares different and migrates on its next save. A stored
// row that does not decode is not unchanged: the full save path validates and
// refuses it.
func syncedUnchanged(rawJSON string, b Binding) bool {
	strip, err := strippedJSON(b)
	if err != nil {
		return false
	}
	return bytes.Equal(strip, []byte(rawJSON))
}

// strippedJSON marshals b and physically removes the overlay keys from the
// document: the shared row carries neither a live value nor a zero stand-in
// for one, so an absent key decodes to the zero and no format bump is needed.
// The removals re-sort the objects they touch (marshal of a parsed map orders
// keys), which is the point of comparing the stored row against this
// function's own output: every shared-row write in the new path produces this
// shape, and a row that predates it migrates with its next save.
func strippedJSON(b Binding) ([]byte, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	dropFrom(doc, "runner", "stream_offset", "remote_live", "remote_status")
	delete(doc, "remote_unreachable_since")
	dropFrom(doc, "progress", "sampled_at", "output_at", "cpu_ticks", "cpu_ticks_at")
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dropFrom removes the named keys from the sub-object stored under section,
// doing nothing when the section itself is absent.
func dropFrom(doc map[string]json.RawMessage, section string, keys ...string) {
	sub, ok := doc[section]
	if !ok {
		return
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(sub, &m); err != nil {
		return
	}
	for _, k := range keys {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return
	}
	doc[section] = out
}

// readLiveOverlay returns the stored overlay for one record id. A missing
// database or key is (zero, false, nil): the binding reads from its synced
// row alone. A malformed blob warns and reads as absent -- the next save
// overwrites it -- because losing the live fields costs at most a re-render,
// while failing every read of the binding would cost the daemon its tick.
func (s *Store) readLiveOverlay(recordID string) (liveOverlay, bool, error) {
	if recordID == "" {
		return liveOverlay{}, false, nil
	}
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return liveOverlay{}, false, err
	}
	raw, ok, err := d.LocalOrSelf().KVGet(liveKey(recordID))
	if err != nil {
		return liveOverlay{}, false, fmt.Errorf("read live overlay %q: %w", recordID, err)
	}
	if !ok {
		return liveOverlay{}, false, nil
	}
	var ov liveOverlay
	if err := json.Unmarshal(raw, &ov); err != nil {
		slog.Warn("live overlay malformed, reading synced row", "record", recordID, "err", err)
		return liveOverlay{}, false, nil
	}
	return ov, true, nil
}

// writeLiveOverlay persists b's overlay under recordID only when it differs
// from the stored one, so a no-op tick writes nothing, local or shared. It
// runs before the shared row moves: a crash between the two costs at most a
// re-render of the unflushed tail, the same window the cursor-in-shared-row
// crash always had, and never a shared row whose cursor ran backwards past
// lines already rendered into this machine's log.
func (s *Store) writeLiveOverlay(recordID string, b Binding) error {
	if recordID == "" {
		return nil
	}
	_, ov := stripLive(b)
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	local := d.LocalOrSelf()
	key := liveKey(recordID)
	raw, ok, err := local.KVGet(key)
	if err != nil {
		return fmt.Errorf("read live overlay %q: %w", recordID, err)
	}
	if ov.isZero() {
		if ok {
			return local.KVDelete(key)
		}
		return nil
	}
	val, err := json.Marshal(ov)
	if err != nil {
		return fmt.Errorf("marshal live overlay %q: %w", recordID, err)
	}
	if ok && bytes.Equal(raw, val) {
		return nil
	}
	return local.KVPut(key, val)
}

// writeViewedOverlay stamps only the viewed field of one binding's overlay,
// keeping every sibling field the stored overlay already holds. A missing or
// malformed stored overlay reads as empty, the same rule readLiveOverlay
// uses, so a stamp never fails over a blob the next save would overwrite.
func (s *Store) writeViewedOverlay(recordID string, at time.Time) error {
	if recordID == "" {
		return nil
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	local := d.LocalOrSelf()
	key := liveKey(recordID)
	var ov liveOverlay
	if raw, ok, err := local.KVGet(key); err != nil {
		return fmt.Errorf("read live overlay %q: %w", recordID, err)
	} else if ok {
		if err := json.Unmarshal(raw, &ov); err != nil {
			slog.Warn("live overlay malformed, reading synced row", "record", recordID, "err", err)
			ov = liveOverlay{}
		}
	}
	ov.ViewedAt = at
	val, err := json.Marshal(ov)
	if err != nil {
		return fmt.Errorf("marshal live overlay %q: %w", recordID, err)
	}
	return local.KVPut(key, val)
}

// deleteLiveOverlay drops the overlay of a binding being archived or deleted,
// the lifecycle half of the case-5 guarantee; the record-id key already
// stops a stale cursor reaching a re-added name.
func (s *Store) deleteLiveOverlay(recordID string) error {
	if recordID == "" {
		return nil
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	return d.LocalOrSelf().KVDelete(liveKey(recordID))
}

// mergeLiveInto restores one binding's live fields for the read paths. An
// absent overlay leaves the row as decoded (the seed-from-synced rule).
func (s *Store) mergeLiveInto(b Binding, recordID string) (Binding, error) {
	ov, ok, err := s.readLiveOverlay(recordID)
	if err != nil || !ok {
		return b, err
	}
	return mergeLive(b, ov), nil
}

// mergeLiveList merges each row's overlay into the binding decoded from it;
// bindings and recs are the same slice in the same order. The overlays arrive
// in one statement and are matched by record id, so a listing costs one kv
// read whatever the row count; a row with no overlay keeps its synced values.
func (s *Store) mergeLiveList(bindings []Binding, recs []db.Record) ([]Binding, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return bindings, nil
	}
	overlays, err := d.LocalOrSelf().KVScanLive()
	if err != nil {
		return nil, fmt.Errorf("read live overlays: %w", err)
	}
	for i := range bindings {
		if recs[i].ID == "" {
			continue
		}
		raw, ok := overlays[liveKey(recs[i].ID)]
		if !ok {
			continue
		}
		var ov liveOverlay
		if err := json.Unmarshal(raw, &ov); err != nil {
			slog.Warn("live overlay malformed, reading synced row", "record", recs[i].ID, "err", err)
			continue
		}
		bindings[i] = mergeLive(bindings[i], ov)
	}
	return bindings, nil
}

// saveLive writes a binding and reports whether the shared row changed.
//
// The synced row moves only when the stripped binding differs from the stored
// one, so a tick that advanced the cursor, a progress sample, and a served
// poll that re-delivered the same live view rewrite no binding_record and
// queue no outbox row -- the overlay alone absorbs them. The verdict is by
// content, not by the caller's belief: an equal row is skipped even by a save
// that would have stamped a new updated_at, which is the whole point.
func (s *Store) saveLive(b Binding) (bool, error) {
	d, err := s.dbForWrite()
	if err != nil {
		return false, err
	}
	rec, ok, err := d.RecordGet(s.owner, b.Name)
	if err != nil {
		return false, fmt.Errorf("read binding %q: %w", b.Name, err)
	}
	if ok && syncedUnchanged(rec.JSON, b) {
		if err := s.writeLiveOverlay(rec.ID, b); err != nil {
			return false, err
		}
		return false, nil
	}

	_, prepared, err := s.prepareSave(b, nil)
	if err != nil {
		return false, err
	}
	// An existing row keeps its id (RecordPut's UPDATE ignores the prepared
	// one), so the overlay is written under the id every later read keys on;
	// a create uses the id prepareSave minted for the row about to be born.
	id := prepared.ID
	if ok {
		id = rec.ID
	}
	if err := s.writeLiveOverlay(id, b); err != nil {
		return false, err
	}
	if _, err := d.RecordPut(prepared); err != nil {
		return false, fmt.Errorf("save binding %q: %w", b.Name, err)
	}
	return true, nil
}

// SaveWithOverlay is Save with the shared-row verdict: it reports whether the
// synced record changed, so a caller that saves on every tick can skip its
// emit and its count when only machine-local live fields moved.
func (t *Tx) SaveWithOverlay(b Binding) (bool, error) {
	return t.s.saveLive(b)
}

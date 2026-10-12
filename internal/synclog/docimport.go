package synclog

// The importer's doc path: doc entries leave a batch or a snapshot region
// before any row is applied, and are applied or parked in one transaction on the
// machine-local file. The rows then go in through the shared file's own
// transaction, which also moves the mark. The two files share no transaction,
// so the order is the safety: a crash between them redelivers the batch, and
// the doc half is idempotent.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TxDoc is a SharedDoc that can apply an item inside the importer's
// transaction, so the item, the revision it leaves and the applied state it is
// remembered by commit together or not at all.
type TxDoc interface {
	SharedDoc
	// ApplyIn writes one key's value, or removes the key when value is nil.
	// refusal is non-nil when the item cannot land now; nothing is recorded and
	// nothing changes, because the item may depend on a key still to come.
	ApplyIn(tx *db.Tx, key string, value json.RawMessage, label string) (refusal error, err error)
	// SupersedeIn records an item that lost, to what this machine holds or
	// because it could never land, and changes nothing.
	SupersedeIn(tx *db.Tx, key string, value json.RawMessage, label, why string) error
}

// TxWriteStamper is the optional half of a TxDoc that can say, inside the
// importer's transaction, when a key was last written on this machine.
type TxWriteStamper interface {
	ItemWriteStampIn(tx *db.Tx, key string) (stamp Stamp, ok bool, err error)
}

// docItem is a doc entry that passed the shape checks, with the entry it came
// from so a locked reader can park it whole.
type docItem struct {
	entry  Entry
	parsed DocEntry
	// label names the installation the entry came from in the revision it
	// leaves.
	label string
}

// splitDocs separates doc entries from row entries, keeping each group's order.
func splitDocs(entries []Entry) (docs, rows []Entry) {
	for _, e := range entries {
		if IsDocTable(e.Table) {
			docs = append(docs, e)
		} else {
			rows = append(rows, e)
		}
	}
	return docs, rows
}

// unknownDoc holds an origin whose entries name a document this build has no
// reader for, at seq: the peer is most likely on a newer binary, and dropping
// the batch would lose the rows beside the entry for good. A doc table naming
// no document at all stays a refusal, because no upgrade would give it a
// meaning.
func (i *Importer) unknownDoc(origin string, seq int, entries []Entry) *Hold {
	for _, e := range entries {
		name, ok := DocName(e.Table)
		if !ok || name == "" {
			continue
		}
		if _, known := i.docNamed(name); !known {
			return &Hold{Origin: origin, Label: i.labelOf(origin), Seq: seq, Reason: fmt.Sprintf(
				"%s writes the shared document %q, which this build does not know: upgrade this machine to follow it (mark rests at %d)",
				i.labelOf(origin), name, seq)}
		}
	}
	return nil
}

// parseDocs checks every doc entry before any is applied, so a malformed entry
// refuses the whole set and nothing lands half way.
func (k *keyed) parseDocs(entries []Entry) ([]docItem, error) {
	items := make([]docItem, 0, len(entries))
	for _, e := range entries {
		parsed, err := parseDocEntry(k.docFor, e)
		if err != nil {
			return nil, err
		}
		items = append(items, docItem{entry: e, parsed: parsed})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].entry.Seq < items[b].entry.Seq })
	return items, nil
}

// takeDocs applies a locked-or-unlocked reader's doc entries and reports how
// many it handled. An unlocked reader applies them; a locked one parks them,
// because it cannot tell an honest entry from a forged one yet.
//
// The clock observes every stamp first, applied, superseded or parked, and
// outside the transaction because it persists through its own.
func (i *Importer) takeDocs(origin string, docs []Entry) (int, error) {
	if len(docs) == 0 {
		return 0, nil
	}
	items, err := i.parseDocs(docs)
	if err != nil {
		return 0, err
	}
	if err := i.observe(items); err != nil {
		return 0, err
	}
	if len(i.key) > 0 {
		label := i.labelOf(origin)
		for n := range items {
			items[n].label = label
		}
	}
	err = i.db.LocalOrSelf().Tx(func(tx *db.Tx) error {
		if len(i.key) > 0 {
			return applyDocs(tx, i.db.Origin(), items)
		}
		for _, it := range items {
			if err := park(tx, it.entry); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("synclog: import docs from %s: %w", origin, err)
	}
	return len(items), nil
}

func (i *Importer) observe(items []docItem) error {
	clock := NewHybridClock(i.db.LocalOrSelf())
	for _, it := range items {
		if err := clock.Observe(it.parsed.Stamp); err != nil {
			return err
		}
	}
	return nil
}

func park(tx *db.Tx, e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("synclog: park %s %s: %w: %w", e.Table, e.PK, err, ErrInvalid)
	}
	return tx.ParkDoc(e.Origin, e.Seq, string(raw), time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
}

// applyDocs applies a set of verified entries in passes. Keys of one document
// can depend on each other -- an actor names an agent -- and the exporter sends
// them in key order, so an item refused for the state of the document is set
// aside and tried again after the rest. When a pass lands nothing new, what is
// left will never land and is recorded as superseded.
func applyDocs(tx *db.Tx, local string, items []docItem) error {
	left := items
	for len(left) > 0 {
		var next []docItem
		var whys []error
		for _, it := range left {
			refusal, err := applyDoc(tx, local, it)
			if err != nil {
				return err
			}
			if refusal != nil {
				next = append(next, it)
				whys = append(whys, refusal)
			}
		}
		if len(next) == len(left) {
			return supersedeRefused(tx, next, whys)
		}
		left = next
	}
	return nil
}

// supersedeRefused records the items no pass could land. The key's applied
// state still moves to the entry, so a redelivery is a no-op and an older
// entry still loses to it.
func supersedeRefused(tx *db.Tx, items []docItem, whys []error) error {
	for n, it := range items {
		p := it.parsed
		doc := p.Doc.(TxDoc) // applyDoc has already refused a doc that is not one
		if err := doc.SupersedeIn(tx, p.Key, docValue(p.Value), it.label, whys[n].Error()); err != nil {
			return err
		}
		if err := tx.SetDocApplied(p.Name, p.Key, p.Origin, p.Stamp.String()); err != nil {
			return err
		}
	}
	return nil
}

// applyDoc decides one verified doc entry against the newest state applied for
// its key. An entry equal to it is a redelivery and does nothing, which is what
// makes a crash between the two transactions harmless; a newer one is applied;
// an older one is recorded as superseded. A local write of the key that this
// machine has not exported yet counts as an entry of its own origin, so an
// arriving entry older than it is superseded too. refusal is the document
// declining a newer entry for now, with nothing recorded.
func applyDoc(tx *db.Tx, local string, it docItem) (refusal error, err error) {
	doc, ok := it.parsed.Doc.(TxDoc)
	if !ok {
		return nil, fmt.Errorf("synclog: doc %s cannot apply inside a transaction: %w", it.parsed.Name, ErrInvalid)
	}
	p := it.parsed
	cur, found, err := tx.DocApplied(p.Name, p.Key)
	if err != nil {
		return nil, err
	}
	value := docValue(p.Value)
	if found {
		order, err := compareApplied(cur, p)
		if err != nil {
			return nil, err
		}
		if order == 0 {
			return nil, nil
		}
		if order < 0 {
			return nil, doc.SupersedeIn(tx, p.Key, value, it.label,
				fmt.Sprintf("an entry stamped %s from %s is older than the one applied, stamped %s from %s",
					p.Stamp, p.Origin, cur.Stamp, cur.Origin))
		}
	}
	wrote, won, err := localWriteWins(tx, doc, local, p, cur, found)
	if err != nil {
		return nil, err
	}
	if won {
		why := fmt.Sprintf("an entry stamped %s from %s is older than a local write of the key, stamped %s",
			p.Stamp, p.Origin, wrote)
		if err := doc.SupersedeIn(tx, p.Key, value, it.label, why); err != nil {
			return nil, err
		}
		return nil, tx.SetDocApplied(p.Name, p.Key, p.Origin, p.Stamp.String())
	}
	refusal, err = doc.ApplyIn(tx, p.Key, value, it.label)
	if refusal != nil || err != nil {
		return refusal, err
	}
	if err := tx.SetDocApplied(p.Name, p.Key, p.Origin, p.Stamp.String()); err != nil {
		return nil, err
	}
	return nil, recordExported(tx, p, value)
}

// localWriteWins reports whether this machine's own write of the key outranks
// both the arriving entry and the applied state, in which case the arriving
// entry must not overwrite it. The write is stamped with this machine's origin,
// so ties break to the higher origin like any other pair of entries.
func localWriteWins(tx *db.Tx, doc TxDoc, local string, p DocEntry, cur db.DocApplied, found bool) (Stamp, bool, error) {
	w, ok := doc.(TxWriteStamper)
	if !ok {
		return 0, false, nil
	}
	wrote, has, err := w.ItemWriteStampIn(tx, p.Key)
	if err != nil || !has {
		return 0, false, err
	}
	if !stampBeats(wrote, local, p.Stamp, p.Origin) {
		return 0, false, nil
	}
	if found {
		have, err := ParseStamp(cur.Stamp)
		if err != nil {
			return 0, false, fmt.Errorf("synclog: applied stamp of %s %s: %w", p.Name, p.Key, err)
		}
		if !stampBeats(wrote, local, have, cur.Origin) {
			return 0, false, nil
		}
	}
	return wrote, true, nil
}

// stampBeats is whether (stamp, origin) is strictly newer than (other, otherOrigin).
func stampBeats(stamp Stamp, origin string, other Stamp, otherOrigin string) bool {
	return stamp > other || (stamp == other && origin > otherOrigin)
}

// recordExported leaves the export state holding what was just applied, so the
// exporter finds nothing to send back.
func recordExported(tx *db.Tx, p DocEntry, value json.RawMessage) error {
	if value == nil {
		return tx.ClearDocExported(p.Name, p.Key)
	}
	digest, err := ItemDigest(value)
	if err != nil {
		return err
	}
	return tx.SetDocExported(p.Name, p.Key, p.Origin, p.Stamp.String(), digest)
}

// compareApplied orders an arriving entry against the applied one by stamp and,
// on a tie, by origin, so two machines that stamped the same instant agree on
// the winner. It is zero for the same (stamp, origin).
func compareApplied(cur db.DocApplied, p DocEntry) (int, error) {
	have, err := ParseStamp(cur.Stamp)
	if err != nil {
		return 0, fmt.Errorf("synclog: applied stamp of %s %s: %w", p.Name, p.Key, err)
	}
	switch {
	case p.Stamp > have:
		return 1, nil
	case p.Stamp < have:
		return -1, nil
	case p.Origin > cur.Origin:
		return 1, nil
	case p.Origin < cur.Origin:
		return -1, nil
	}
	return 0, nil
}

// docValue turns the null a tombstone carries into the nil Apply takes.
func docValue(raw json.RawMessage) json.RawMessage {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}

// DrainPending applies the doc entries parked while this machine was locked,
// now that it holds a key. An entry that does not verify, or no longer parses,
// is refused and removed, so a forged entry parked earlier is never applied.
// The entries that verify go in together, so the keys of one document can
// depend on each other as they do in a batch.
func (i *Importer) DrainPending() (ImportResult, error) {
	var res ImportResult
	if len(i.key) == 0 {
		return res, nil
	}
	rows, err := i.db.PendingDocs()
	if err != nil {
		return res, fmt.Errorf("synclog: pending docs: %w", err)
	}
	var items []docItem
	var bad []db.PendingDoc
	for _, row := range rows {
		item, why := i.verifyParked(row)
		if why != "" {
			res.Dropped = append(res.Dropped, Drop{Origin: row.Origin, Seq: row.Seq, Reason: why})
			bad = append(bad, row)
			continue
		}
		item.label = i.labelOf(row.Origin)
		items = append(items, item)
	}
	if err := i.observe(items); err != nil {
		return res, err
	}
	err = i.db.LocalOrSelf().Tx(func(tx *db.Tx) error {
		if err := applyDocs(tx, i.db.Origin(), items); err != nil {
			return err
		}
		for _, it := range items {
			if err := tx.UnparkDoc(it.entry.Origin, it.entry.Seq); err != nil {
				return err
			}
		}
		for _, row := range bad {
			if err := tx.UnparkDoc(row.Origin, row.Seq); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("synclog: pending docs: %w", err)
	}
	res.Applied += len(items)
	return res, nil
}

// verifyParked reads a parked entry back and checks it as a keyed reader checks
// an arriving one. The reason is empty when the entry may be applied.
func (i *Importer) verifyParked(row db.PendingDoc) (docItem, string) {
	var e Entry
	if err := json.Unmarshal([]byte(row.Entry), &e); err != nil {
		return docItem{}, fmt.Sprintf("parked entry %d from %q cannot be read: %v", row.Seq, row.Origin, err)
	}
	if e.Origin != row.Origin || e.Seq != row.Seq {
		return docItem{}, fmt.Sprintf("parked entry %d from %q names another entry", row.Seq, row.Origin)
	}
	if len(e.MAC) == 0 || !VerifyMAC(i.key, e) {
		return docItem{}, fmt.Sprintf("parked entry %d from %q has no valid signature", row.Seq, row.Origin)
	}
	parsed, err := parseDocEntry(i.docFor, e)
	if err != nil {
		return docItem{}, fmt.Sprintf("parked entry %d from %q: %v", row.Seq, row.Origin, err)
	}
	return docItem{entry: e, parsed: parsed}, ""
}

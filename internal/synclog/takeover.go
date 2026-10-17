package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// CtlTakeover is the reserved table of the entry that moves a binding's writer
// without the old writer's consent.
const CtlTakeover = "ctl:takeover"

// IsCtlTable reports whether tbl names a control entry rather than a shared row.
func IsCtlTable(tbl string) bool { return strings.HasPrefix(tbl, "ctl:") }

// Takeover is the body of a ctl:takeover entry: the writer the binding left, the
// last of its entries that stays valid, and the machine that took it.
type Takeover struct {
	RecordID  string `json:"record_id"`
	BindingID string `json:"binding_id"`
	OldWriter string `json:"old_writer"`
	Upto      int    `json:"upto"`
	Taker     string `json:"taker"`
}

// TakeoverPK keys the entry by root and old writer, so a later takeover of the
// same pair supersedes an earlier one in compaction and the fence only rises.
func TakeoverPK(recordID, oldWriter string) string {
	raw, _ := json.Marshal([]string{recordID, oldWriter})
	return string(raw)
}

// NewTakeover builds the takeover entry. After is the old writer's entry the
// fence names, so no importer flips the writer before applying through it.
func NewTakeover(origin string, t Takeover, schemaVersion int, at time.Time) (Entry, error) {
	if t.RecordID == "" || t.OldWriter == "" || t.Taker == "" || t.Upto < 0 || t.Taker != origin || t.Taker == t.OldWriter {
		return Entry{}, fmt.Errorf("synclog: takeover body is incomplete or not the taker's own: %w", ErrInvalid)
	}
	body, err := json.Marshal(t)
	if err != nil {
		return Entry{}, fmt.Errorf("synclog: takeover body: %w: %w", err, ErrInvalid)
	}
	e := Entry{Origin: origin, Table: CtlTakeover, PK: TakeoverPK(t.RecordID, t.OldWriter), Op: OpUpsert, SchemaVersion: schemaVersion, Body: body, At: at}
	if t.Upto > 0 {
		e.After = &Ref{Origin: t.OldWriter, Seq: t.Upto}
	}
	if err := checkShape(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// DecodeTakeover reads a takeover body and checks it against its entry.
func DecodeTakeover(e Entry) (Takeover, error) {
	var t Takeover
	if e.Table != CtlTakeover || e.Op != OpUpsert {
		return t, fmt.Errorf("synclog: %s %s is not a takeover: %w", e.Table, e.PK, ErrInvalid)
	}
	if err := json.Unmarshal(e.Body, &t); err != nil {
		return t, fmt.Errorf("synclog: takeover body: %w: %w", err, ErrInvalid)
	}
	if t.RecordID == "" || t.OldWriter == "" || t.Taker == "" || t.Upto < 0 || t.Taker != e.Origin || t.Taker == t.OldWriter || e.PK != TakeoverPK(t.RecordID, t.OldWriter) {
		return t, fmt.Errorf("synclog: takeover %s from %q is malformed: %w", e.PK, e.Origin, ErrInvalid)
	}
	return t, nil
}

// applyTakeover writes a takeover entry's effect on one importer: both roots
// flip to the taker, the old writer's term is raised to the fence, a taker that
// had been fenced earlier keeps that term and records this entry as its regrant,
// and, when this installation is the old writer, the root is recorded as left.
// Whatever the flip queued for this installation is discarded: an import never
// exports, and a fenced change is only one the exporter drops later.
func applyTakeover(tx *db.Tx, e Entry, transport LogTransport, now time.Time) error {
	t, err := DecodeTakeover(e)
	if err != nil {
		return err
	}
	if e.After == nil || e.After.Origin != t.OldWriter || e.After.Seq != t.Upto {
		if t.Upto > 0 {
			return fmt.Errorf("synclog: takeover %s names a fence its After does not: %w", e.PK, ErrInvalid)
		}
	}
	since, err := tx.MaxOutboxSeq()
	if err != nil {
		return err
	}
	roots := []struct{ table, id string }{{"binding_record", t.RecordID}, {"binding", t.BindingID}}
	for _, r := range roots {
		if r.id == "" {
			continue
		}
		if _, err := tx.RecordLeaveTakeover(r.table, r.id, t.Taker, t.OldWriter, t.Upto, now); err != nil {
			return err
		}
		if _, _, _, had, err := tx.Term(r.id, t.Taker); err != nil {
			return err
		} else if had {
			if err := tx.SetRegrant(r.id, t.Taker, e.Origin, e.Seq); err != nil {
				return err
			}
		}
		if err := tx.DiscardOutboxSince(r.table, `["`+r.id+`"]`, since); err != nil {
			return err
		}
	}
	if t.OldWriter == tx.Origin() && transport != nil {
		if err := recordOwnFencedEntries(tx, t, transport, now); err != nil {
			return err
		}
	}
	return nil
}

// recordOwnFencedEntries records entries this origin wrote for the taken-over binding
// above the fence (upto) in sync_fenced_change.
func recordOwnFencedEntries(tx *db.Tx, t Takeover, transport LogTransport, now time.Time) error {
	state, err := transport.ReadMarks()
	if err != nil {
		return err
	}
	head := state.Heads[t.OldWriter]
	if head <= t.Upto {
		return nil
	}
	cursor := t.Upto
	for cursor < head {
		page, err := transport.PullRange(t.OldWriter, cursor, head)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		advanced := cursor
		for _, ownEntry := range page {
			if ownEntry.Origin != t.OldWriter || ownEntry.Seq <= cursor || ownEntry.Seq > head {
				continue
			}
			advanced = max(advanced, ownEntry.Seq)
			if err := recordFencedEntry(tx, t, ownEntry, now); err != nil {
				return err
			}
		}
		if advanced == cursor {
			break
		}
		cursor = advanced
	}
	return nil
}

func recordFencedEntry(tx *db.Tx, t Takeover, ownEntry Entry, now time.Time) error {
	if IsCtlTable(ownEntry.Table) {
		return nil
	}
	var values map[string]any
	if ownEntry.Op == OpUpsert && len(ownEntry.Body) > 0 {
		values, _ = DecodeBody(ownEntry.Body)
	}
	rootTable, rootID := findRootIDAndTable(tx, ownEntry, values)
	if rootID == "" || !isTakeoverRoot(t, rootID) {
		return nil
	}
	fencedRootID := rootID
	if rootTable == "binding" && t.BindingID == "" && t.RecordID != "" {
		fencedRootID = t.RecordID
	}
	return tx.RecordFencedChange(fencedRootID, ownEntry.Table, ownEntry.PK, now)
}

func isTakeoverRoot(t Takeover, rootID string) bool {
	return (t.RecordID != "" && rootID == t.RecordID) ||
		(t.BindingID != "" && rootID == t.BindingID)
}

// exportCtl appends the control entries a takeover queued, before the rows that
// follow them, so an importer applies the fence before the taker's first write.
// A locked machine keeps them queued: a control entry must be signed.
func (e *Exporter) exportCtl() (int, error) {
	if len(e.key) == 0 {
		return 0, nil
	}
	pending, err := e.db.PendingCtl()
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	entries := make([]Entry, 0, len(pending))
	for _, p := range pending {
		var t Takeover
		if err := json.Unmarshal(p.Body, &t); err != nil {
			return 0, fmt.Errorf("synclog: pending takeover: %w", err)
		}
		entry, err := NewTakeover(e.origin, t, e.schema, e.now())
		if err != nil {
			return 0, err
		}
		entries = append(entries, entry)
	}
	if err := e.guard(e.transport, e.origin); err != nil {
		return 0, err
	}
	e.sign(entries)
	written, err := e.appendTo(e.transport, e.origin, entries)
	if err != nil {
		return 0, fmt.Errorf("synclog: export: append takeover: %w", err)
	}
	noteOwnHead(e.db.LocalOrSelf(), written)
	return len(entries), e.db.DropPendingCtl(len(pending))
}

// noteSchema records the highest schema version a verified batch carries, once
// per increase: a hand-over reads it to refuse a target that has not upgraded.
func (i *Importer) noteSchema(origin string, batch []Entry) error {
	if i.schemaSeen == nil {
		i.schemaSeen = map[string]int{}
	}
	top := 0
	for _, e := range batch {
		top = max(top, e.SchemaVersion)
	}
	if top <= i.schemaSeen[origin] {
		return nil
	}
	if err := i.db.NoteOriginSchema(origin, top); err != nil {
		return fmt.Errorf("synclog: import: note schema of %s: %w", origin, err)
	}
	i.schemaSeen[origin] = top
	return nil
}

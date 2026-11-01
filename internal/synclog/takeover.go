package synclog

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// CtlTakeover is the reserved table moving a binding's writer without the old
// writer's consent.
const CtlTakeover = "ctl:takeover"

func IsCtlTable(tbl string) bool { return strings.HasPrefix(tbl, "ctl:") }

// Takeover is a ctl:takeover body: the left writer, its last valid entry, and the
// taker.
type Takeover struct {
	RecordID  string `json:"record_id"`
	BindingID string `json:"binding_id"`
	OldWriter string `json:"old_writer"`
	Upto      int    `json:"upto"`
	Taker     string `json:"taker"`
}

// TakeoverPK keys the entry by root and old writer, so a later takeover supersedes
// in compaction and the fence only rises.
func TakeoverPK(recordID, oldWriter string) string {
	raw, _ := json.Marshal([]string{recordID, oldWriter})
	return string(raw)
}

// NewTakeover builds the takeover entry. After names the old writer's fenced
// entry, so no importer flips the writer before applying through it.
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

// applyTakeover writes a takeover's effect: both roots flip to the taker, the old
// term rises to the fence, and this installation's queued flip is discarded, since
// an import never exports.
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

// exportCtl appends queued control entries before the rows following them, so the
// fence applies before the taker's first write. Locked keeps them queued: control
// entries must be signed.
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

// noteSchema records a verified batch's highest schema version, once per increase:
// a hand-over reads it to refuse an unupgraded target.
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

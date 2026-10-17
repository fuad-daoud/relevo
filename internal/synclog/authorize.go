package synclog

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// HoldError indicates an entry caused the origin to be held rather than dropped.
type HoldError struct {
	Reason string
}

func (h *HoldError) Error() string {
	return h.Reason
}

// decision is what authorize decided about an entry before it is written.
type decision struct {
	owner        string
	isHandover   bool
	targetWriter string
}

// isBindingFamily reports whether tbl is in the binding or binding_record tree.
func isBindingFamily(tbl string) bool {
	switch tbl {
	case "binding", "binding_record", "binding_event", "round_file", "round", "event", "artifact", "transcript":
		return true
	default:
		return false
	}
}

func columnFromRow(row db.ExchangeRow, colName string) string {
	for _, col := range row.Columns {
		if col.Name == colName {
			s, _ := col.Value.(string)
			return s
		}
	}
	return ""
}

func pkFirst(tbl, pk string) string {
	if _, pks, err := KeyOf(tbl, pk); err == nil && len(pks) > 0 {
		s, _ := pks[0].(string)
		return s
	}
	return ""
}

func resolveRoundEventRoot(tx *db.Tx, e Entry, values map[string]any) string {
	if bID, _ := values["binding_id"].(string); bID != "" {
		return bID
	}
	if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
		return columnFromRow(row, "binding_id")
	}
	return ""
}

func resolveArtifactRoot(tx *db.Tx, e Entry, values map[string]any) string {
	rID, _ := values["round_id"].(string)
	if rID == "" {
		if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
			rID = columnFromRow(row, "round_id")
		}
	}
	if rID == "" {
		return ""
	}
	if row, ok, err := tx.ReadExchangeRow("round", `["`+rID+`"]`); err == nil && ok {
		return columnFromRow(row, "binding_id")
	}
	return ""
}

func resolveTranscriptRoot(tx *db.Tx, e Entry, values map[string]any) (string, string) {
	kind, _ := values["owner_kind"].(string)
	ownerID, _ := values["owner_id"].(string)
	if kind == "" || ownerID == "" {
		if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
			if kind == "" {
				kind = columnFromRow(row, "owner_kind")
			}
			if ownerID == "" {
				ownerID = columnFromRow(row, "owner_id")
			}
		}
	}
	if kind != "round" || ownerID == "" {
		return "", ""
	}
	if row, ok, err := tx.ReadExchangeRow("round", `["`+ownerID+`"]`); err == nil && ok {
		return "binding", columnFromRow(row, "binding_id")
	}
	return "", ""
}

func findRootIDAndTable(tx *db.Tx, e Entry, values map[string]any) (string, string) {
	switch e.Table {
	case "binding_record":
		id, _ := values["id"].(string)
		if id == "" {
			id = pkFirst(e.Table, e.PK)
		}
		return "binding_record", id
	case "binding":
		id, _ := values["id"].(string)
		if id == "" {
			id = pkFirst(e.Table, e.PK)
		}
		return "binding", id
	case "binding_event", "round_file":
		recID, _ := values["record_id"].(string)
		if recID == "" {
			recID = pkFirst(e.Table, e.PK)
		}
		return "binding_record", recID
	case "round", "event":
		return "binding", resolveRoundEventRoot(tx, e, values)
	case "artifact":
		return "binding", resolveArtifactRoot(tx, e, values)
	case "transcript":
		return resolveTranscriptRoot(tx, e, values)
	default:
		return "", ""
	}
}

// resolveRootAndWriter resolves the root ID and the root row's current writer for entry e.
func resolveRootAndWriter(tx *db.Tx, e Entry, values map[string]any) (rootID string, currentWriter string, foundRoot bool, err error) {
	rootTable, rID := findRootIDAndTable(tx, e, values)
	if rID == "" || rootTable == "" {
		return "", "", false, nil
	}
	row, ok, err := tx.ReadExchangeRow(rootTable, `["`+rID+`"]`)
	if err != nil {
		return rID, "", false, err
	}
	if !ok {
		return rID, "", false, nil
	}
	return rID, columnFromRow(row, "writer"), true, nil
}

func applyHandover(tx *db.Tx, e Entry, rootID, bodyWriter string, values map[string]any) (decision, error) {
	if e.Table == "binding_record" {
		name, _ := values["name"].(string)
		owner, _ := values["owner"].(string)
		if collides, err := tx.HasLiveBindingRecord(bodyWriter, owner, name, rootID); err != nil {
			return decision{}, err
		} else if collides {
			return decision{}, &HoldError{Reason: "name taken: unbind or rename the local binding, then the hand-over applies"}
		}
	}
	if err := tx.SetTerm(rootID, e.Origin, e.Seq); err != nil {
		return decision{}, err
	}
	if err := tx.DeleteTerm(rootID, bodyWriter); err != nil {
		return decision{}, err
	}
	if bodyWriter == tx.Origin() {
		if err := tx.SetWriterAfter(rootID, e.Origin, e.Seq); err != nil {
			return decision{}, err
		}
		if err := tx.ClearBindingLeft(rootID); err != nil {
			return decision{}, err
		}
	}
	return decision{owner: bodyWriter, isHandover: true, targetWriter: bodyWriter}, nil
}

func authorizeNonBinding(tx *db.Tx, e Entry) (decision, error) {
	owner, found, err := tx.ResolveOwner(e.Table, e.PK)
	if err != nil {
		return decision{}, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	if found && owner != e.Origin {
		return decision{}, fmt.Errorf("synclog: import %s %s: %s does not own it, %s does: %w: %w",
			e.Table, e.PK, e.Origin, owner, errNotOwner, ErrInvalid)
	}
	return decision{owner: e.Origin}, nil
}

func authorizeAbsentRoot(tx *db.Tx, e Entry, rootID string, values map[string]any) (decision, error) {
	if e.Table != "binding" && e.Table != "binding_record" {
		return decision{owner: e.Origin}, nil
	}
	bodyWriter, _ := values["writer"].(string)
	bodyOrigin, _ := values["origin"].(string)
	if bodyWriter == "" {
		bodyWriter = e.Origin
	}
	if bodyOrigin == "" {
		bodyOrigin = e.Origin
	}
	if bodyOrigin != e.Origin && e.After == nil {
		return decision{}, fmt.Errorf("forged creator claim: root %s %s origin %q differs from entry origin %q without After: %w",
			e.Table, e.PK, bodyOrigin, e.Origin, ErrInvalid)
	}
	if bodyWriter != e.Origin {
		if bodyOrigin != e.Origin && e.After == nil {
			return decision{}, fmt.Errorf("handover first sight with different creator requires After: %w", ErrInvalid)
		}
		return applyHandover(tx, e, rootID, bodyWriter, values)
	}
	return decision{owner: e.Origin}, nil
}

func authorizePresentRoot(tx *db.Tx, e Entry, rootID, currentWriter string, values map[string]any) (decision, error) {
	if currentWriter == e.Origin {
		if (e.Table == "binding" || e.Table == "binding_record") && e.Op == OpUpsert {
			bodyWriter, _ := values["writer"].(string)
			if bodyWriter != "" && bodyWriter != e.Origin {
				return applyHandover(tx, e, rootID, bodyWriter, values)
			}
		}
		return decision{owner: e.Origin}, nil
	}

	upto, regrantOrigin, regrantSeq, foundTerm, err := tx.Term(rootID, e.Origin)
	if err != nil {
		return decision{}, err
	}
	if foundTerm {
		term := &Term{
			RootID:        rootID,
			Writer:        e.Origin,
			Upto:          upto,
			RegrantOrigin: regrantOrigin,
			RegrantSeq:    regrantSeq,
		}
		if !EntryValid(term, e) {
			return decision{}, fmt.Errorf("writer moved: entry %d from %q is above upto %d: %w", e.Seq, e.Origin, term.Upto, ErrInvalid)
		}
		return decision{owner: e.Origin}, nil
	}

	return decision{}, fmt.Errorf("synclog: import %s %s: %s does not own it, %s does: %w: %w",
		e.Table, e.PK, e.Origin, currentWriter, errNotOwner, ErrInvalid)
}

// authorize evaluates whether entry e is valid to write under ownership and term rules.
func authorize(tx *db.Tx, e Entry, values map[string]any) (decision, error) {
	if !isBindingFamily(e.Table) {
		return authorizeNonBinding(tx, e)
	}
	rootID, currentWriter, foundRoot, err := resolveRootAndWriter(tx, e, values)
	if err != nil {
		return decision{}, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}
	if !foundRoot {
		return authorizeAbsentRoot(tx, e, rootID, values)
	}
	return authorizePresentRoot(tx, e, rootID, currentWriter, values)
}

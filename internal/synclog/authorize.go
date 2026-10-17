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

// resolveRootAndWriter resolves the root ID and the root row's current writer for entry e.
func resolveRootAndWriter(tx *db.Tx, e Entry, values map[string]any) (rootID string, currentWriter string, foundRoot bool, err error) {
	var rootTable string
	switch e.Table {
	case "binding_record":
		rootTable = "binding_record"
		if id, ok := values["id"].(string); ok && id != "" {
			rootID = id
		} else if _, pks, err := KeyOf(e.Table, e.PK); err == nil && len(pks) > 0 {
			rootID, _ = pks[0].(string)
		}
	case "binding":
		rootTable = "binding"
		if id, ok := values["id"].(string); ok && id != "" {
			rootID = id
		} else if _, pks, err := KeyOf(e.Table, e.PK); err == nil && len(pks) > 0 {
			rootID, _ = pks[0].(string)
		}
	case "binding_event", "round_file":
		rootTable = "binding_record"
		if recID, ok := values["record_id"].(string); ok && recID != "" {
			rootID = recID
		} else if _, pks, err := KeyOf(e.Table, e.PK); err == nil && len(pks) > 0 {
			rootID, _ = pks[0].(string)
		}
	case "round", "event":
		rootTable = "binding"
		if bID, ok := values["binding_id"].(string); ok && bID != "" {
			rootID = bID
		} else if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
			for _, col := range row.Columns {
				if col.Name == "binding_id" {
					rootID, _ = col.Value.(string)
				}
			}
		}
	case "artifact":
		rootTable = "binding"
		var roundID string
		if rID, ok := values["round_id"].(string); ok && rID != "" {
			roundID = rID
		} else if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
			for _, col := range row.Columns {
				if col.Name == "round_id" {
					roundID, _ = col.Value.(string)
				}
			}
		}
		if roundID != "" {
			if row, ok, err := tx.ReadExchangeRow("round", `["`+roundID+`"]`); err == nil && ok {
				for _, col := range row.Columns {
					if col.Name == "binding_id" {
						rootID, _ = col.Value.(string)
					}
				}
			}
		}
	case "transcript":
		kind, _ := values["owner_kind"].(string)
		ownerID, _ := values["owner_id"].(string)
		if kind == "" || ownerID == "" {
			if row, ok, err := tx.ReadExchangeRow(e.Table, e.PK); err == nil && ok {
				for _, col := range row.Columns {
					if col.Name == "owner_kind" && kind == "" {
						kind, _ = col.Value.(string)
					}
					if col.Name == "owner_id" && ownerID == "" {
						ownerID, _ = col.Value.(string)
					}
				}
			}
		}
		if kind == "round" {
			rootTable = "binding"
			if row, ok, err := tx.ReadExchangeRow("round", `["`+ownerID+`"]`); err == nil && ok {
				for _, col := range row.Columns {
					if col.Name == "binding_id" {
						rootID, _ = col.Value.(string)
					}
				}
			}
		} else {
			return "", "", false, nil
		}
	default:
		return "", "", false, nil
	}

	if rootID == "" || rootTable == "" {
		return "", "", false, nil
	}

	row, ok, err := tx.ReadExchangeRow(rootTable, `["`+rootID+`"]`)
	if err != nil {
		return rootID, "", false, err
	}
	if !ok {
		return rootID, "", false, nil
	}
	var writer string
	for _, col := range row.Columns {
		if col.Name == "writer" {
			writer, _ = col.Value.(string)
		}
	}
	return rootID, writer, true, nil
}

// authorize evaluates whether entry e is valid to write under ownership and term rules.
func authorize(tx *db.Tx, e Entry, values map[string]any) (decision, error) {
	if !isBindingFamily(e.Table) {
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

	rootID, currentWriter, foundRoot, err := resolveRootAndWriter(tx, e, values)
	if err != nil {
		return decision{}, fmt.Errorf("synclog: import %s %s: %w", e.Table, e.PK, err)
	}

	// Case 1: Root row absent in DB (Rule 3) or child whose parent is absent.
	if !foundRoot {
		if e.Table != "binding" && e.Table != "binding_record" {
			// Children whose parent is absent keep today's FK deferral.
			return decision{owner: e.Origin}, nil
		}

		// Root table upsert where root row is absent.
		bodyWriter, _ := values["writer"].(string)
		bodyOrigin, _ := values["origin"].(string)
		if bodyWriter == "" {
			bodyWriter = e.Origin
		}
		if bodyOrigin == "" {
			bodyOrigin = e.Origin
		}

		// Immutability / claim check: body origin must match entry origin unless carrying After.
		if bodyOrigin != e.Origin && e.After == nil {
			return decision{}, fmt.Errorf("forged creator claim: root %s %s origin %q differs from entry origin %q without After: %w",
				e.Table, e.PK, bodyOrigin, e.Origin, ErrInvalid)
		}

		// Handover on first sight (body writer differs from entry origin)
		if bodyWriter != e.Origin {
			if bodyOrigin != e.Origin && e.After == nil {
				return decision{}, fmt.Errorf("handover first sight with different creator requires After: %w", ErrInvalid)
			}
			// Check unique collision on target
			if e.Table == "binding_record" {
				name, _ := values["name"].(string)
				owner, _ := values["owner"].(string)
				if collides, err := tx.HasLiveBindingRecord(bodyWriter, owner, name, rootID); err != nil {
					return decision{}, err
				} else if collides {
					return decision{}, &HoldError{Reason: "name taken: unbind or rename the local binding, then the hand-over applies"}
				}
			}

			// Record term for old writer
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
			}
			return decision{owner: bodyWriter, isHandover: true, targetWriter: bodyWriter}, nil
		}

		return decision{owner: e.Origin}, nil
	}

	// Case 2: Root row is present in DB.
	// Sub-case 2a: current writer == e.Origin
	if currentWriter == e.Origin {
		// Check if it is a hand-over (root upsert with body writer != e.Origin)
		if (e.Table == "binding" || e.Table == "binding_record") && e.Op == OpUpsert {
			bodyWriter, _ := values["writer"].(string)
			if bodyWriter != "" && bodyWriter != e.Origin {
				// Rule 5: hand-over!
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
				}
				return decision{owner: bodyWriter, isHandover: true, targetWriter: bodyWriter}, nil
			}
		}

		// Rule 1: regular write by current writer.
		return decision{owner: e.Origin}, nil
	}

	// Sub-case 2b: current writer != e.Origin
	// Rule 2: Term (R, e.Origin) present?
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
		// Honest late write
		return decision{owner: e.Origin}, nil
	}

	// Rule 4: Any other writer
	return decision{}, fmt.Errorf("synclog: import %s %s: %s does not own it, %s does: %w: %w",
		e.Table, e.PK, e.Origin, currentWriter, errNotOwner, ErrInvalid)
}

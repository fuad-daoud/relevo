package relevo

import (
	"encoding/json"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainPullTrace copies a server chain's step trace onto the mirror, keeping the
// server's own seq and timestamp on every row: a mirror trace that renumbered
// would show transitions in an order the chain never ran in, and `show --trace`
// reads the seq.
//
// A row whose seq the mirror already holds is skipped, so a re-pull appends
// nothing. A row the server sends with no seq is not a position in the trace and
// is refused rather than given one: the mirror's own rows keep the seqs the
// server named.
func chainPullTrace(tx *store.Tx, name string, trace []remote.ChainEventView) error {
	if len(trace) == 0 {
		return nil
	}
	have, err := tx.ChainEvents(name)
	if err != nil {
		return err
	}
	seen := make(map[int]bool, len(have))
	for _, e := range have {
		seen[e.Seq] = true
	}
	for _, v := range trace {
		if v.Seq <= 0 || seen[v.Seq] {
			continue
		}
		row, ok := chainTraceRow(v)
		if !ok {
			continue
		}
		if _, err := tx.ChainEventPut(name, row); err != nil {
			return err
		}
		seen[v.Seq] = true
	}
	return nil
}

// chainTraceRow is one wire trace row as a stored one, reporting false for a row
// the mirror refuses: a row with no seq has no place in the trace, and a row
// whose event or action is not the document the column holds is a wire fault,
// never something to guess a replacement for. A column the engine left empty
// stays empty, which is what a start's own row ships.
func chainTraceRow(v remote.ChainEventView) (db.ChainEventRow, bool) {
	if v.Seq <= 0 {
		slog.Warn("chain trace row has no seq; not stored", "member", v.Member, "round", v.Round)
		return db.ChainEventRow{}, false
	}
	if !traceDocumentIs(v.Event) || !traceDocumentIs(v.Action) {
		slog.Warn("chain trace row has a malformed event or action; not stored",
			"seq", v.Seq, "member", v.Member, "round", v.Round)
		return db.ChainEventRow{}, false
	}
	return db.ChainEventRow{
		Seq: v.Seq, TS: v.TS, Phase: v.Phase, Step: v.Step, Member: v.Member,
		Round: v.Round, Plan: v.Plan, Event: v.Event, Action: v.Action, Reason: v.Reason,
	}, true
}

// traceDocumentIs reports whether a stored column holds a JSON document or is
// empty: the engine writes an encoded event and action, and the empty string is
// how it writes "this row had none".
func traceDocumentIs(s string) bool {
	if s == "" {
		return true
	}
	return json.Valid([]byte(s))
}

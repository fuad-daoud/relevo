package ui

import (
	"github.com/fuad-daoud/relevo/internal/view"
)

// This file holds the round view's own row: the single-row fetch's reply and
// the merge that puts its figures over the fleet row the shell refreshes. It
// is split out of view_round.go for the 600-line file ceiling, not because it
// is a separate concern from the rest of the round view.

// rows is the report the pane reads: the shell's, with this view's own rows
// (a chain's members) merged in, and then the fetched detail row merged in over
// the shell's own row for the same key. A detail fetch still in flight -- or
// one that failed -- contributes nothing, so the pane paints the fleet row it
// has.
//
// A row this view brought itself is never overlaid: a chain member's document
// is the one the pane was opened on, and the fetched row is the binding's own
// live status rather than the chain step the human is reading.
func (r roundView) rows(env Env) view.Report {
	rep := mergeRows(env.Report, r.extra)
	if row(env.Report, r.detailKey) == nil {
		return rep
	}
	return overlayRow(rep, r.detailKey, r.detailRow)
}

// overlayRow is rep with the fetched row's detail figures filling in the fleet
// row of the same key. Only those three move: every other field stays the
// fleet report's, so opening a row changes what the pane shows about the diff,
// the live usage and the tail and nothing else -- the state, the cells, the
// owner stamps and the rest are the report's.
//
// A key the report does not carry gets no row: there is nothing to fill in, and
// a fetched row would be a second row for one binding.
func overlayRow(rep view.Report, key string, detail *view.BindingStatus) view.Report {
	if key == "" || detail == nil {
		return rep
	}
	i := -1
	for j := range rep.Bindings {
		if rep.Bindings[j].Key() == key {
			i = j
			break
		}
	}
	if i < 0 {
		return rep
	}
	// The shell's report is lent on every call, so the merged row goes into a
	// copy: writing into its slice would leave the detail figures in the
	// fleet's own report for the rest of the session.
	out := rep
	out.Bindings = append([]view.BindingStatus(nil), rep.Bindings...)
	out.Bindings[i] = withDetailFigures(out.Bindings[i], *detail)
	return out
}

// withDetailFigures is base with the figures detail carries and base lacks.
// Filling in only: the fetch exists to restore what the fleet gate dropped, so
// a figure base already shows is left alone -- a runtime with no usage reader
// answers nil, and that must not retract a figure the fleet read did produce.
//
// A fetched row with no headless tail leaves the base's own headless info, so a
// row that has one keeps showing its pid and start time.
func withDetailFigures(base, detail view.BindingStatus) view.BindingStatus {
	out := base
	if out.Live == nil {
		out.Live = detail.Live
	}
	if out.LiveUsage == nil {
		out.LiveUsage = detail.LiveUsage
	}
	if detail.Headless != nil && len(detail.Headless.Tail) > 0 &&
		(out.Headless == nil || len(out.Headless.Tail) == 0) {
		h := &view.HeadlessInfo{}
		if out.Headless != nil {
			*h = *out.Headless
		}
		h.Tail = detail.Headless.Tail
		out.Headless = h
	}
	return out
}

// onDetailRow is the detailRowMsg arm: a reply for the row on screen lands its
// three figures, and a reply that failed -- or that names a row the view has
// already left -- leaves the fleet's own nil figures. There is no placeholder
// figure and no stale full row.
func (r roundView) onDetailRow(msg detailRowMsg) roundView {
	if msg.key != r.pane.detail.name {
		return r
	}
	if msg.err != nil || msg.row == nil {
		r.detailKey, r.detailRow = "", nil
		return r
	}
	r.detailKey, r.detailRow = msg.key, msg.row
	return r
}

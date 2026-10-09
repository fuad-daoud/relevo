package view

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Next is the move a row waiting on the MasterMind offers: a short button
// label and the text a consumer pastes into the MasterMind's input.
type Next struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// StatusLineGate is one live gate in StatusLineDoc, so a consumer can toast
// a gate appearing or clearing by diffing polls. Until is RFC3339, empty when
// the gate lasts until cleared.
type StatusLineGate struct {
	Token    string `json:"token"`
	Provider string `json:"provider"`
	Until    string `json:"until"`
	Reason   string `json:"reason"`
}

// StatusLineGates projects the machine's live gates for the document.
func StatusLineGates(gated []availability.Gate) []StatusLineGate {
	var out []StatusLineGate
	for _, g := range gated {
		reason := g.Note
		if reason == "" {
			reason = availability.GateKindText(g.Kind)
		}
		until := ""
		if !g.Until.IsZero() {
			until = g.Until.UTC().Format(time.RFC3339)
		}
		out = append(out, StatusLineGate{
			Token: g.Token, Provider: availability.ProviderOf(g.Token),
			Until: until, Reason: reason,
		})
	}
	return out
}

// nextOf is the row's next move, nil unless the row waits on the MasterMind:
// tone "needs" or "report", the same rule rowStatus sets.
func nextOf(b BindingStatus, tone string) *Next {
	if tone != "needs" && tone != "report" {
		return nil
	}
	p := b.LastPayload
	toMasterMind := p != nil && p.Direction == store.DirToMasterMind
	if toMasterMind && p.Kind == store.KindQuestion {
		q := b.Question
		if q == "" && b.Waiting != nil {
			q = b.Waiting.Line
		}
		return &Next{
			Label: "answer",
			Text:  fmt.Sprintf("Answer %s r%d's question %q: ", b.Name, p.Round, capLine(q, 120)),
		}
	}
	if tone == "report" && toMasterMind && p.Kind == store.KindReport && b.Shape == store.ShapeReader {
		// A reader's round ends in an artifact to review, not a diff to check.
		return &Next{
			Label: "review the output",
			Text:  fmt.Sprintf("Review %s r%d's output: relevo show %s --round %d --output", b.Name, p.Round, b.Name, p.Round),
		}
	}
	if tone == "report" && toMasterMind && p.Kind == store.KindReport {
		text := fmt.Sprintf("Verify %s r%d: run make check, then compare relevo show %s --round %d --diff",
			b.Name, p.Round, b.Name, p.Round)
		if b.PromptPath != "" {
			text += " against " + b.PromptPath
		}
		return &Next{Label: "make check, compare the diff", Text: text}
	}
	if tone != "needs" {
		return nil
	}
	return haltNext(b.Name, b.Round, b.Waiting, b.Detail)
}

// haltNext is the halt case: the resolving move a row's Waiting names, else
// the row's own detail.
func haltNext(name string, round int, w *Waiting, detail string) *Next {
	text := "Resolve " + name
	if round > 0 {
		text += fmt.Sprintf(" r%d", round)
	}
	switch {
	case w != nil:
		text += fmt.Sprintf(" (%s: %s): run %s", w.Cause, w.Line, w.Hint)
	case detail != "":
		text += ": " + detail
	}
	return &Next{Label: "resolve the halt", Text: text}
}

// haltLineOf is what a row waiting on a halt is waiting on, so a consumer can
// show the halt itself; the row's Reason keeps the status column's wording.
func haltLineOf(b BindingStatus, tone string) string {
	if tone != "needs" || b.Waiting == nil {
		return ""
	}
	return b.Waiting.Line
}

// withNextMove fills the row's next move and halt line, the two fields a
// row waiting on the MasterMind carries for the band.
func withNextMove(row StatusLineRow, b BindingStatus, tone string) StatusLineRow {
	row.Next = nextOf(b, tone)
	row.Halt = haltLineOf(b, tone)
	return row
}

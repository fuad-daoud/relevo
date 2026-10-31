package ui

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

func candPickSentence(doc relevo.ConfigDoc, env widget.Env, r candRow) string {
	if !r.on {
		return widget.MutedStyle.Render("off: no actor has it on")
	}
	parts := make([]string, 0, len(r.slots))
	for _, s := range r.slots {
		parts = append(parts, widget.MutedStyle.Render("the ")+widget.TextStyle.Render(s.Actor)+
			widget.MutedStyle.Render("'s "+candOrdinal(s.Position)+" pick"))
	}
	out := strings.Join(parts, widget.MutedStyle.Render(" and "))
	if r.gate == nil || len(r.slots) == 0 {
		return out
	}
	actor := r.slots[0].Actor
	if next, ok := nextReady(doc, env, actor, r.c.Name); ok {
		return out + widget.MutedStyle.Render("; while it is gated the ") + widget.TextStyle.Render(actor) +
			widget.MutedStyle.Render(" takes ") + widget.TextStyle.Render(next)
	}
	return out + widget.MutedStyle.Render("; the ") + widget.TextStyle.Render(actor) +
		widget.MutedStyle.Render(" has no other ready candidate")
}

func nextReady(doc relevo.ConfigDoc, env widget.Env, actor, name string) (string, bool) {
	type slot struct {
		name string
		on   bool
	}
	var list []slot
	at := -1
	for _, e := range doc.Actors[actor].Candidates {
		n := candName(doc, e.Candidate)
		if n == "" {
			continue
		}
		if n == name && at < 0 {
			at = len(list)
		}
		list = append(list, slot{name: n, on: !e.Off})
	}
	if at < 0 {
		return "", false
	}
	ready := func(i int) (string, bool) {
		s := list[i]
		if !s.on || s.name == name {
			return "", false
		}
		for _, c := range doc.Candidates {
			if c.Name != s.name {
				continue
			}
			token := c.Ref().String()
			for j := range env.Report.Gated {
				if env.Report.Gated[j].Token == token {
					return "", false
				}
			}
			return s.name, true
		}
		return "", false
	}
	for i := at + 1; i < len(list); i++ {
		if n, ok := ready(i); ok {
			return n, true
		}
	}
	for i := 0; i < at; i++ {
		if n, ok := ready(i); ok {
			return n, true
		}
	}
	return "", false
}

func candDetailLines(doc relevo.ConfigDoc, env widget.Env, r candRow, width int) []string {
	c := r.c
	meta := c.Harness + " · " + c.Provider + " · " + c.Model
	first := "   " + widget.FaintStyle.Bold(true).Render(c.Name) + "   " + widget.MutedStyle.Render(meta)
	out := []string{widget.Fit(first, width)}
	if r.gate != nil {
		g := *r.gate
		line := "   " + widget.RedStyle.Render("gated") +
			widget.MutedStyle.Render(" since "+candSince(g.Since, env.Now)) +
			widget.MutedStyle.Render(", "+candUntilText(g, env.Now))
		if reason := stats.GateReasonText(g.Note); reason != "" {
			line += "   " + widget.TextStyle.Render(reason)
		}
		out = append(out, widget.Fit(line, width))
	}
	return append(out, widget.Fit("   "+candPickSentence(doc, env, r), width))
}

type candLeft struct{ actor, name string }

func candActorsLeft(doc relevo.ConfigDoc, name string) []candLeft {
	var out []candLeft
	for _, actor := range actorOrder(doc.Actors) {
		var remaining []string
		had := false
		for _, e := range doc.Actors[actor].Candidates {
			n := candName(doc, e.Candidate)
			if n == "" || e.Off {
				continue
			}
			if n == name {
				had = true
				continue
			}
			remaining = append(remaining, n)
		}
		if had && len(remaining) == 1 {
			out = append(out, candLeft{actor: actor, name: remaining[0]})
		}
	}
	return out
}

func candDeleteLines(doc relevo.ConfigDoc, env widget.Env, r candRow) []string {
	label := func(l string) string { return widget.Pad(l, 11) }
	var out []string
	if len(r.slots) > 0 {
		drop := "it drops it"
		if len(r.slots) > 1 {
			drop = "both drop it"
		}
		out = append(out, label("picked by")+servesText(r)+"   "+drop)
	}
	for _, left := range candActorsLeft(doc, r.c.Name) {
		out = append(out, label("")+"the "+left.actor+" is left with "+left.name)
	}
	if r.gate != nil {
		out = append(out, label("kept")+r.c.Provider+"'s gate, "+candUntilText(*r.gate, env.Now))
	}
	return append(out, label("")+"its past rounds in :rounds and :stats")
}

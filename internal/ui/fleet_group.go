package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

type fleetGroup int

const (
	groupNeedsYou fleetGroup = iota
	groupWorking
	groupIdle
	groupHeld
	groupOther
	groupDone
)

func groupOf(b view.BindingStatus) fleetGroup {
	switch {
	case b.Display == "NEEDS YOU" || reportReady(b):
		return groupNeedsYou
	case b.Display == "ACTIVE" && b.BuilderStatus != "idle":
		return groupWorking
	case b.Display == "ACTIVE" && b.BuilderStatus == "idle":
		return groupIdle
	// HELD: DisplayState never produces it; PAUSED is live and groups
	// here, and the word still groups the same way when a row carries it.
	case b.Display == "HELD" || b.Display == "PAUSED":
		return groupHeld
	case b.Display == "DONE":
		return groupDone
	default:
		return groupOther
	}
}

type groupMeta struct {
	label string
	hint  string
	pill  lipgloss.Style
	dot   lipgloss.Style
	glyph string
}

var groupMetas = map[fleetGroup]groupMeta{
	groupNeedsYou: {
		label: "needs you",
		hint:  "a builder is waiting on an answer",
		pill:  chipWarnStyle,
		dot:   warnStyle,
		glyph: "●",
	},
	groupWorking: {
		label: "working",
		hint:  "a round is running",
		pill:  chipGreenStyle,
		dot:   greenStyle,
		glyph: "●",
	},
	groupIdle: {
		label: "idle",
		hint:  "finished, waiting for the next plan",
		pill:  kbdStyle,
		dot:   mutedStyle,
		glyph: "○",
	},
	groupHeld: {
		label: "on hold",
		hint:  "paused or held",
		pill:  kbdStyle,
		dot:   mutedStyle,
		glyph: "◐",
	},
	groupOther: {
		label: "other",
		hint:  "",
		pill:  kbdStyle,
		dot:   mutedStyle,
		glyph: "○",
	},
	groupDone: {
		label: "done",
		hint:  "",
		pill:  kbdStyle,
		dot:   faintStyle,
		glyph: "✓",
	},
}

// shownRound is the round to name on a row: a closed round's Round already
// names the next round, so the round that reported comes from the payload,
// the same rule as the status line.
func shownRound(b view.BindingStatus) int {
	if b.LastPayload != nil &&
		b.LastPayload.Direction == store.DirToMasterMind &&
		(b.LastPayload.Kind == store.KindReport || b.LastPayload.Kind == store.KindQuestion) {
		return b.LastPayload.Round
	}
	return b.Round
}

func rowNow(b view.BindingStatus, now time.Time) string {
	g := groupOf(b)
	if g == groupDone {
		return nowCell(b, now)
	}

	rndPrefix := ""
	if sr := shownRound(b); sr > 0 {
		rndPrefix = fmt.Sprintf("r%d · ", sr)
	}

	switch g {
	case groupWorking:
		// The word comes from the shared rule; a runner the rule does not
		// recognise (unknown, or a word a row must not claim) still says what
		// its status says.
		statusPart := view.ActivityWord(b)
		if statusPart == "" {
			statusPart = b.BuilderStatus
		}
		if !b.RoundStart.IsZero() {
			if age := ago(b.RoundStart, now); age != "" {
				statusPart += " · " + age
			}
		}
		return rndPrefix + statusPart

	case groupIdle:
		var idlePart string
		if b.LastPayload != nil && b.LastPayload.Kind == store.KindReport {
			word := "reported"
			if b.Shape == store.ShapeReader {
				word = "artifact"
			}
			if age := ago(b.LastPayload.TS, now); age != "" {
				idlePart = word + " " + age
			} else {
				idlePart = word
			}
		} else if b.Last != nil && !b.Last.TS.IsZero() {
			age := ago(b.Last.TS, now)
			if age != "" {
				idlePart = "idle " + age
			} else {
				idlePart = "idle"
			}
		} else {
			idlePart = "idle"
		}
		return rndPrefix + idlePart

	default: // groupNeedsYou, groupHeld, groupOther
		return rndPrefix + nowCell(b, now)
	}
}

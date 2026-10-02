package view

import (
	"strings"
)

// ConsumedNoteMarker is the marker note written when a chain consumes a
// member round.
const ConsumedNoteMarker = "consumed by chain "

// isConsumedPayload reports whether p carries a chain-consumption note.
func isConsumedPayload(p *LastEvent) bool {
	return p != nil && strings.Contains(p.Note, ConsumedNoteMarker)
}

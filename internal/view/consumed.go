package view

import (
	"strings"
)

const ConsumedNoteMarker = "consumed by chain "

func isConsumedPayload(p *LastEvent) bool {
	return p != nil && strings.Contains(p.Note, ConsumedNoteMarker)
}

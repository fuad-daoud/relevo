package view

import "unicode/utf8"

// StatusLineBoard is the live board block in StatusLineDoc: the scene name, its
// scope ("live") and the URL -- with its per-run token -- so it can be copied
// straight into a browser. It is null when no live board is running.
type StatusLineBoard struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	URL   string `json:"url"`
}

// RenderBoardLine is the statusline's board line: "board <mastermind> · <url>",
// dim, directly after the MasterMind line, truncated to the statusline width.
// The owner names the MasterMind whose board it is; when it is unknown the line
// falls back to the scene name. A nil block renders nothing.
func RenderBoardLine(b *StatusLineBoard, mastermind string, columns int) string {
	if b == nil || b.Name == "" {
		return ""
	}
	name := mastermind
	if name == "" {
		name = b.Name
	}
	text := "board " + name + " · " + b.URL
	if columns > 0 && utf8.RuneCountInString(text) > columns {
		text = truncate(text, columns)
	}
	return ansiDim + text + ansiReset + "\n"
}

package chatlabel

import (
	"bytes"
	"encoding/json"
	"strings"
)

// claudeEntry is the slice of a transcript line this package reads; unknown
// fields are ignored.
type claudeEntry struct {
	Type            string `json:"type"`
	CustomTitle     string `json:"customTitle"`
	AITitle         string `json:"aiTitle"`
	LastPrompt      string `json:"lastPrompt"`
	BridgeSessionID string `json:"bridgeSessionId"`
}

// Claude builds the Label for a Claude Code transcript (JSONL) from its tail,
// preferring a /rename'd title, then the automatic title, then the last
// prompt. A line that is blank, not JSON, or undecodable is skipped; nothing
// here errors or panics.
func Claude(tail []byte) Label {
	var customTitle, aiTitle, lastPrompt, bridgeSession string

	for _, line := range bytes.Split(tail, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e claudeEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		// Each type re-appends through a session; the latest non-empty value wins.
		switch e.Type {
		case "custom-title":
			if e.CustomTitle != "" {
				customTitle = e.CustomTitle
			}
		case "ai-title":
			if e.AITitle != "" {
				aiTitle = e.AITitle
			}
		case "last-prompt":
			if e.LastPrompt != "" {
				lastPrompt = e.LastPrompt
			}
		case "bridge-session":
			if e.BridgeSessionID != "" {
				bridgeSession = e.BridgeSessionID
			}
		}
	}

	label := Label{}
	switch {
	case clean(customTitle) != "":
		label.Text = clean(customTitle)
	case clean(aiTitle) != "":
		label.Text = clean(aiTitle)
	case clean(lastPrompt) != "":
		label.Text = quote(clean(lastPrompt))
	}

	// A bridged session's id carries a cse_ prefix the claude.ai URL does not.
	if rest, ok := strings.CutPrefix(bridgeSession, "cse_"); ok && rest != "" {
		label.Link = "https://claude.ai/code/session_" + rest
	}
	return label
}

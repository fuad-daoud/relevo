package harness

import (
	"embed"
	"errors"
)

//go:embed agents/*.md agents/*.toml
var agentFS embed.FS

// ErrNoAgentDoc reports a role/kind pair with no embedded definition.
var ErrNoAgentDoc = errors.New("no embedded agent definition")

// AgentDoc returns the embedded definition for one role of one harness kind.
// The filename comes from the table's Doc field, never the caller's role, so a
// caller cannot steer the read with path syntax.
func AgentDoc(role, kind string) ([]byte, error) {
	h, ok := Lookup(kind)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	r, ok := h.Role(role)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	ext := h.DocExt
	if ext == "" {
		ext = "md"
	}
	b, err := agentFS.ReadFile("agents/" + r.Doc + "." + ext)
	if err != nil {
		return nil, ErrNoAgentDoc
	}
	return b, nil
}

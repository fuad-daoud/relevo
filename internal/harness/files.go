package harness

import "embed"

// pluginFS embeds the OpenCode plugin package relevo ships beside its agent
// definitions.
//
//go:embed opencodeplugin/package.json opencodeplugin/server.ts opencodeplugin/tui.tsx
var pluginFS embed.FS

// ShippedFile is one file relevo installs for a harness kind beside its agent
// definitions.
type ShippedFile struct {
	Name  string // install label, e.g. "opencode-plugin/package.json"
	Path  string // home-relative install path
	Embed string // path inside the embed FS
}

// ShippedFileBytes returns the embedded bytes of one shipped file of kind. The
// path read is the table's Embed field, never the caller's name.
func ShippedFileBytes(kind, name string) ([]byte, error) {
	h, ok := Lookup(kind)
	if !ok {
		return nil, ErrNoAgentDoc
	}
	for _, f := range h.Files {
		if f.Name != name {
			continue
		}
		b, err := pluginFS.ReadFile(f.Embed)
		if err != nil {
			return nil, ErrNoAgentDoc
		}
		return b, nil
	}
	return nil, ErrNoAgentDoc
}

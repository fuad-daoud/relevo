package store

import (
	"strings"
	"testing"
)

func TestHandedOverErrorDropsTerminalControlsFromPeerText(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"handover", "takeover"} {
		err := &HandedOverError{
			Name: "bind\x1b[2Jing", To: "id\a", Label: "box\u009b31m-b", Kind: kind,
		}
		msg := err.Error()
		for _, bad := range []string{"\x1b", "\a", "\u009b"} {
			if strings.Contains(msg, bad) {
				t.Errorf("%s: error carries %q: %q", kind, bad, msg)
			}
		}
	}
}

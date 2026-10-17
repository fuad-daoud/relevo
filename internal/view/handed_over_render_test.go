package view

import (
	"strings"
	"testing"
)

func TestHandedOverBlockDropsTerminalControlsFromPeerText(t *testing.T) {
	t.Parallel()

	out := RenderStatus(Report{HandedOver: []HandedOverRow{{
		Name: "bind\x1b[2Jing", To: "box\a\u009b31m-b", Kind: "handover",
	}}})

	for _, bad := range []string{"\x1b", "\a", "\u009b"} {
		if strings.Contains(out, bad) {
			t.Errorf("render carries %q:\n%q", bad, out)
		}
	}
	if !strings.Contains(out, "handed over to") {
		t.Errorf("render lost the row:\n%q", out)
	}
}

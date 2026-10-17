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

func TestHandedOverErrorMessageNamesThisMachineAndFencingTargetChanges(t *testing.T) {
	t.Parallel()

	err := &HandedOverError{
		Name:  "hw",
		To:    "id-zen",
		Label: "zen",
		Kind:  "handover",
	}
	want := `binding "hw" is handed over to zen; to take it back, run relevo takeover hw --force on this machine (it fences zen's later changes)`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

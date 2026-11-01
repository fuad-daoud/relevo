package upgrade

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/text"
)

// SplitCapability is the token a split-aware binary's `daemon --preflight`
// prints on its ok line: --preflight opens nothing, so the static token
// witnesses that the candidate reads config from the machine-local file.
const SplitCapability = "split=1"

// CheckSplitCapability refuses a re-exec candidate whose --preflight output
// lacks the split token on a machine whose local file carries the split
// marker. Following a blind candidate would flap the visible config between
// the two files across re-execs -- sections present, then absent, then
// present again -- so the daemon stays on its current image. On a machine
// without the marker every candidate passes.
func CheckSplitCapability(preflightOut string, splitActive bool) error {
	if !splitActive {
		return nil
	}
	for _, field := range strings.Fields(preflightOut) {
		if field == SplitCapability {
			return nil
		}
	}
	return errors.New("candidate predates the local config split (no split=1 in its --preflight); refusing re-exec")
}

// SplitGuardPreflight is the candidate check a split-aware daemon's watcher
// runs: the candidate's own --preflight decides fitness, and the split token
// in its output decides whether this machine may follow it.
func SplitGuardPreflight(splitActive bool) func(context.Context, string) error {
	return func(ctx context.Context, path string) error {
		out, err := exec.CommandContext(ctx, path, "daemon", "--preflight").CombinedOutput()
		if err != nil {
			if line := text.FirstLine(string(out)); line != "" {
				return errors.New(line)
			}
			return err
		}
		return CheckSplitCapability(string(out), splitActive)
	}
}

package doctor

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/view"
)

// DefaultTmpWarnBytes is the default warning size for one relevo tmp root:
// the sweep is meant to keep the root far under a full ten GiB, so a root
// that big means the sweep is not keeping up.
const DefaultTmpWarnBytes int64 = 10 << 30

// TmpRootCheck builds the disk row for one relevo tmp root from the numbers
// a bounded walk measured. They arrive as plain integers so internal/doctor
// never imports internal/relevo; truncated says every number is a floor.
func TmpRootCheck(rounds, checks, loose int64, truncated bool, warnBytes int64) Check {
	c := Check{
		Name:   "tmp root",
		Detail: fmt.Sprintf("%s in rounds, %s in checks, %s loose", view.HumanBytes(rounds), view.HumanBytes(checks), view.HumanBytes(loose)),
	}
	if truncated {
		c.Detail += " (the walk ran out of budget: at least this much)"
	}
	if rounds+checks+loose > warnBytes {
		c.Severity = SevWarn
		c.Fix = "the daemon sweeps rounds/ and checks/ once their records end and day-old loose directories; to clear now: rm -rf <tmp root>/rounds/* <tmp root>/checks/* and any stale loose directory"
	}
	return c
}

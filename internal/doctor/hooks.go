package doctor

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/hooks"
)

// hooksWindow bounds the failure count the hooks row reports: the last day.
const hooksWindow = 24 * time.Hour

// HooksCheckInput is the recorded runs, oldest first, and the clock the
// 24-hour window is measured against.
type HooksCheckInput struct {
	Runs []hooks.HookRun
	Now  time.Time
}

// HooksCheck reports "hooks: N runs, M failed in the last 24h", plus the
// last failure's event and error, on one line.
func HooksCheck(in HooksCheckInput) Check {
	failed := 0
	last := -1
	for i, run := range in.Runs {
		if run.Error == "" {
			continue
		}
		last = i
		if !run.At.IsZero() && in.Now.Sub(run.At) > hooksWindow {
			continue
		}
		failed++
	}

	detail := fmt.Sprintf("hooks: %d runs, %d failed in the last 24h", len(in.Runs), failed)
	if last >= 0 {
		detail += fmt.Sprintf(" (last: %s: %s)", in.Runs[last].Event, firstLine(in.Runs[last].Error))
	}

	c := Check{Name: "hooks", Detail: detail}
	if failed == 0 {
		c.Severity = SevOK
	} else {
		c.Severity = SevWarn
	}
	return c
}

// firstLine keeps an error carrying newlines from splitting the row.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

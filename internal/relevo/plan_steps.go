package relevo

import (
	"regexp"
	"strings"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

var (
	planOrderedLine = regexp.MustCompile(`^\s*\d+[.)]\s+\S`)
	planBulletLine  = regexp.MustCompile(`^\s*[-*]\s+\S`)
	planStepLine    = regexp.MustCompile(`(?i)^\s*(?:#+\s*)?step\s+\d+`)
)

// planHasSteps reports whether a plan body carries steps a builder can run. It
// is true when the body has at least one ordered-list line (`1.` or `1)`
// followed by text), at least two bullet lines (`-` or `*` followed by text),
// or a line starting `step <n>` (case-insensitive, after an optional heading
// marker). Empty or whitespace text, and a body of fewer than three non-empty
// lines with none of those, is false: that is a sentence pointing at a path.
// It errs towards true, because a false negative halts a chain on a valid plan
// while a false positive only hands the builder what it would have got anyway.
// The trailing relevo block is cut first: every reader's message ends with one,
// and its lines would otherwise count a one-line pointer as a plan.
func planHasSteps(body []byte) bool {
	body = reporttail.StripTail(body)
	bullets, nonEmpty := 0, 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		switch {
		case planOrderedLine.MatchString(line), planStepLine.MatchString(line):
			return true
		case planBulletLine.MatchString(line):
			bullets++
		}
	}
	return bullets >= 2 || nonEmpty >= 3
}

func chainStepWritesPlan(def workflow.Definition, step string) bool {
	switch def.Steps[step].Seed {
	case "shipped:correct", "shipped:fix":
		return true
	}
	return false
}

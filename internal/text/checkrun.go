package text

import (
	"fmt"
	"strconv"
	"strings"
)

// ChainCheckRunLabel is the line above a check log the gate section shows, so a
// reader can tell it from the round's own gate log.
func ChainCheckRunLabel(run int) string {
	return fmt.Sprintf("chain check run %d", run)
}

// NewestCheckRun is the highest run number among round's chain check logs, or 0
// when the round holds none. The highest run is the attempt the round was left at.
func NewestCheckRun(names []string, round int) int {
	newest := 0
	for _, n := range names {
		if run, ok := CheckLogRun(n, round); ok && run > newest {
			newest = run
		}
	}
	return newest
}

// CheckLogRun parses a round-file name shaped NNN-check-RRR.log into its run
// number; ok is false for every other name. The store writes that shape for
// nothing else: a served check log is NNN-served-check.log, and a consult's files
// do not end .log.
func CheckLogRun(n string, round int) (int, bool) {
	base, ok := strings.CutSuffix(n, ".log")
	if !ok {
		return 0, false
	}
	parts := strings.Split(base, "-")
	if len(parts) != 3 || parts[1] != "check" {
		return 0, false
	}
	r, err := strconv.Atoi(parts[0])
	if err != nil || r != round {
		return 0, false
	}
	run, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, false
	}
	return run, true
}

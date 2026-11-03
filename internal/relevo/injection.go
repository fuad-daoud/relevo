package relevo

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/housekeep"
)

type injectionScan struct {
	Flagged   int
	FlaggedBy string // "", "regex"
}

// scanForInjection runs the regex floor over the text. It never fails: an
// empty match set reports zero flagged with no judge named.
func scanForInjection(rt Runtime, text []byte) injectionScan {
	regexLines := scanRegexFloor(rt, text)
	by := ""
	if len(regexLines) > 0 {
		by = "regex"
	}
	return injectionScan{
		Flagged:   len(regexLines),
		FlaggedBy: by,
	}
}

// scanRegexFloor runs the regex patterns over the text: the floor every scan
// reports.
func scanRegexFloor(rt Runtime, text []byte) []int {
	return housekeep.ScanLines(text, housekeep.CompileScanPatterns(rt.Policy))
}

// flaggedParenthetical is the caveat the two scan sites append to the text they
// record for the round: the flagged count and where the lines are listed.
// Empty when flagged <= 0, always.
func flaggedParenthetical(flagged int) string {
	if flagged <= 0 {
		return ""
	}
	unit := "lines"
	if flagged == 1 {
		unit = "line"
	}
	return fmt.Sprintf(" (%d instruction-shaped %s flagged; see relevo show --log)", flagged, unit)
}

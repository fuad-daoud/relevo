package doctor

import (
	"fmt"
)

// openRoundsCheck reports rounds left open on finished bindings. Doctor has no
// repair path, so the row only reports.
func openRoundsCheck(rounds, bindings int) Check {
	if rounds == 0 {
		return Check{Name: "open rounds", Severity: SevOK, Detail: "no open round on a finished binding"}
	}
	return Check{
		Name:     "open rounds",
		Severity: SevWarn,
		Detail:   fmt.Sprintf("%d round(s) still open on %d finished binding(s); report only, doctor repairs nothing", rounds, bindings),
		Fix:      "relevo history --q 'outcome:open'",
	}
}

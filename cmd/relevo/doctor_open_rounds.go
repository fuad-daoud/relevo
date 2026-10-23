package main

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/doctor"
)

// openRoundsCheck reports rounds left open on finished bindings. Doctor has no
// repair path, so the row only reports.
func openRoundsCheck(rounds, bindings int) doctor.Check {
	if rounds == 0 {
		return doctor.Check{Name: "open rounds", Severity: doctor.SevOK, Detail: "no open round on a finished binding"}
	}
	return doctor.Check{
		Name:     "open rounds",
		Severity: doctor.SevWarn,
		Detail:   fmt.Sprintf("%d round(s) still open on %d finished binding(s); report only, doctor repairs nothing", rounds, bindings),
		Fix:      "relevo history --q 'outcome:open'",
	}
}

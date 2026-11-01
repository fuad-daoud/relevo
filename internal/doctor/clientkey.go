package doctor

// The row says this machine's identity is not the one its servers were
// enrolled for, read from local state alone. The severity is a warning and
// not a failure because a replaced machine still runs: what it cannot do is
// be recognised by the servers it was enrolled on.

type ClientKeyReplacedInput struct {
	// Swapped says the machine's file carries the record.
	Swapped bool
	// From is the client id the machine had before the arrival.
	From string
	// To is the client id it has now.
	To string
}

// ClientKeyCheck is the row. A machine that never had its key replaced has
// no row at all: there is nothing to report where every other line is a
// finding.
func ClientKeyCheck(in ClientKeyReplacedInput) (Check, bool) {
	if !in.Swapped || in.To == "" {
		return Check{}, false
	}
	detail := "this machine's client id is " + in.To
	if in.From != "" {
		detail += ", replacing " + in.From
	}
	detail += "; the servers need the new enrolment line"
	return Check{
		Name:     "client id",
		Severity: SevWarn,
		Detail:   detail,
		// The line is printed first and enrolled second because the second needs
		// the first as an argument, and a fix that cannot be pasted in order is a
		// fix nobody runs.
		Fix: "relevo config server key --enroll-line && relevo serve enroll --key \"<line>\"",
	}, true
}

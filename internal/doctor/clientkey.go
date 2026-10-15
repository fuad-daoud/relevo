package doctor

// The row that says this machine's identity is not the one its servers were
// enrolled for. It is read from local state alone: which client id this machine
// had and which it has now is a fact its own file carries, and the fix is a
// command rather than a probe.
//
// The severity is a warning and not a failure because a machine whose identity
// was replaced still runs: what it cannot do is be recognised by the servers it
// was enrolled on, and that is a fact for whoever is setting that fleet up to
// act on, not a reason to stop.

// ClientKeyReplacedInput is the one fact the row needs: this machine's file
// recorded a swap, and the two identities it names.
type ClientKeyReplacedInput struct {
	// Swapped says the machine's file carries the record.
	Swapped bool
	// From is the client id the machine had before the arrival.
	From string
	// To is the client id it has now.
	To string
}

// ClientKeyCheck is the row. A machine that never had its key replaced has no
// row at all rather than an "ok" one: there is nothing to report, and a row that
// says so would sit in a report where every other line is a finding.
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
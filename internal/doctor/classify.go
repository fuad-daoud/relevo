package doctor

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/classify"
)

// ClassifyCheck is the one global doctor row for the classifier; it never
// fails doctor, since regex runs regardless.
//
//	!st.Configured                -> SevOK,   "regex only (no classify block in policy.json)"
//	Configured, KeySource "env"   -> SevOK,   "<model>; key from TYPESAFE_API_KEY; not passed to builders"
//	Configured, KeySource "db"    -> SevOK,   "<model>; key from the database"
//	Configured, KeySource ""      -> SevWarn, "<model> configured but no classifier key; the daemon falls back to regex"
func ClassifyCheck(st classify.Status) Check {
	c := Check{Name: "classify"}
	if !st.Configured {
		c.Severity = SevOK
		c.Detail = "regex only (no classify block in policy.json)"
		return c
	}
	switch st.KeySource {
	case "env":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s; key from TYPESAFE_API_KEY; not passed to builders", st.Model)
	case "db":
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("%s; key from the database", st.Model)
	default:
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("%s configured but no classifier key; the daemon falls back to regex", st.Model)
		c.Fix = "set TYPESAFE_API_KEY for the daemon, or store a key in the database"
	}
	return c
}

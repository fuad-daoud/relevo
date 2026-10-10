package syncworker

import "regexp"

// logRowOverhead is what a log row costs beside its body, for the columns the
// body does not carry.
const logRowOverhead = 96

// quotaWord is the error code Turso documents for a plan quota. It is matched
// as a whole word, case-sensitively: it is a code and not a sentence, and
// matching inside a longer word would latch on unrelated text.
var quotaWord = regexp.MustCompile(`\bBLOCKED\b`)

// isQuotaRefusal reports whether err carries Turso's quota code. The driver's
// TURSO_DATABASE_FULL is deliberately not matched: it is SQLite's local
// disk-full code, and latching it would call a full local disk a reached quota.
func isQuotaRefusal(err error) bool {
	return quotaWord.MatchString(err.Error())
}
